package dnfbridge

import (
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func addPartyLeaveAuxiliary(t *testing.T, service *Service, primary *gameSession) *gameSession {
	t.Helper()
	if service.onlinePlayers == nil {
		service.onlinePlayers = newOnlinePlayerManager()
	}
	if service.onlinePlayers.SessionForCharacter(primary.selectedCharacterID) == nil {
		service.onlinePlayers.EnterArea(&onlinePlayerInfo{
			CharacterID: primary.selectedCharacterID,
			TownID:      38,
			AreaID:      1,
			Session:     primary,
		})
	}
	auxiliary := &gameSession{
		conn:            &bufferConn{},
		connID:          fmt.Sprintf("party-leave-aux-%d", primary.selectedCharacterID),
		channel:         primary.channel,
		residentChannel: primary.residentChannel,
	}
	service.bindGameSessionCharacter(auxiliary, primary.selectedCharacterID)
	if owner, ok := service.onlineGameSession(primary.selectedCharacterID); !ok || owner != primary {
		t.Fatalf("auxiliary stole the character directory for %d", primary.selectedCharacterID)
	}
	service.registerLiveGameSession(auxiliary)
	t.Cleanup(func() { service.unregisterLiveGameSession(auxiliary) })
	return auxiliary
}

func assertPartyProjectionEmpty(t *testing.T, label string, session *gameSession) {
	t.Helper()
	state := runtimePartyStateSnapshot(session)
	if state.PartyID != 0 || len(runtimePartyMembers(state)) != 0 {
		t.Fatalf("%s retained stale party projection: id=%d leader=%d members=%d",
			label, state.PartyID, state.UserID, len(runtimePartyMembers(state)))
	}
}

func leavePartyThroughDispatch(t *testing.T, service *Service, session *gameSession) {
	t.Helper()
	handled, err := service.handleAlignedGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketLeaveParty),
		nil,
	)
	if err != nil {
		t.Fatalf("leave party: %v", err)
	}
	if !handled {
		t.Fatal("LeaveParty was not handled by the online party bridge")
	}
}

func TestPartyLeaveProjectionMemberLeaveClearsEveryTargetConnection(t *testing.T) {
	h := newPartyLeaveHarness(t)
	leaderAux := addPartyLeaveAuxiliary(t, h.service, h.leader)
	memberAux := addPartyLeaveAuxiliary(t, h.service, h.member)

	h.dispatchMemberLeave(t)

	manager := h.service.runtimePartyManagerForService()
	if snapshot, found := manager.SnapshotByUser(1002, 0); found {
		t.Fatalf("departed member remains in the manager: %+v", snapshot)
	}
	leaderParty, found := manager.SnapshotByUser(1001, 0)
	if !found || leaderParty.Leader != 1001 || len(leaderParty.Members) != 1 {
		t.Fatalf("remaining member did not retain a solo leader lobby: found=%t %+v", found, leaderParty)
	}

	for label, session := range map[string]*gameSession{
		"departed primary":   h.member,
		"departed auxiliary": memberAux,
		"survivor primary":   h.leader,
		"survivor auxiliary": leaderAux,
	} {
		assertPartyProjectionEmpty(t, label, session)
	}

	for label, session := range map[string]*gameSession{
		"departed primary accessor":   h.member,
		"departed auxiliary accessor": memberAux,
	} {
		state := h.service.runtimePartyStateForSession(session)
		if state.PartyID != 0 || isNonLeaderPartyMember(state, 1002) {
			t.Fatalf("%s resolved the removed party: id=%d leader=%d members=%d nonLeader=%t",
				label, state.PartyID, state.UserID, len(runtimePartyMembers(state)),
				isNonLeaderPartyMember(state, 1002))
		}
	}
	for label, session := range map[string]*gameSession{
		"survivor primary accessor":   h.leader,
		"survivor auxiliary accessor": leaderAux,
	} {
		state := h.service.runtimePartyStateForSession(session)
		if state.PartyID <= 0 || state.UserID != 1001 || !state.IsLeader ||
			len(runtimePartyMembers(state)) != 1 || isNonLeaderPartyMember(state, 1001) {
			t.Fatalf("%s lost the surviving leader lobby: %+v", label, state)
		}
	}
}

func TestPartyLeaveProjectionLeaderLeaveRebuildsTheRemainingLeader(t *testing.T) {
	h := newPartyLeaveHarness(t)
	leaderAux := addPartyLeaveAuxiliary(t, h.service, h.leader)
	memberAux := addPartyLeaveAuxiliary(t, h.service, h.member)

	leavePartyThroughDispatch(t, h.service, h.leader)

	manager := h.service.runtimePartyManagerForService()
	if snapshot, found := manager.SnapshotByUser(1001, 0); found {
		t.Fatalf("departed leader remains in the manager: %+v", snapshot)
	}
	memberParty, found := manager.SnapshotByUser(1002, 0)
	if !found || memberParty.Leader != 1002 || len(memberParty.Members) != 1 {
		t.Fatalf("remaining member did not become the rebuilt leader: found=%t %+v", found, memberParty)
	}
	if memberParty.ID == h.partyID {
		t.Fatalf("leader leave reused the retired party id %d", memberParty.ID)
	}

	assertPartyProjectionEmpty(t, "departed leader primary", h.leader)
	assertPartyProjectionEmpty(t, "departed leader auxiliary", leaderAux)
	assertPartyProjectionEmpty(t, "rebuilt leader primary cache", h.member)
	assertPartyProjectionEmpty(t, "rebuilt leader auxiliary cache", memberAux)

	for label, session := range map[string]*gameSession{
		"rebuilt leader primary accessor":   h.member,
		"rebuilt leader auxiliary accessor": memberAux,
	} {
		state := h.service.runtimePartyStateForSession(session)
		if state.PartyID != int(memberParty.ID) || state.UserID != 1002 || !state.IsLeader ||
			len(runtimePartyMembers(state)) != 1 || isNonLeaderPartyMember(state, 1002) {
			t.Fatalf("%s did not resolve the rebuilt leader: %+v", label, state)
		}
	}
}

func TestPartyLeaveProjectionKickClearsTargetAuxiliary(t *testing.T) {
	h := newPartyLeaveHarness(t)
	leaderAux := addPartyLeaveAuxiliary(t, h.service, h.leader)
	memberAux := addPartyLeaveAuxiliary(t, h.service, h.member)

	handled, err := h.service.handleAlignedGameCommand(
		h.leader,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketWalkoutPartyMember),
		[]byte{1},
	)
	if err != nil || !handled {
		t.Fatalf("kick member: handled=%t err=%v", handled, err)
	}

	manager := h.service.runtimePartyManagerForService()
	if snapshot, found := manager.SnapshotByUser(1002, 0); found {
		t.Fatalf("kicked member remains in the manager: %+v", snapshot)
	}
	leaderParty, found := manager.SnapshotByUser(1001, 0)
	if !found || leaderParty.Leader != 1001 || len(leaderParty.Members) != 1 {
		t.Fatalf("kicker did not retain a solo leader lobby: found=%t %+v", found, leaderParty)
	}
	assertPartyProjectionEmpty(t, "kicked primary", h.member)
	assertPartyProjectionEmpty(t, "kicked auxiliary", memberAux)
	assertPartyProjectionEmpty(t, "kicker primary cache", h.leader)
	assertPartyProjectionEmpty(t, "kicker auxiliary cache", leaderAux)

	if state := h.service.runtimePartyStateForSession(memberAux); state.PartyID != 0 ||
		isNonLeaderPartyMember(state, 1002) {
		t.Fatalf("kicked auxiliary still resolved the old party: %+v", state)
	}
	if state := h.service.runtimePartyStateForSession(leaderAux); state.PartyID <= 0 ||
		state.UserID != 1001 || !state.IsLeader {
		t.Fatalf("kicker auxiliary lost the surviving leader lobby: %+v", state)
	}
}

func TestPartyLeaveProjectionOldSessionCannotEraseARegroup(t *testing.T) {
	h := newPartyLeaveHarness(t)
	oldMember := h.member
	oldGeneration := oldMember.characterGeneration

	replacement := &gameSession{
		conn:                &bufferConn{},
		connID:              "party-leave-member-replacement",
		channel:             h.member.channel,
		residentChannel:     h.member.residentChannel,
		channelReconnect:    true,
		selectedCharacterID: 1002,
	}
	h.service.bindGameSessionCharacter(replacement, 1002)
	if owner, ok := h.service.onlineGameSession(1002); !ok || owner != replacement {
		t.Fatalf("replacement is not the character directory owner: found=%t owner=%p", ok, owner)
	}
	replacementAux := addPartyLeaveAuxiliary(t, h.service, replacement)

	leavePartyThroughDispatch(t, h.service, replacement)
	leavePartyThroughDispatch(t, h.service, h.leader)
	if snapshot, found := h.service.runtimePartyManagerForService().SnapshotByUser(1001, 0); found {
		t.Fatalf("fixture: old leader lobby survived the second leave: %+v", snapshot)
	}
	if _, _, ok := h.service.createOrJoinManagedRuntimeParty(replacement, h.leader); !ok {
		t.Fatal("regroup after leave failed")
	}
	manager := h.service.runtimePartyManagerForService()
	regrouped, found := manager.SnapshotByUser(1002, 0)
	if !found || regrouped.Leader != 1001 || len(regrouped.Members) != 2 ||
		regrouped.ID == h.partyID {
		t.Fatalf("regroup did not create a fresh party: found=%t %+v", found, regrouped)
	}

	// A buffered command from the retired socket must be consumed without
	// touching the replacement's new membership.
	handled, err := h.service.handleAlignedGameCommand(
		oldMember,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketLeaveParty),
		nil,
	)
	if err != nil || !handled {
		t.Fatalf("stale leave command: handled=%t err=%v", handled, err)
	}
	if current, found := manager.SnapshotByUser(1002, 0); !found ||
		current.ID != regrouped.ID || len(current.Members) != 2 {
		t.Fatalf("retired socket changed the regrouped party: found=%t %+v", found, current)
	}

	// Even delayed cleanup keyed to the current replacement generation must
	// preserve a party created after the previous leave.
	h.service.clearRuntimePartyProjection(1002, replacement.characterGeneration)
	if current, found := manager.SnapshotByUser(1002, 0); !found ||
		current.ID != regrouped.ID || len(current.Members) != 2 {
		t.Fatalf("delayed cleanup changed the regrouped manager state: found=%t %+v", found, current)
	}
	for label, session := range map[string]*gameSession{
		"replacement":           replacement,
		"replacement auxiliary": replacementAux,
	} {
		state := h.service.runtimePartyStateForSession(session)
		if state.PartyID != int(regrouped.ID) || state.UserID != 1001 ||
			len(runtimePartyMembers(state)) != 2 {
			t.Fatalf("%s lost the regrouped party after delayed cleanup: %+v", label, state)
		}
	}
	if oldGeneration == replacement.characterGeneration {
		t.Fatal("fixture: replacement did not receive a fresh generation")
	}
}

func TestPartyLeaveProjectionTwoMemberDisbandClearsAuxiliarySessions(t *testing.T) {
	fixture := newLeaderExitFixture(t)
	leaderAux := addPartyLeaveAuxiliary(t, fixture.service, fixture.leader)
	memberAux := addPartyLeaveAuxiliary(t, fixture.service, fixture.member)

	fixture.service.disbandRuntimePartyOnDungeonLeaderExit(
		fixture.leader,
		fixture.instance,
		99,
		"party_leave_projection_test",
	)

	manager := fixture.service.runtimePartyManagerForService()
	for _, characterID := range []uint16{99, 100} {
		if snapshot, found := manager.SnapshotByUser(characterID, 0); found {
			t.Fatalf("char %d remains in a disbanded party: %+v", characterID, snapshot)
		}
	}
	for label, session := range map[string]*gameSession{
		"leader primary":   fixture.leader,
		"leader auxiliary": leaderAux,
		"member primary":   fixture.member,
		"member auxiliary": memberAux,
	} {
		assertPartyProjectionEmpty(t, label, session)
		if state := fixture.service.runtimePartyStateForSession(session); state.PartyID != 0 {
			t.Fatalf("%s resolved a disbanded party after cleanup: %+v", label, state)
		}
	}
}

func TestPartyLeaveProjectionConcurrentLookupAndInvalidation(t *testing.T) {
	h := newPartyLeaveHarness(t)
	memberAux := addPartyLeaveAuxiliary(t, h.service, h.member)
	start := make(chan struct{})
	done := make(chan struct{})
	lookupsDone := make(chan struct{})

	go func() {
		defer close(lookupsDone)
		<-start
		for {
			_ = h.service.runtimePartyStateForSession(memberAux)
			select {
			case <-done:
				return
			default:
			}
		}
	}()

	close(start)
	h.dispatchMemberLeave(t)
	close(done)
	<-lookupsDone

	if state := h.service.runtimePartyStateForSession(memberAux); state.PartyID != 0 ||
		isNonLeaderPartyMember(state, 1002) {
		t.Fatalf("concurrent lookup restored the departed party: %+v", state)
	}
	assertPartyProjectionEmpty(t, "departed auxiliary", memberAux)
	assertPartyProjectionEmpty(t, "departed primary", h.member)
}
