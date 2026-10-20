package dnfbridge

import (
	"context"
	"errors"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestGriefCompletedOp42DoesNotWaitForUnrequestedOrdinaryCards(t *testing.T) {
	s, rt, session, conn := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
	rt.Dungeon.Metadata.IntegerValues = map[string]int64{"tower of grief": 1}
	completeSettlementExitBarriersForTest(t, s, rt, session)
	conn.write.Reset()
	if err := s.handleDungeonGiveupGame(session, nil); err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime != nil || !rt.townReturnOp24Sent {
		t.Fatalf("completed grief floor still waits for ordinary cards: bytes=%d phase=%v", conn.write.Len(), rt.settlementPhase)
	}
	if rt.settlementCardLayoutSent || rt.settlementCardSelectionSent || rt.settlementCardRewardCommitted {
		t.Fatal("grief exit fabricated an ordinary card reward")
	}
	assertCompletedDungeonTownRouteWithGiveupAckForTest(t, conn.write.Bytes(), rt.townReturnTransition.Body)
	conn.write.Reset()
	if err := s.handleDungeonGiveupGame(session, nil); err != nil || conn.write.Len() != 0 {
		t.Fatalf("retired grief exit replay: err=%v bytes=%d", err, conn.write.Len())
	}
}

func TestGriefCompletedExitRequiresCommittedOwnedResults(t *testing.T) {
	for _, name := range []string{"valid", "no_marker", "marker_zero", "marker_two", "missing_result", "missing_statistic", "missing_notice", "unpersisted", "missing_plan", "wrong_plan_owner", "wrong_completion", "empty_source", "cards_started", "card_scroll_started", "card_selected", "card_committed"} {
		t.Run(name, func(t *testing.T) {
			s, rt, session, conn := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
			rt.Dungeon.Metadata.IntegerValues = map[string]int64{"tower of grief": 1}
			completeSettlementExitBarriersForTest(t, s, rt, session)
			switch name {
			case "no_marker":
				delete(rt.Dungeon.Metadata.IntegerValues, "tower of grief")
			case "marker_zero":
				rt.Dungeon.Metadata.IntegerValues["tower of grief"] = 0
			case "marker_two":
				rt.Dungeon.Metadata.IntegerValues["tower of grief"] = 2
			case "missing_result":
				rt.settlementPlayResultReceived = false
			case "missing_statistic":
				rt.settlementStatisticReceived = false
			case "missing_notice":
				rt.settlementClearRewardSent = false
			case "unpersisted":
				rt.clearMapCompletionPhaseAPersisted = false
			case "missing_plan":
				rt.settlementResultPlan = nil
			case "wrong_plan_owner":
				rt.settlementResultPlan.CharacterID++
			case "wrong_completion":
				rt.settlementResultPlan.CompletionKey += ":other"
			case "empty_source":
				rt.settlementResultPlan.Source = ""
			case "cards_started":
				rt.settlementCardLayoutSent = true
			case "card_scroll_started":
				rt.settlementCardScrollStateSent = true
			case "card_selected":
				rt.settlementCardSelectionSent = true
			case "card_committed":
				rt.settlementCardRewardCommitted = true
			}
			conn.write.Reset()
			if got := currentGriefCompletedResultExitReady(rt, session.selectedCharacterID); got != (name == "valid") {
				t.Fatalf("exit ready=%v", got)
			}
			if name != "valid" && name != "cards_started" && name != "card_selected" && name != "card_committed" {
				if err := s.handleDungeonGiveupGame(session, nil); err != nil {
					t.Fatal(err)
				}
				if session.dungeon.runtime != rt || rt.townReturnOp24Sent || conn.write.Len() != 0 {
					t.Fatal("incomplete grief result bypassed exit barriers")
				}
			}
		})
	}
}

func TestGriefCompletedExitWriteFailureKeepsOwnerAndRetriesOnlyTail(t *testing.T) {
	s, rt, session, _ := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
	rt.Dungeon.Metadata.IntegerValues = map[string]int64{"tower of grief": 1}
	completeSettlementExitBarriersForTest(t, s, rt, session)
	want := errors.New("grief op24 write failed")
	failing := &failCompletedTownOp24WriteConn{t: t, err: want}
	session.conn = failing
	if err := s.handleDungeonGiveupGame(session, nil); !errors.Is(err, want) {
		t.Fatalf("exit error=%v", err)
	}
	if session.dungeon.runtime != rt || rt.townReturnOp24Sent {
		t.Fatal("failed grief exit retired owner")
	}
	if err := s.handleDungeonGiveupGame(session, nil); err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime != nil || failing.successfulOp24Writes != 1 || failing.positionWrites != 1 || failing.areaWrites != 1 {
		t.Fatal("grief exit retry duplicated successful prefix or lost owner")
	}
}

func TestRealPVFGriefFirstFloorCompletedTownReturn(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("requires fixed published PVF")
	}
	a, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	d, found := table.FindDungeon(5115)
	if !found || d.Metadata.IntegerValues["tower of grief"] != 1 || d.Metadata.IntegerValues["tower of despair"] != 0 {
		t.Fatalf("wrong actual first-floor identity: %+v", d)
	}
	s, rt, session, conn := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
	// Use actual resource identity with the existing committed-run fixture.
	// This verifies the exit policy, not a full real monster/settlement replay.
	rt.Dungeon = d
	completeSettlementExitBarriersForTest(t, s, rt, session)
	conn.write.Reset()
	if err := s.handleDungeonGiveupGame(session, nil); err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime != nil || !rt.townReturnOp24Sent || rt.settlementCardRewardCommitted {
		t.Fatal("actual grief identity did not use receipted town return")
	}
	assertCompletedDungeonTownRouteForTest(t, conn.write.Bytes(), rt.townReturnTransition.Body)
}
