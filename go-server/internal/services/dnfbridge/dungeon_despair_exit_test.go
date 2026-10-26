package dnfbridge

import (
	"errors"
	"testing"
)

func TestDespairCompletedOp42DoesNotWaitForOrdinaryCards(t *testing.T) {
	s, rt, session, conn := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
	rt.Dungeon.Metadata.IntegerValues = map[string]int64{"tower of despair": 1}
	completeSettlementExitBarriersForTest(t, s, rt, session)
	conn.write.Reset()
	if err := s.handleDungeonGiveupGame(session, nil); err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime != nil || !rt.townReturnOp24Sent {
		t.Fatalf("completed tower still waits for ordinary cards: bytes=%d phase=%v", conn.write.Len(), rt.settlementPhase)
	}
	if rt.settlementCardLayoutSent || rt.settlementCardSelectionSent || rt.settlementCardRewardCommitted {
		t.Fatal("tower exit fabricated an ordinary card reward")
	}
	assertCompletedDungeonTownRouteWithGiveupAckForTest(t, conn.write.Bytes(), rt.townReturnTransition.Body)
}

func TestDespairCompletedOp42StillRequiresResultAndStatistic(t *testing.T) {
	s, rt, session, conn := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
	rt.Dungeon.Metadata.IntegerValues = map[string]int64{"tower of despair": 1}
	conn.write.Reset()
	if err := s.handleDungeonGiveupGame(session, nil); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 || session.dungeon.runtime != rt {
		t.Fatal("tower bypassed completion barriers")
	}
	plan := currentDungeonSettlementPacketPlanForTest(t, rt, 0)
	rt.settlementResultPlan = &plan
	if err := s.handleDungeonSetPlayResult(session, make([]byte, currentDungeonPlayResultBaseSize)); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	if err := s.handleDungeonGiveupGame(session, nil); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 || session.dungeon.runtime != rt {
		t.Fatal("tower bypassed statistic barrier")
	}
}

func TestDespairDoesNotCreateOrRevealOrdinaryCards(t *testing.T) {
	s, rt, session, conn := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
	rt.Dungeon.Metadata.IntegerValues = map[string]int64{"tower of despair": 1}
	if err := s.freezeCurrentDungeonCardRewardStateLocked(session, rt); err != nil {
		t.Fatal(err)
	}
	if rt.settlementCardRewardState != nil {
		t.Fatal("tower created ordinary card RNG/reward state")
	}
	completeSettlementExitBarriersForTest(t, s, rt, session)
	conn.write.Reset()
	if err := s.handleDungeonScoreScrollState(session, nil); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 || rt.settlementCardScrollStateSent {
		t.Fatal("tower opened ordinary card handshake")
	}
}

func TestDespairCompletedExitWriteFailureKeepsOwnerForRetry(t *testing.T) {
	s, rt, session, _ := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
	rt.Dungeon.Metadata.IntegerValues = map[string]int64{"tower of despair": 1}
	completeSettlementExitBarriersForTest(t, s, rt, session)
	want := errors.New("tower op24 write failed")
	failing := &failCompletedTownOp24WriteConn{t: t, err: want}
	session.conn = failing
	if err := s.handleDungeonGiveupGame(session, nil); !errors.Is(err, want) {
		t.Fatalf("exit error=%v", err)
	}
	if session.dungeon.runtime != rt || rt.townReturnOp24Sent {
		t.Fatal("failed exit retired owner")
	}
	if err := s.handleDungeonGiveupGame(session, nil); err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime != nil || failing.successfulOp24Writes != 1 || failing.positionWrites != 1 || failing.areaWrites != 1 {
		t.Fatal("retry duplicated prefix or lost owner")
	}
}

func TestDespairExitRequiresExactMarkerAndDurablePlanOwner(t *testing.T) {
	s, rt, session, _ := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
	rt.Dungeon.Metadata.IntegerValues = map[string]int64{"tower of despair": 1}
	completeSettlementExitBarriersForTest(t, s, rt, session)
	if !currentDespairCompletedResultExitReady(rt, 99) {
		t.Fatal("valid completed fixture not ready")
	}
	saved := rt.settlementResultPlan
	for _, marker := range []int64{0, 2, -1} {
		rt.Dungeon.Metadata.IntegerValues["tower of despair"] = marker
		if currentDespairCompletedResultExitReady(rt, 99) {
			t.Fatalf("unrecognized marker %d allowed exit", marker)
		}
	}
	rt.Dungeon.Metadata.IntegerValues["tower of despair"] = 1
	if currentDespairCompletedResultExitReady(rt, 98) {
		t.Fatal("wrong character owns exit")
	}
	copyPlan := *saved
	copyPlan.CompletionKey = "different-run"
	rt.settlementResultPlan = &copyPlan
	if currentDespairCompletedResultExitReady(rt, 99) {
		t.Fatal("different completion plan allowed exit")
	}
	rt.settlementResultPlan = saved
	rt.clearMapCompletionPhaseAPersisted = false
	if currentDespairCompletedResultExitReady(rt, 99) {
		t.Fatal("unpersisted completion allowed exit")
	}
}
