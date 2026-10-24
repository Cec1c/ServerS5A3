package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/characterdata"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// MySQL rejects an expired context even when the selected row still exists.
// The memory store does not necessarily implement that transport boundary.
type selectContextCharacterStore struct{ dnfrepo.CharacterRepository }

func (r selectContextCharacterStore) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return dnfrepo.CharacterRecord{}, false, err
	}
	return r.CharacterRepository.Load(ctx, id)
}

func TestLegacySelectSnapshotSurvivesIndependentGuildPresenceTimeout(t *testing.T) {
	s, repos := newAccountRecordBootstrapFixture(t)
	s.options.currentClientVersion = "official-24.9"
	p := newAccountRecordBootstrapSession()
	slow := &selectSlowGuildPresence{}
	repos.Guild = slow
	repos.Character = selectContextCharacterStore{repos.Character}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	body := make([]byte, 11)
	binary.LittleEndian.PutUint32(body, 10)
	if err := s.sendSelectCharacterState(p, body); err != nil {
		t.Fatal(err)
	}
	if p.selectedCharacterID != 10 || slow.calls != 1 || p.conn.(*bufferConn).write.Len() == 0 {
		t.Fatalf("selected=%d guild calls=%d; legacy selection lost its snapshot after presence timeout", p.selectedCharacterID, slow.calls)
	}
}

type selectReadFailureStore struct {
	dnfrepo.CharacterRepository
	err error
}

func (r selectReadFailureStore) Load(context.Context, string) (dnfrepo.CharacterRecord, bool, error) {
	return dnfrepo.CharacterRecord{}, false, r.err
}

func TestSelectionRevalidationStillRejectsStaleOwnerAndUnavailableSnapshot(t *testing.T) {
	for _, mode := range []string{"account", "slot", "name", "pending_rename", "missing", "deadline", "canceled", "storage_failure"} {
		t.Run(mode, func(t *testing.T) {
			s, repos := newAccountRecordBootstrapFixture(t)
			s.options.currentClientVersion = "official-24.9"
			p := newAccountRecordBootstrapSession()
			ctx := context.Background()
			selected, _, err := repos.Character.Load(ctx, "10")
			if err != nil {
				t.Fatal(err)
			}
			current := dnfrepo.CloneCharacter(selected)
			switch mode {
			case "account":
				current.AccountID = "different_owner"
			case "slot":
				current.Slot++
			case "name":
				current.Name = "changed"
			case "pending_rename":
				current.Stats[characterdata.RenameEntitlementStat] = 1
			case "missing":
				selected.CharacterID = "missing"
			case "deadline":
				repos.Character = selectReadFailureStore{repos.Character, context.DeadlineExceeded}
			case "canceled":
				repos.Character = selectReadFailureStore{repos.Character, context.Canceled}
			case "storage_failure":
				repos.Character = selectReadFailureStore{repos.Character, errors.New("unavailable")}
			}
			if err := repos.Character.Save(ctx, current); err != nil {
				t.Fatal(err)
			}
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			if s.currentRenameSelectionStillCurrent(ctx, p, selected) {
				t.Fatal("unsafe selection admitted")
			}
		})
	}
}

type selectSlowGuildPresence struct{ calls int }

func (r *selectSlowGuildPresence) WithinGuild(ctx context.Context, _ []dnfrepo.GuildOwner, _ func(dnfrepo.Group) error) error {
	r.calls++
	<-ctx.Done()
	// Allow timeout cleanup to finish after cancellation. The outer selector
	// and independent presence deadlines can be only microseconds apart;
	// returning in the same timer tick would race their cancellation callbacks.
	time.Sleep(50 * time.Millisecond)
	return ctx.Err()
}

func TestSelectSnapshotSurvivesIndependentGuildPresenceTimeout(t *testing.T) {
	s, repos := newAccountRecordBootstrapFixture(t)
	s.options.currentClientVersion = "official-24.9"
	s.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
	p := newAccountRecordBootstrapSession()
	slow := &selectSlowGuildPresence{}
	repos.Guild = slow
	repos.Character = selectContextCharacterStore{repos.Character}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	started := time.Now()
	err := s.sendUpperCSharpSelectInit(p, selectFirstSlotRequest())
	if err != nil {
		t.Fatal(err)
	}
	packets := splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes())
	ack := false
	for _, packet := range packets {
		if packet.Header.Classification == 1 && packet.Header.MsgID == 7 {
			t.Fatalf("unchanged character was returned to selector after %v; guild calls=%d", time.Since(started), slow.calls)
		}
		if packet.Header.Classification == 1 && packet.Header.MsgID == 4 {
			ack = true
		}
	}
	if !ack || p.selectedCharacterID != 10 || slow.calls != 1 {
		t.Fatalf("selected=%d ack=%t guild calls=%d", p.selectedCharacterID, ack, slow.calls)
	}
}
