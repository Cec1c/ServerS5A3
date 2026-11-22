package dnfbridge

import "testing"

func TestReconcileRaidTableReturnDowngradesAfterOp24Sent(t *testing.T) {
	s := &Service{}
	session := &gameSession{selectedCharacterID: 2002}

	// op24 尚未发送前发生状态漂移：保持原有拒绝语义
	rtBefore := &runtimeDungeonState{
		townReturnSource:   currentRaidTablePartyReturnSource,
		townReturnOp24Sent: false,
	}
	allow, reason := s.reconcileRaidTableReturnBeforeTownCommitLocked(session, rtBefore, "test_before_op24")
	if allow || reason == "" {
		t.Fatalf("expected rejection before op24 sent, got allow=%v reason=%q", allow, reason)
	}

	// op24 已发送（客户端已切回城镇）：降级放行，避免断开队友 TCP
	rtAfter := &runtimeDungeonState{
		townReturnSource:   currentRaidTablePartyReturnSource,
		townReturnOp24Sent: true,
		raidTablePartyReturn: &currentRaidTablePartyReturn{
			leader: boundGameSessionCharacter{character: 1001},
		},
	}
	allow, reason = s.reconcileRaidTableReturnBeforeTownCommitLocked(session, rtAfter, "test_after_op24")
	if !allow || reason != "" {
		t.Fatalf("expected downgrade allow after op24 sent, got allow=%v reason=%q", allow, reason)
	}
	if rtAfter.raidTablePartyReturn != nil {
		t.Fatalf("expected stale raidTablePartyReturn to be cleared on downgrade")
	}
}

func TestPartyUDPRelayNoteForwardedLockFreeCounters(t *testing.T) {
	leg := &currentPartyUDPRelayLeg{}
	fwd1, bytes1, first1 := leg.noteForwarded(128)
	if fwd1 != 1 || bytes1 != 128 || !first1 {
		t.Fatalf("first forward mismatch: fwd=%d bytes=%d first=%v", fwd1, bytes1, first1)
	}
	fwd2, bytes2, first2 := leg.noteForwarded(256)
	if fwd2 != 2 || bytes2 != 384 || first2 {
		t.Fatalf("second forward mismatch: fwd=%d bytes=%d first=%v", fwd2, bytes2, first2)
	}
	totalFwd, totalBytes := leg.ForwardedDatagrams()
	if totalFwd != 2 || totalBytes != 384 {
		t.Fatalf("ForwardedDatagrams mismatch: fwd=%d bytes=%d", totalFwd, totalBytes)
	}
}
