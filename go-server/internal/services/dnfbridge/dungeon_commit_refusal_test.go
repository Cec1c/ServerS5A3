package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Fail a selected member's settlement after its normal writes, inside the repository
// transaction. In a party the follower can still commit before the leader.
type dungeonCommitRefusalSettlement struct {
	dnfrepo.CharacterSettlementUnitOfWork
	injected int
	failID   string
}

func (s *dungeonCommitRefusalSettlement) WithinCharacterSettlement(ctx context.Context, id string, apply func(dnfrepo.Group) error) error {
	if id != s.failID {
		return s.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, apply)
	}
	return s.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(tx dnfrepo.Group) error {
		if err := apply(tx); err != nil {
			return err
		}
		s.injected++
		return errors.New("injected dungeon commit failure")
	})
}

func injectDungeonCommitSettlementFailure(t *testing.T, s *Service, failIDs ...string) func() {
	t.Helper()
	original := s.repositoryProvider
	group, ok := original()
	if !ok || group.CharacterSettlement == nil {
		t.Fatal("missing fixture settlement repository")
	}
	failID := "99"
	if len(failIDs) > 0 {
		failID = failIDs[0]
	}
	fault := &dungeonCommitRefusalSettlement{CharacterSettlementUnitOfWork: group.CharacterSettlement, failID: failID}
	group.CharacterSettlement = fault
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return group, true }
	t.Cleanup(func() {
		if fault.injected != 1 {
			t.Errorf("injected %d settlement failures, want one", fault.injected)
		}
	})
	return func() { s.repositoryProvider = original }
}

func TestDungeonCommitRefusalSoloRetry(t *testing.T) {
	s, session := newDungeonSelectRefusalFixture(t, dnfrepo.CharacterRecord{
		CharacterID: "99", AccountID: "account-1", Level: 20,
		Stats: map[string]int64{
			"fatigue": 100, "town_id": 38, "area_id": 1,
			"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
		},
	}, nil)
	group, _ := s.repositoryGroup()
	if err := group.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	bindDungeonSelectorOriginForTestAt(t, s, session, 38, 1, 450, 234)
	restore := injectDungeonCommitSettlementFailure(t, s)
	body := doc104SelectBody()
	if err := s.handleDungeonSelectUpper(session, body); err != nil {
		t.Fatal(err)
	}
	log := testPacketLogText(t, s)
	if !strings.Contains(log, "reason=dungeon_runtime_commit_failed") {
		t.Fatalf("request missed the injected runtime commit failure:\n%s", log)
	}
	if sessionHasActiveDungeonRuntime(session) {
		t.Fatal("failed entry installed a runtime")
	}
	character, found, err := group.Character.Load(context.Background(), "99")
	if err != nil || !found || character.Stats["fatigue"] != 100 {
		t.Fatalf("failed settlement changed fatigue: character=%+v found=%t err=%v", character, found, err)
	}
	conn := session.conn.(*bufferConn)
	if conn.write.Len() == 0 {
		t.Fatal("runtime commit failed without an op16 refusal")
	}
	refusal, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if refusal.Header.Classification != 1 || refusal.Header.MsgID != 16 || !bytes.Equal(refusal.Body, []byte{0, 1, 0}) {
		t.Fatalf("commit refusal header=%+v body=%x, want class 1 op16 {00 01 00}", refusal.Header, refusal.Body)
	}
	assertRiftUnlockSystemText(t, rest, "地下城入场未能完成。")
	if !strings.Contains(log, "failure_class=entry_unavailable") {
		t.Fatalf("commit refusal lacks failure class:\n%s", log)
	}

	restore()
	conn.write.Reset()
	if err := s.handleDungeonSelectUpper(session, body); err != nil {
		t.Fatal(err)
	}
	entered := session.dungeon.runtime
	if entered == nil || entered.Dungeon.ID != 700 {
		t.Fatal("retry did not enter dungeon 700")
	}
	acknowledged := false
	for stream := conn.write.Bytes(); len(stream) != 0; {
		packet, next := splitGameServerUpperPacket(t, stream)
		if packet.Header.Classification == 1 && packet.Header.MsgID == 16 {
			acknowledged = bytes.Equal(packet.Body, upperSuccessBody(buildSelectDungeonBodyForCharacter(99)))
		}
		stream = next
	}
	if !acknowledged {
		t.Fatal("retry did not return the op16 success ACK")
	}

	conn.write.Reset()
	body[4]++ // Same dungeon, different difficulty while the recovered runtime is active.
	if err := s.handleDungeonSelectUpper(session, body); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 || session.dungeon.runtime != entered {
		t.Fatalf("mismatched duplicate disturbed the active run: output=%x", conn.write.Bytes())
	}
}

func TestDungeonCommitRefusalRoutedOwnerAfterFollowerRollback(t *testing.T) {
	for _, failID := range []string{"99", "100"} {
		t.Run(failID, func(t *testing.T) {
			f := newDoc104PartyFixture(t, 20, true)
			doc104AuditStartLoop(t, f)
			injectDungeonCommitSettlementFailure(t, f.service, failID)
			token := f.service.bindPartyDungeonEntryRoutedPending(f.follower)
			if token == nil {
				t.Fatal("could not bind follower request")
			}
			if err := f.service.handleDungeonSelectUpperPlanned(f.leader, doc104SelectBody(), nil, true, token); err != nil {
				t.Fatal(err)
			}
			doc104AssertNoEntrySequence(t, f)
			if doc104PendingCharacterForTest(f.follower) != 0 {
				t.Fatal("follower still waiting for routed request")
			}
			log := testPacketLogText(t, f.service)
			site := "dungeon_runtime_commit_failed"
			if failID == "100" {
				site = "party_follower_runtime_commit_failed_before_leader_commit"
			}
			if !strings.Contains(log, "reason="+site) {
				t.Fatalf("routed entry missed injected commit failure:\n%s", log)
			}
			for _, id := range []uint16{99, 100} {
				steps := doc104StepsFor(t, f, id)
				frames := doc104Op16Steps(steps)
				if len(frames) != 1 {
					t.Fatalf("char %d received %d op16 replies, want one refusal", id, len(frames))
				}
				doc104AssertRefusalFrames(t, id, frames, 1)
				notices := 0
				for i, step := range steps {
					if step.Classification == 0 && step.MsgID == 12 {
						if i == 0 || steps[i-1].MsgID != 16 {
							t.Fatalf("char %d received system text outside the op16 refusal", id)
						}
						notices++
					}
				}
				if notices != 1 {
					t.Fatalf("char %d received %d system notices, want one", id, notices)
				}
			}
		})
	}
}
