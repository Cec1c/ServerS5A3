package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

// Pair the two preflight reads, and (on the old nontransactional path) the
// two read-modify-write reads. Both requests see the same unselected target.
// The real memory aggregate remains untouched and supplies transaction locking.
type epicStartPairedReads struct {
	repo.CharacterRepository
	mu    sync.Mutex
	reads int
	pairs [2]chan struct{}
}

type epicStartAbortAssets struct {
	repo.CharacterAssetUnitOfWork
	failure error
}

func (u epicStartAbortAssets) WithinCharacterAssets(ctx context.Context, id string, apply func(repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return u.CharacterAssetUnitOfWork.WithinCharacterAssets(ctx, id, func(c repo.CharacterRepository, i repo.InventoryRepository, e repo.EquipmentRepository) error {
		if err := apply(c, i, e); err != nil {
			return err
		}
		return u.failure
	})
}

func TestEpicProductionStartTransactionRollbackAndPreservedStats(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	before := repo.CharacterRecord{CharacterID: "3", AccountID: "owner", Level: 90, Stats: map[string]int64{currentEpicProductionChargeStat: 23000, currentEpicProductionWeeklyCountStat: 2, currentEpicProductionCarryGroup1Stat: 1234, "unrelated": 7}}
	if err := g.Character.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("abort after target save")
	changed, err := commitCurrentEpicProductionStart(ctx, epicStartAbortAssets{g.CharacterAssets, failure}, "owner", "3", 108000520, 1)
	if !errors.Is(err, failure) || changed {
		t.Fatalf("uncommitted result changed=%t err=%v", changed, err)
	}
	after, _, _ := g.Character.Load(ctx, "3")
	if !reflect.DeepEqual(before.Stats, after.Stats) {
		t.Fatalf("rollback changed stats: %v", after.Stats)
	}
	changed, err = commitCurrentEpicProductionStart(ctx, g.CharacterAssets, "owner", "3", 108000520, 1)
	if err != nil || !changed {
		t.Fatalf("start: changed=%t err=%v", changed, err)
	}
	after, _, _ = g.Character.Load(ctx, "3")
	want := repo.CloneCharacter(before)
	want.Stats[currentEpicProductionTargetStat] = 108000520
	if !reflect.DeepEqual(want.Stats, after.Stats) {
		t.Fatalf("start lost progress/carry/unrelated stats: %v", after.Stats)
	}
	changed, err = commitCurrentEpicProductionStart(ctx, g.CharacterAssets, "owner", "3", 108000520, 1)
	if err != nil || changed {
		t.Fatalf("retry: changed=%t err=%v", changed, err)
	}
	if changed, err := commitCurrentEpicProductionStart(ctx, nil, "owner", "3", 108000520, 1); err == nil || changed {
		t.Fatal("missing transaction accepted")
	}
	if changed, err := commitCurrentEpicProductionStart(ctx, g.CharacterAssets, "foreign", "3", 108000520, 1); err == nil || changed {
		t.Fatal("foreign owner accepted")
	}
}

func TestEpicProductionRealPVFStartMissingTransactionRejectsWithoutMutation(t *testing.T) {
	s, session, conn := newCurrentEpicProductionRealPVFService(t, epicProductionSessionAccount, epicProductionAccountScopeSession, epicProductionInfoPushAlways)
	s.options.currentClientVersion = "official-24.9"
	g, _ := s.repositoryGroup()
	before, _, _ := g.Character.Load(context.Background(), "3")
	modified := g
	modified.CharacterAssets = nil
	s.repositoryProvider = func() (repo.Group, bool) { return modified, true }
	if err := s.handleCurrentEpicProductionStart(session, epicProductionStartRequestBody(t, 108000520)); err != nil {
		t.Fatal(err)
	}
	packet, tail := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != 1415 || len(tail) != 0 || !reflect.DeepEqual(packet.Body, []byte{0, 3}) {
		t.Fatalf("unexpected missing-transaction reply %x", conn.write.Bytes())
	}
	after, _, _ := g.Character.Load(context.Background(), "3")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("missing transaction mutated character")
	}
}

func (r *epicStartPairedReads) Load(ctx context.Context, id string) (repo.CharacterRecord, bool, error) {
	record, found, err := r.CharacterRepository.Load(ctx, id)
	if err != nil {
		return record, found, err
	}
	r.mu.Lock()
	n := r.reads
	r.reads++
	if n < 4 && n%2 == 1 {
		close(r.pairs[n/2])
	}
	r.mu.Unlock()
	if n < 4 {
		select {
		case <-r.pairs[n/2]:
		case <-ctx.Done():
			return repo.CharacterRecord{}, false, ctx.Err()
		}
	}
	return record, found, nil
}

func TestEpicProductionRealPVFConcurrentStartHasOneWinner(t *testing.T) {
	s, session, _ := newCurrentEpicProductionRealPVFService(t, epicProductionSessionAccount, epicProductionAccountScopeSession, epicProductionInfoPushAlways)
	s.options.currentClientVersion = "official-24.9"
	g, _ := s.repositoryGroup()
	paired := &epicStartPairedReads{CharacterRepository: g.Character, pairs: [2]chan struct{}{make(chan struct{}), make(chan struct{})}}
	modified := g
	modified.Character = paired
	s.repositoryProvider = func() (repo.Group, bool) { return modified, true }
	// Both literal targets are real [at priest] production weapons; validate
	// them through the actual handler and resource catalogue, not a fake owner.
	targets := []uint32{108000520, 108010509}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := parseCurrentEpicProductionCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.allows("[at priest]", targets[1]) {
		t.Fatalf("literal second target %d absent from real job catalogue", targets[1])
	}
	type reply struct {
		target uint32
		body   []byte
		err    error
	}
	done := make(chan reply, 2)
	for _, target := range targets {
		go func(target uint32) {
			conn := &bufferConn{}
			local := &gameSession{conn: conn, selectedCharacterID: session.selectedCharacterID, accountID: session.accountID}
			err := s.handleCurrentEpicProductionStart(local, epicProductionStartRequestBody(t, target))
			done <- reply{target, append([]byte(nil), conn.write.Bytes()...), err}
		}(target)
	}
	winners := 0
	var winner uint32
	for range targets {
		select {
		case r := <-done:
			if r.err != nil {
				t.Fatal(r.err)
			}
			packet, tail := splitLongHengGameServerUpperPacket(t, r.body)
			if packet.Header.MsgID != 1415 || len(tail) != 0 {
				t.Fatalf("unexpected reply %x", r.body)
			}
			if len(packet.Body) == 9 && packet.Body[0] == 1 {
				winners++
				winner = r.target
				if binary.LittleEndian.Uint32(packet.Body[1:5]) != winner {
					t.Fatal("ACK target differs")
				}
			} else if len(packet.Body) != 2 || packet.Body[0] != 0 || packet.Body[1] != currentEpicProductionAlreadyActive {
				t.Fatalf("unexpected rejection %x", packet.Body)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent start did not finish")
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent different targets acknowledged %d winners; want one", winners)
	}
	after, found, err := g.Character.Load(context.Background(), "3")
	if err != nil || !found || after.Stats[currentEpicProductionTargetStat] != int64(winner) {
		t.Fatalf("persisted target does not match sole ACK: %+v %v", after.Stats, err)
	}
}
