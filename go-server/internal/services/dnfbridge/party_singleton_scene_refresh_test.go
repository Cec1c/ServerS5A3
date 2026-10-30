package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// TestPartySingletonSceneRefreshSendsRealtimeThenRoster pins a scene refresh
// for the leader retained after the ordinary member leaves. The singleton is
// still an active party, so the refresh must send exactly op153 followed by
// op9. Matching both packets and leaving no trailing bytes also excludes the
// op11 peer handshake and every extra frame.
func TestPartySingletonSceneRefreshSendsRealtimeThenRoster(t *testing.T) {
	h := newPartyLeaveHarness(t)
	h.dispatchMemberLeave(t)
	assertPartyLeaveLeaderSingleton(t, h)

	h.leaderConn.write.Reset()
	h.memberConn.write.Reset()

	state := h.service.runtimePartyStateForSession(h.leader)
	if err := h.service.sendRuntimePartyRosterLocal(h.leader, state, "singleton_scene_refresh_test"); err != nil {
		t.Fatalf("singleton scene refresh: %v", err)
	}

	realtime, rest := splitGameServerUpperPacket(t, h.leaderConn.write.Bytes())
	if realtime.Header.Classification != 0 || realtime.Header.MsgID != 0x0099 {
		t.Fatalf("singleton scene refresh first frame = class %d msg %d, want class0 op153",
			realtime.Header.Classification, realtime.Header.MsgID)
	}
	realtimeRows := parseRealtimeInfoRows(t, realtime.Body)
	if len(realtimeRows) != 1 || realtimeRows[0].uid != 1001 || realtimeRows[0].slot != 0 {
		t.Fatalf("singleton scene refresh op153 rows=%+v, want one row uid=1001 slot=0", realtimeRows)
	}

	partyFrame, rest := splitGameServerUpperPacket(t, rest)
	if partyFrame.Header.Classification != 0 || partyFrame.Header.MsgID != uint16(dnfenum.CmdPacketRecoverStamina) {
		t.Fatalf("singleton scene refresh second frame = class %d msg %d, want class0 op9",
			partyFrame.Header.Classification, partyFrame.Header.MsgID)
	}
	if len(partyFrame.Body) < 10 || partyFrame.Body[9] != currentSceneOp9ActorDisplayKind {
		t.Fatalf("singleton scene refresh op9 body=% X, want kind0", partyFrame.Body)
	}
	hasIdentity := int(partyFrame.Body[7]) != currentClientSelectedChannelID(h.leader)
	rows, leaderSlot := parseOp9PartyFrame(t, partyFrame.Body, hasIdentity)
	if len(rows) != 1 || rows[0].uid != 1001 || rows[0].slot != 0 || leaderSlot != 0 {
		t.Fatalf("singleton scene refresh op9 rows=%+v leaderSlot=%d, want one row uid=1001 slot=0 leaderSlot=0",
			rows, leaderSlot)
	}
	if len(rest) != 0 {
		t.Fatalf("singleton scene refresh emitted %d trailing bytes after op9", len(rest))
	}
	if h.memberConn.write.Len() != 0 {
		t.Fatalf("singleton scene refresh wrote %d bytes to the departed member", h.memberConn.write.Len())
	}
}

// TestPartySingletonSceneRefreshRejectsRetiredMembership saves the exact
// singleton state consumed by the positive refresh, performs the real final
// leader leave, and retries that old state. The state still has one member, so
// the non-empty gate cannot suppress it; only the membership guard may do so.
func TestPartySingletonSceneRefreshRejectsRetiredMembership(t *testing.T) {
	h := newPartyLeaveHarness(t)
	h.dispatchMemberLeave(t)
	assertPartyLeaveLeaderSingleton(t, h)

	oldState := h.service.runtimePartyStateForSession(h.leader)
	handled, err := h.service.handleAlignedGameCommand(
		h.leader,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketLeaveParty),
		nil,
	)
	if err != nil {
		t.Fatalf("leader final leave party: %v", err)
	}
	if !handled {
		t.Fatal("leader final LeaveParty must be handled by the online party bridge")
	}
	if snapshot, found := h.service.runtimePartyManagerForService().SnapshotByUser(1001, 0); found {
		t.Fatalf("leader remained in the runtime party after its final leave: %+v", snapshot)
	}
	if projection := runtimePartyStateSnapshot(h.leader); projection.PartyID != 0 {
		t.Fatalf("leader projection was not cleared by its final leave: %+v", projection)
	}

	h.leaderConn.write.Reset()
	h.memberConn.write.Reset()
	if err := h.service.sendRuntimePartyRosterLocal(h.leader, oldState, "singleton_scene_refresh_test"); err != nil {
		t.Fatalf("retired singleton scene refresh: %v", err)
	}
	if h.leaderConn.write.Len() != 0 {
		t.Fatalf("retired singleton scene refresh wrote %d bytes to the former leader", h.leaderConn.write.Len())
	}
	if h.memberConn.write.Len() != 0 {
		t.Fatalf("retired singleton scene refresh wrote %d bytes to the former member", h.memberConn.write.Len())
	}
}
