package dnfbridge

import (
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// TestPartySingletonProbeMemberLeaveKeepsLeaderRowUntilLeaderLeavesThenReinvite
// keeps one ordinary two-member party on the real command, manager and packet
// paths. The departure order distinguishes a retained leader singleton from a
// cleared client projection, then checks that the retained singleton can be
// released and rebuilt through the existing invitation/acceptance operations.
func TestPartySingletonProbeMemberLeaveKeepsLeaderRowUntilLeaderLeavesThenReinvite(t *testing.T) {
	const (
		leaderID = uint16(1001)
		memberID = uint16(1002)
	)

	h := newPartyLeaveHarness(t)
	service := h.service
	manager := service.runtimePartyManagerForService()
	partyID := h.partyID

	handled, err := service.handleOnlinePartyCommand(h.member, uint16(dnfenum.CmdPacketLeaveParty), nil)
	if err != nil {
		t.Fatalf("member op13 through handleOnlinePartyCommand: %v", err)
	}
	if !handled {
		t.Fatal("member op13 was not consumed by handleOnlinePartyCommand")
	}

	if snapshot, found := manager.SnapshotByUser(memberID, 0); found {
		t.Fatalf("member is still present in the runtime-party manager after op13: %+v", snapshot)
	}
	leaderSnapshot, found := manager.SnapshotByUser(leaderID, 0)
	if !found || leaderSnapshot.ID != partyID || leaderSnapshot.Leader != leaderID || len(leaderSnapshot.Members) != 1 || leaderSnapshot.Members[0].UserID != leaderID {
		t.Fatalf("member op13 must retain exactly the leader singleton in the manager: found=%t party=%+v want id=%d leader=%d", found, leaderSnapshot, partyID, leaderID)
	}

	// The first member departure must not clear the leader's own scene record.
	// Reject any kind3 self record and require one parseable kind0 row for 1001.
	frames := splitUpperFrames(t, h.leaderConn.write.Bytes())
	var singletonFrame *upperFrame
	for index := range frames {
		frame := &frames[index]
		if frame.msgID != uint16(dnfenum.CmdPacketRecoverStamina) || len(frame.body) < 10 || binary.LittleEndian.Uint16(frame.body[4:6]) != currentSceneActorObjectKey(leaderID) {
			continue
		}
		switch frame.body[9] {
		case currentSceneOp9ActorRemoveKind:
			t.Fatalf("leader received op9 kind3 clearing its own retained singleton row after member op13: body=% X frames=%+v", frame.body, frames)
		case currentSceneOp9ActorDisplayKind:
			if singletonFrame == nil {
				singletonFrame = frame
			}
		}
	}
	if singletonFrame == nil {
		t.Fatalf("leader received no op9 kind0 singleton row after member op13: frames=%+v", frames)
	}
	hasIdentity := int(singletonFrame.body[7]) != currentClientSelectedChannelID(h.leader)
	rows, leaderSlot := parseOp9PartyFrame(t, singletonFrame.body, hasIdentity)
	if len(rows) != 1 || rows[0].slot != 0 || rows[0].uid != leaderID || leaderSlot != 0 {
		t.Fatalf("leader op9 singleton rows=%+v leader_slot=%d want slot0/uid%d/leader_slot0 body=% X", rows, leaderSlot, leaderID, singletonFrame.body)
	}

	leaderProjection := runtimePartyStateSnapshot(h.leader)
	leaderRows := runtimePartyMembers(leaderProjection)
	if leaderProjection.PartyID != int(partyID) || leaderProjection.UserID != leaderID || !leaderProjection.IsLeader || len(leaderRows) != 1 || leaderRows[0].UserID != leaderID {
		t.Fatalf("leader client projection must match the retained manager singleton: projection=%+v rows=%+v want id=%d", leaderProjection, leaderRows, partyID)
	}
	if memberProjection := runtimePartyStateSnapshot(h.member); memberProjection.PartyID != 0 {
		t.Fatalf("departing member retained a client projection: %+v", memberProjection)
	}

	// The retained leader must be able to issue a real final op13. This must
	// remove the manager's last member, clear both projections and emit kind3.
	h.leaderConn.write.Reset()
	h.memberConn.write.Reset()
	handled, err = service.handleOnlinePartyCommand(h.leader, uint16(dnfenum.CmdPacketLeaveParty), nil)
	if err != nil {
		t.Fatalf("leader op13 through handleOnlinePartyCommand: %v", err)
	}
	if !handled {
		t.Fatal("leader op13 was not consumed by handleOnlinePartyCommand")
	}
	for _, characterID := range []uint16{leaderID, memberID} {
		if snapshot, found := manager.SnapshotByUser(characterID, 0); found {
			t.Fatalf("character %d remained in the manager after the leader left: %+v", characterID, snapshot)
		}
	}
	if projection := runtimePartyStateSnapshot(h.leader); projection.PartyID != 0 {
		t.Fatalf("leader projection was not cleared by its own op13: %+v", projection)
	}
	if projection := runtimePartyStateSnapshot(h.member); projection.PartyID != 0 {
		t.Fatalf("former member projection changed after the leader left: %+v", projection)
	}
	leaderFrames := splitUpperFrames(t, h.leaderConn.write.Bytes())
	if len(leaderFrames) == 0 || leaderFrames[0].msgID != uint16(dnfenum.CmdPacketLeaveParty) || len(leaderFrames[0].body) != 1 || leaderFrames[0].body[0] != 1 {
		t.Fatalf("leader op13 did not begin with the success ACK: frames=%+v", leaderFrames)
	}
	if !assertOp9Kind3RemovalFor(leaderFrames, currentSceneActorObjectKey(leaderID)) {
		t.Fatalf("leader did not receive the final op9 kind3 self clear: frames=%+v", leaderFrames)
	}
	for _, frame := range leaderFrames {
		if frame.msgID == uint16(dnfenum.CmdPacketRecoverStamina) && len(frame.body) >= 10 &&
			binary.LittleEndian.Uint16(frame.body[4:6]) == currentSceneActorObjectKey(leaderID) &&
			frame.body[9] == currentSceneOp9ActorDisplayKind {
			t.Fatalf("leader retained an active op9 row after its final op13: body=% X frames=%+v", frame.body, leaderFrames)
		}
	}

	// Rebuild only through the same online handler and its existing request
	// decoders. Both characters are registered in the same town/area/channel
	// bucket so op10 is not silently rejected by the real presence gate.
	service.onlinePlayers = newOnlinePlayerManager()
	for _, session := range []*gameSession{h.leader, h.member} {
		service.onlinePlayers.EnterArea(&onlinePlayerInfo{
			CharacterID: session.selectedCharacterID,
			TownID:      38,
			AreaID:      1,
			Session:     session,
		})
	}
	h.leaderConn.write.Reset()
	h.memberConn.write.Reset()

	request := make([]byte, 13)
	binary.LittleEndian.PutUint16(request[:2], memberID)
	handled, err = service.handleOnlinePartyCommand(h.leader, uint16(dnfenum.CmdPacketRequestPeer), request)
	if err != nil || !handled {
		t.Fatalf("leader op10 re-invite: handled=%t err=%v", handled, err)
	}
	inviteFrames := splitUpperFrames(t, h.memberConn.write.Bytes())
	inviteForwarded := false
	for _, frame := range inviteFrames {
		if frame.msgID == 0x0007 {
			inviteForwarded = true
			break
		}
	}
	if !inviteForwarded {
		t.Fatalf("real op10 did not forward the native invitation prompt: frames=%+v", inviteFrames)
	}

	handled, err = service.handleOnlinePartyCommand(h.member, uint16(dnfenum.CmdPacketResponsePeer), buildResponsePeerBody(leaderID, 0, 0))
	if err != nil || !handled {
		t.Fatalf("member op11 re-invite acceptance: handled=%t err=%v", handled, err)
	}
	rejoinedLeader, leaderFound := manager.SnapshotByUser(leaderID, 0)
	rejoinedMember, memberFound := manager.SnapshotByUser(memberID, 0)
	if !leaderFound || !memberFound || rejoinedLeader.ID == 0 || rejoinedLeader.ID != rejoinedMember.ID ||
		rejoinedLeader.Leader != leaderID || len(rejoinedLeader.Members) != 2 || len(rejoinedMember.Members) != 2 {
		t.Fatalf("re-invite did not rebuild the original two-person party: leader_found=%t member_found=%t leader=%+v member=%+v", leaderFound, memberFound, rejoinedLeader, rejoinedMember)
	}
	for _, session := range []*gameSession{h.leader, h.member} {
		projection := runtimePartyStateSnapshot(session)
		rows := runtimePartyMembers(projection)
		if projection.PartyID != int(rejoinedLeader.ID) || projection.UserID != leaderID || len(rows) != 2 || rows[0].UserID != leaderID || rows[1].UserID != memberID {
			t.Fatalf("re-invited char %d projection=%+v rows=%+v want party=%d leader-first", session.selectedCharacterID, projection, rows, rejoinedLeader.ID)
		}
	}
}
