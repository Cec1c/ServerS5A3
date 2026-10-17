package dnfbridge

import (
	"bytes"
	"errors"
	"testing"
)

func TestDespairResultOp72TownAndSelectOther(t *testing.T) {
	for _, option := range []byte{1, 2} {
		t.Run(map[byte]string{1: "selector", 2: "town"}[option], func(t *testing.T) {
			s, rt, session, conn := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
			rt.Dungeon.Metadata.IntegerValues = map[string]int64{"tower of despair": 1}
			completeSettlementExitBarriersForTest(t, s, rt, session)
			conn.write.Reset()
			if err := s.handleDungeonEplpCommand(session, []byte{1, option}); err != nil {
				t.Fatal(err)
			}
			if conn.write.Len() == 0 {
				t.Fatal("tower result action produced no response")
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.MsgID != 72 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, buildCurrentDungeonOp72SuccessBody(1, option)) {
				t.Fatalf("ACK=%+v %x", ack.Header, ack.Body)
			}
			if session.dungeon.runtime != nil {
				t.Fatal("completed tower runtime was not retired")
			}
			if rt.settlementCardRewardCommitted || rt.settlementCardLayoutSent {
				t.Fatal("ordinary cards fabricated")
			}
			if option == 2 {
				assertCompletedDungeonTownRouteForTest(t, rest, rt.townReturnTransition.Body)
				return
			}
			seen := map[uint16]bool{}
			for len(rest) > 0 {
				p, tail := splitGameServerUpperPacket(t, rest)
				seen[p.Header.MsgID] = true
				rest = tail
			}
			if !seen[15] || !seen[currentFatigueMsgID] || !seen[currentDungeonContextMsgID] || seen[currentSceneTransitionMsgID] {
				t.Fatalf("selector sequence=%v", seen)
			}
		})
	}
}

func TestDespairResultOp72BlocksRetryAndIncompleteResult(t *testing.T) {
	for _, reason := range []string{"retry", "statistic", "result", "receipt"} {
		t.Run(reason, func(t *testing.T) {
			s, rt, session, conn := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
			rt.Dungeon.Metadata.IntegerValues = map[string]int64{"tower of despair": 1}
			completeSettlementExitBarriersForTest(t, s, rt, session)
			request := []byte{1, 2}
			switch reason {
			case "retry":
				request[1] = 0
			case "statistic":
				rt.settlementStatisticReceived = false
			case "result":
				rt.settlementDespairResultSent = false
			case "receipt":
				rt.clearMapCompletionPhaseAPersisted = false
			}
			conn.write.Reset()
			if err := s.handleDungeonEplpCommand(session, request); err != nil {
				t.Fatal(err)
			}
			if conn.write.Len() != 0 || session.dungeon.runtime != rt || rt.settlementCardExitAckSent {
				t.Fatal("invalid tower action consumed exit gate")
			}
		})
	}
}

func TestDespairResultOp72TownWriteFailureResumesWithoutDuplicateAck(t *testing.T) {
	s, rt, session, _ := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
	rt.Dungeon.Metadata.IntegerValues = map[string]int64{"tower of despair": 1}
	completeSettlementExitBarriersForTest(t, s, rt, session)
	want := errors.New("tower result button op24 write failed")
	failing := &failCompletedTownOp24WriteConn{t: t, err: want}
	session.conn = failing
	if err := s.handleDungeonEplpCommand(session, []byte{1, 2}); !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
	if session.dungeon.runtime != rt || !rt.settlementCardExitAckSent || rt.townReturnOp24Sent {
		t.Fatal("failed route lost owner")
	}
	if err := s.handleDungeonEplpCommand(session, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime != nil || failing.successfulOp24Writes != 1 || failing.positionWrites != 1 || failing.areaWrites != 1 {
		t.Fatal("retry duplicated route or retained old runtime")
	}
}
