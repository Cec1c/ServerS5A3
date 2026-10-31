package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type p33RaidExitFailConn struct {
	bufferConn
	err    error
	closed bool
}

func (connection *p33RaidExitFailConn) Close() error {
	connection.closed = true
	return nil
}

func (connection *p33RaidExitFailConn) Write(data []byte) (int, error) {
	if connection.err != nil {
		return 0, connection.err
	}
	return connection.bufferConn.Write(data)
}

func p33RaidParty(t *testing.T, service *Service, sessions ...*gameSession) (uint16, alignedcmd.PartyState) {
	t.Helper()
	if len(sessions) < 2 {
		t.Fatal("P33 party needs at least two sessions")
	}
	members := make([]party.RuntimePartyMember, 0, len(sessions))
	for _, session := range sessions {
		identity, ok := service.boundGameSessionCharacterSnapshot(session)
		if !ok {
			t.Fatalf("P33 session %d is not bound", session.selectedCharacterID)
		}
		members = append(members, party.RuntimePartyMember{UserID: identity.character, SessionGeneration: identity.generation})
	}
	manager := service.runtimePartyManagerForService()
	created := manager.Create(members[0], alignedcmd.PartyState{NameBytes: []byte("p33"), MaxMembers: 4, UserState: 1})
	if !created.OK {
		t.Fatalf("create P33 party: %s", created.Reason)
	}
	joined := created
	for _, member := range members[1:] {
		joined = manager.Join(created.Party.ID, member)
		if !joined.OK {
			t.Fatalf("join P33 party %d: %s", member.UserID, joined.Reason)
		}
	}
	return created.Party.ID, service.publishRuntimePartySnapshot(joined.Party)
}

func p33RaidBattleWithFrozenFlow(t *testing.T, service *Service, raidKey uint32, leader, member *gameSession, extraMembers ...*gameSession) (runtimeRaidState, *currentAntonRewardFlow, *currentAntonRewardFlow, uint64) {
	t.Helper()
	leaderID, memberID := leader.selectedCharacterID, member.selectedCharacterID
	accounts := map[uint16]string{}
	groups := map[uint16]byte{}
	for _, session := range append([]*gameSession{leader, member}, extraMembers...) {
		accounts[session.selectedCharacterID] = service.accountIDForSession(session)
		groups[session.selectedCharacterID] = 1
	}
	leaderAccount, memberAccount := accounts[leaderID], accounts[memberID]
	instance := "p33aabbccddeeff001122334455667788"
	flow := &currentAntonRewardFlow{
		InstanceID: instance,
		RaidKey:    raidKey,
		Phase:      0,
		Mode:       1,
		Recipients: []currentAntonRewardRecipient{
			{CharacterID: leaderID, AccountID: leaderAccount, Group: 1, Clears: 8},
			{CharacterID: memberID, AccountID: memberAccount, Group: 1, Clears: 8},
		},
		Party: map[uint16]*currentAntonRewardCard{},
		Squad: map[uint16]*currentAntonRewardCard{},
	}
	service.raidMu.Lock()
	live := service.raids[raidKey]
	if live == nil || runtimeRaidMemberIndex(*live, leaderID) < 0 || runtimeRaidMemberIndex(*live, memberID) < 0 {
		service.raidMu.Unlock()
		t.Fatal("P33 raid membership fixture is incomplete")
	}
	live.Started = true
	live.Battle = &runtimeRaidBattle{
		InstanceID:     instance,
		Mode:           1,
		Phase:          0,
		ChannelID:      leader.channel.ID,
		ChannelType:    byte(leader.channel.Type),
		MemberAccounts: accounts,
		MemberGroups:   groups,
		Participation: map[uint16]currentRaidParticipation{
			leaderID: {RunID: "p33-accepted-run", DungeonID: 210, Phase: 0},
			memberID: {RunID: "p33-accepted-run", DungeonID: 210, Phase: 0},
		},
		RewardFlow: [2]*currentAntonRewardFlow{},
	}
	live.Battle.RewardFlow[0] = flow
	if service.antonRewardFlows == nil {
		service.antonRewardFlows = map[string]*currentAntonRewardFlow{}
	}
	service.antonRewardFlows[currentAntonFlowKey(instance, 0)] = flow
	version := live.Battle.ProgressVersion
	state := cloneRuntimeRaidState(*live)
	service.raidMu.Unlock()
	if err := service.sendCurrentRaidProgressProjection(leader, state, true, false); err != nil {
		t.Fatal(err)
	}
	frozen := cloneCurrentAntonRewardFlow(flow)
	return state, flow, frozen, version
}

func p33WaitRaidProjectionVersion(t *testing.T, session *gameSession, version uint64) {
	t.Helper()
	deadline := time.Now().Add(createWriteTimeout)
	for {
		session.raidProjection.mu.Lock()
		ready := session.raidProjection.version >= version && !session.raidProjection.failed
		session.raidProjection.mu.Unlock()
		if ready {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("P33 progress projection did not reach version %d", version)
		}
		time.Sleep(time.Millisecond)
	}
}

func p33HasParticipationRemoval(t *testing.T, data []byte, characterID uint16) bool {
	t.Helper()
	for len(data) > 0 {
		packet, rest := splitGameServerUpperPacket(t, data)
		data = rest
		if packet.Header.MsgID != 584 || len(packet.Body) < 7 || packet.Body[5] != 0 {
			continue
		}
		count := int(packet.Body[6])
		if len(packet.Body) != 7+count*2 {
			t.Fatalf("P33 malformed 584 removal body: %x", packet.Body)
		}
		for i := 0; i < count; i++ {
			if binary.LittleEndian.Uint16(packet.Body[7+i*2:]) == characterID {
				return true
			}
		}
	}
	return false
}

func p33HasMessageID(t *testing.T, data []byte, messageID uint16) bool {
	t.Helper()
	for len(data) > 0 {
		packet, rest := splitGameServerUpperPacket(t, data)
		data = rest
		if packet.Header.MsgID == messageID {
			return true
		}
	}
	return false
}

func p33HasMessageBody(t *testing.T, data []byte, messageID uint16, body []byte) bool {
	t.Helper()
	for len(data) > 0 {
		packet, rest := splitGameServerUpperPacket(t, data)
		data = rest
		if packet.Header.MsgID == messageID && bytes.Equal(packet.Body, body) {
			return true
		}
	}
	return false
}

func p33HasPartyRemovalSequence(t *testing.T, data []byte, want byte) bool {
	t.Helper()
	sawPartyFrame := false
	for len(data) > 0 {
		packet, rest := splitGameServerUpperPacket(t, data)
		data = rest
		if sawPartyFrame && packet.Header.MsgID == 0x0099 && len(packet.Body) > 0 && packet.Body[0] == want {
			return true
		}
		sawPartyFrame = packet.Header.MsgID == 9
		if packet.Header.MsgID == 0x0099 && len(packet.Body) > 0 && packet.Body[0] == want {
			// A realtime-only frame is not enough: the committed removal must
			// carry the party-frame projection before it.
			continue
		}
	}
	return false
}

func TestP33Op665SyncsMatchingPartyContinuesAfterNotifyFailureAndPreservesReward(t *testing.T) {
	s, leader, member, leaderConn, memberConn, raid := prepareApprovedCurrentRaid(t)
	failure := errors.New("p33 injected survivor write failure")
	repository, ok := s.repositoryGroup()
	if !ok {
		t.Fatal("P33 repository unavailable")
	}
	if err := repository.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: "21", AccountID: "dnf:3", Name: "P33 survivor", Job: "0", Level: 90}); err != nil {
		t.Fatal(err)
	}
	failedConn := &p33RaidExitFailConn{err: failure}
	failingPartyLeader := &gameSession{
		conn: failedConn, connID: "p33-failing-party-leader",
		channel: leader.channel, accountID: "dnf:3", selectedCharacterID: 21,
		loginPacketSeen: true, accountFromToken: true,
	}
	s.bindGameSessionCharacter(failingPartyLeader, 21)
	s.raidMu.Lock()
	live := s.raids[raid.RaidKey]
	if live == nil {
		s.raidMu.Unlock()
		t.Fatal("P33 raid fixture missing")
	}
	live.Members = append([]runtimeRaidMemberState{{CharID: 21, GroupIndex: 1, SlotOrder: 2, UserState: 1, HPPercent: 100, MPPercent: 100}}, live.Members...)
	s.raidMu.Unlock()
	partyID, _ := p33RaidParty(t, s, failingPartyLeader, leader, member)
	_, flow, frozen, beforeVersion := p33RaidBattleWithFrozenFlow(t, s, raid.RaidKey, leader, member, failingPartyLeader)
	memberIdentity, _ := s.boundGameSessionCharacterSnapshot(member)
	leaderConn.write.Reset()
	memberConn.write.Reset()

	if err := s.handleGameCommand(member, 1, 665, []byte{byte(member.selectedCharacterID), 0}); err != nil {
		t.Fatalf("op665 propagated another recipient's failure: %v", err)
	}
	if !failedConn.closed {
		t.Fatal("failed recipient remained open after notification failure")
	}
	ack, _ := splitGameServerUpperPacket(t, memberConn.write.Bytes())
	if ack.Header.MsgID != 665 || len(ack.Body) != 2 || ack.Body[0] != 1 {
		t.Fatalf("op665 ACK was not emitted after commit: msg=%d body=%x", ack.Header.MsgID, ack.Body)
	}
	if _, found := s.runtimePartyManagerForService().SnapshotByUser(memberIdentity.character, memberIdentity.generation); found {
		t.Fatal("op665 left the matching session inside the central party")
	}
	removedState := runtimePartyStateSnapshot(member)
	if removedState.PartyID != 0 || len(runtimePartyMembers(removedState)) != 0 {
		t.Fatalf("departed raid member retained own party projection: %+v", removedState)
	}
	remaining := runtimePartyStateSnapshot(leader)
	if remaining.PartyID != int(partyID) || len(runtimePartyMembers(remaining)) != 2 {
		t.Fatalf("surviving party projection=%+v, want party %d with two members", remaining, partyID)
	}
	for _, row := range runtimePartyMembers(remaining) {
		if row.UserID == member.selectedCharacterID {
			t.Fatal("departed raid member leaked into surviving central party projection")
		}
	}
	if !p33HasPartyRemovalSequence(t, leaderConn.write.Bytes(), 2) {
		t.Fatal("surviving party projection did not publish the two remaining members")
	}

	s.raidMu.Lock()
	live = s.raids[raid.RaidKey]
	if live == nil || live.Battle == nil {
		s.raidMu.Unlock()
		t.Fatal("P33 raid disappeared after member leave")
	}
	afterVersion := live.Battle.ProgressVersion
	s.raidMu.Unlock()
	if afterVersion != beforeVersion+1 {
		t.Fatalf("op665 ProgressVersion=%d, want %d", afterVersion, beforeVersion+1)
	}
	p33WaitRaidProjectionVersion(t, leader, afterVersion)
	s.raidMu.Lock()
	liveFlow := live.Battle.RewardFlow[0]
	registeredFlow := s.antonRewardFlows[currentAntonFlowKey(flow.InstanceID, 0)]
	s.raidMu.Unlock()
	if liveFlow != flow || registeredFlow != flow || !reflect.DeepEqual(flow, frozen) {
		t.Fatal("op665 rewrote the frozen reward recipients or display plans")
	}
	if !p33HasMessageID(t, leaderConn.write.Bytes(), 591) {
		t.Fatal("remaining raid roster was not published after a prior survivor write failure")
	}
	if !p33HasParticipationRemoval(t, leaderConn.write.Bytes(), member.selectedCharacterID) {
		t.Fatal("op665 did not publish a 584 op0 removal for the departed member")
	}
}

func TestP33Op665DoesNotRetireMismatchedNewParty(t *testing.T) {
	s, leader, member, _, memberConn, raid := prepareApprovedCurrentRaid(t)
	oldPartyID, _ := p33RaidParty(t, s, leader, member)
	if runtimePartyStateSnapshot(member).PartyID != int(oldPartyID) {
		t.Fatal("fixture did not cache the old party projection")
	}
	manager := s.runtimePartyManagerForService()
	leaderIdentity, _ := s.boundGameSessionCharacterSnapshot(leader)
	memberIdentity, _ := s.boundGameSessionCharacterSnapshot(member)
	created := manager.Create(party.RuntimePartyMember{UserID: memberIdentity.character, SessionGeneration: memberIdentity.generation}, alignedcmd.PartyState{MaxMembers: 4})
	joined := manager.Join(created.Party.ID, party.RuntimePartyMember{UserID: leaderIdentity.character, SessionGeneration: leaderIdentity.generation})
	if !created.OK || !joined.OK || created.Party.ID == oldPartyID {
		t.Fatal("fixture did not create a distinct replacement party")
	}
	memberConn.write.Reset()
	if err := s.handleGameCommand(member, 1, 665, []byte{byte(member.selectedCharacterID), 0}); err != nil {
		t.Fatal(err)
	}
	current, found := manager.SnapshotByUser(memberIdentity.character, memberIdentity.generation)
	if !found || current.ID != created.Party.ID || len(current.Members) != 2 {
		t.Fatalf("op665 removed a mismatched newer party: %+v found=%t", current, found)
	}
	if state := runtimePartyStateSnapshot(member); state.PartyID != int(oldPartyID) {
		t.Fatalf("op665 rewrote the stale local projection instead of leaving the current team intact: %+v", state)
	}
	ack, rest := splitGameServerUpperPacket(t, memberConn.write.Bytes())
	if ack.Header.MsgID != 665 || len(ack.Body) == 0 || ack.Body[0] != 1 || len(rest) != 0 {
		t.Fatalf("op665 ACK/remainder=%d/%x/%x", ack.Header.MsgID, ack.Body, rest)
	}
	if _, found := s.runtimeRaidForMember(member.selectedCharacterID); found {
		t.Fatal("P33 stale-party guard prevented the committed raid leave")
	}
	if raid.RaidKey == 0 {
		t.Fatal("P33 raid fixture missing key")
	}
}

func TestP33Op665DoesNotRetirePartyForStaleSessionGeneration(t *testing.T) {
	s, leader, member, _, memberConn, raid := prepareApprovedCurrentRaid(t)
	partyID, _ := p33RaidParty(t, s, leader, member)
	memberIdentity, _ := s.boundGameSessionCharacterSnapshot(member)
	oldGeneration := memberIdentity.generation
	if next := advanceGameSessionCharacterGeneration(member); next == 0 || next == oldGeneration {
		t.Fatal("fixture did not advance the session generation")
	}
	memberConn.write.Reset()
	if err := s.handleGameCommand(member, 1, 665, []byte{byte(member.selectedCharacterID), 0}); err != nil {
		t.Fatal(err)
	}
	manager := s.runtimePartyManagerForService()
	current, found := manager.SnapshotByUser(member.selectedCharacterID, oldGeneration)
	if !found || current.ID != partyID || len(current.Members) != 2 {
		t.Fatalf("op665 retired the party under a stale generation: %+v found=%t", current, found)
	}
	if state := runtimePartyStateSnapshot(member); state.PartyID != int(partyID) {
		t.Fatalf("op665 cleared the stale-generation party projection: %+v", state)
	}
	ack, rest := splitGameServerUpperPacket(t, memberConn.write.Bytes())
	if ack.Header.MsgID != 665 || len(ack.Body) == 0 || ack.Body[0] != 1 || len(rest) != 0 {
		t.Fatalf("op665 ACK/remainder=%d/%x/%x", ack.Header.MsgID, ack.Body, rest)
	}
	if _, found := s.runtimeRaidForMember(member.selectedCharacterID); found {
		t.Fatal("stale-generation party guard prevented the committed raid leave")
	}
	if raid.RaidKey == 0 {
		t.Fatal("P33 raid fixture missing key")
	}
}

func TestP33Op665SingletonSurvivorProjectsSoloRoster(t *testing.T) {
	s, leader, member, leaderConn, _, raid := prepareApprovedCurrentRaid(t)
	partyID, _ := p33RaidParty(t, s, leader, member)
	_, _, _, beforeVersion := p33RaidBattleWithFrozenFlow(t, s, raid.RaidKey, leader, member)
	leaderConn.write.Reset()
	if err := s.handleGameCommand(member, 1, 665, []byte{byte(member.selectedCharacterID), 0}); err != nil {
		t.Fatal(err)
	}
	leaderIdentity, _ := s.boundGameSessionCharacterSnapshot(leader)
	assertPartyProjectionSingleton(t, "raid survivor", runtimePartyStateSnapshot(leader), int(partyID), leaderIdentity.character, 0, true)
	managerParty, found := s.runtimePartyManagerForService().SnapshotByUser(leaderIdentity.character, leaderIdentity.generation)
	if !found || managerParty.ID != partyID || len(managerParty.Members) != 1 {
		t.Fatalf("singleton manager lobby=%+v found=%t", managerParty, found)
	}
	state, found := s.runtimeRaidForMember(leaderIdentity.character)
	if !found || len(state.Members) != 1 || !state.Members[0].OrdinaryPartySolo || state.Battle == nil || state.Battle.ProgressVersion != beforeVersion+1 {
		t.Fatalf("surviving solo raid projection=%+v found=%t", state, found)
	}
	projection, err := s.buildRuntimeRaidRosterBody(state)
	if err != nil {
		t.Fatal(err)
	}
	p33WaitRaidProjectionVersion(t, leader, state.Battle.ProgressVersion)
	if !p33HasMessageBody(t, leaderConn.write.Bytes(), 591, projection) {
		t.Fatal("op665 survivor roster did not carry the committed solo group marker")
	}
}

func TestP33Op665PartyLeaderRetirementClearsOnlyOldGeneration(t *testing.T) {
	s, leader, member, leaderConn, memberConn, raid := prepareApprovedCurrentRaid(t)
	oldPartyID, _ := p33RaidParty(t, s, leader, member)
	state, flow, frozen, beforeVersion := p33RaidBattleWithFrozenFlow(t, s, raid.RaidKey, leader, member)
	if err := s.sendCurrentRaidProgressProjection(member, state, true, false); err != nil {
		t.Fatal(err)
	}
	leaderConn.write.Reset()
	memberConn.write.Reset()
	if err := s.handleGameCommand(leader, 1, 665, []byte{byte(leader.selectedCharacterID), 0}); err != nil {
		t.Fatal(err)
	}
	memberIdentity, _ := s.boundGameSessionCharacterSnapshot(member)
	leaderIdentity, _ := s.boundGameSessionCharacterSnapshot(leader)
	manager := s.runtimePartyManagerForService()
	if _, found := manager.SnapshotByUser(leaderIdentity.character, leaderIdentity.generation); found {
		t.Fatal("departed party leader retained manager membership")
	}
	managerParty, found := manager.SnapshotByUser(memberIdentity.character, memberIdentity.generation)
	if !found || managerParty.ID == oldPartyID || len(managerParty.Members) != 1 || managerParty.Members[0].UserID != memberIdentity.character {
		t.Fatalf("party leader retirement did not create a survivor generation: %+v found=%t", managerParty, found)
	}
	assertPartyProjectionSingleton(t, "raid successor", runtimePartyStateSnapshot(member), int(managerParty.ID), memberIdentity.character, 1, true)
	sawReplacementSnapshot := false
	for data := memberConn.write.Bytes(); len(data) > 0; {
		packet, rest := splitGameServerUpperPacket(t, data)
		if packet.Header.Classification == 0 && packet.Header.MsgID == 11 {
			assertRuntimeSuccessorSingletonSnapshot(t, data, member, true, 1)
			sawReplacementSnapshot = true
			break
		}
		data = rest
	}
	if !sawReplacementSnapshot {
		t.Fatal("party leader retirement did not publish the surviving client's new singleton")
	}
	s.raidMu.Lock()
	afterVersion := s.raids[raid.RaidKey].Battle.ProgressVersion
	registeredFlow := s.antonRewardFlows[currentAntonFlowKey(flow.InstanceID, 0)]
	s.raidMu.Unlock()
	if afterVersion != beforeVersion+1 || registeredFlow != flow || !reflect.DeepEqual(flow, frozen) {
		t.Fatal("party leader retirement changed the reward revision or frozen recipients")
	}
	p33WaitRaidProjectionVersion(t, member, afterVersion)
	if !p33HasParticipationRemoval(t, memberConn.write.Bytes(), leaderIdentity.character) {
		t.Fatal("party leader retirement did not publish a 584 op0 removal")
	}
}

func TestP33DisconnectExpiryQueues584RemovalAndPreservesReward(t *testing.T) {
	s, leader, member, leaderConn, _, raid := prepareApprovedCurrentRaid(t)
	_, flow, frozen, beforeVersion := p33RaidBattleWithFrozenFlow(t, s, raid.RaidKey, leader, member)
	memberIdentity, _ := s.boundGameSessionCharacterSnapshot(member)
	now := time.Now()
	s.raidMu.Lock()
	live := s.raids[raid.RaidKey]
	if live == nil || live.Battle == nil {
		s.raidMu.Unlock()
		t.Fatal("P33 disconnect fixture lost raid battle")
	}
	idx := runtimeRaidMemberIndex(*live, memberIdentity.character)
	if idx < 0 {
		s.raidMu.Unlock()
		t.Fatal("P33 disconnect member missing from raid")
	}
	live.Members[idx].UserState = 0
	if s.currentRaidDisconnects == nil {
		s.currentRaidDisconnects = map[uint16]currentRaidDisconnectSeat{}
	}
	s.currentRaidDisconnects[memberIdentity.character] = currentRaidDisconnectSeat{
		Raid: live, RaidKey: raid.RaidKey, CharacterID: memberIdentity.character,
		Session: member, Generation: memberIdentity.generation, AccountID: s.accountIDForSession(member),
		InstanceID: live.Battle.InstanceID, DisconnectedAt: now.Add(-2 * time.Second), ExpiresAt: now.Add(-time.Second),
	}
	s.raidMu.Unlock()
	leaderConn.write.Reset()
	s.expireCurrentRaidDisconnectSeats(now)
	if _, found := s.runtimeRaidForMember(memberIdentity.character); found {
		t.Fatal("expired disconnect seat remained in the raid")
	}
	s.raidMu.Lock()
	afterVersion := live.Battle.ProgressVersion
	s.raidMu.Unlock()
	if afterVersion != beforeVersion+1 {
		t.Fatalf("disconnect expiry ProgressVersion=%d, want %d", afterVersion, beforeVersion+1)
	}
	p33WaitRaidProjectionVersion(t, leader, afterVersion)
	s.raidMu.Lock()
	liveFlow := live.Battle.RewardFlow[0]
	registeredFlow := s.antonRewardFlows[currentAntonFlowKey(flow.InstanceID, 0)]
	s.raidMu.Unlock()
	if liveFlow != flow || registeredFlow != flow || !reflect.DeepEqual(flow, frozen) {
		t.Fatal("disconnect expiry rewrote the frozen reward recipients or display plans")
	}
	if !p33HasParticipationRemoval(t, leaderConn.write.Bytes(), memberIdentity.character) {
		t.Fatal("disconnect expiry did not publish a 584 op0 removal")
	}
}

func TestP33CommittedExitDoesNotOverwriteSurvivorReplacement(t *testing.T) {
	for _, replaceParty := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-session", true: "new-party"}[replaceParty], func(t *testing.T) {
			s, leader, member, leaderConn, _, _ := prepareApprovedCurrentRaid(t)
			p33RaidParty(t, s, leader, member)
			projection, err := s.leaveRuntimePartyForRaidExit(member)
			if err != nil || !projection.changed {
				t.Fatalf("departure: %+v %v", projection, err)
			}
			identity, _ := s.boundGameSessionCharacterSnapshot(leader)
			if replaceParty {
				created := s.runtimePartyManagerForService().Create(party.RuntimePartyMember{UserID: identity.character, SessionGeneration: identity.generation}, alignedcmd.PartyState{MaxMembers: 4})
				s.publishRuntimePartySnapshot(created.Party)
			} else {
				advanceGameSessionCharacterGeneration(leader)
			}
			before := runtimePartyStateSnapshot(leader)
			leaderConn.write.Reset()
			if err := s.broadcastRuntimePartyRaidExit(projection, member.selectedCharacterID, member); err != nil {
				t.Fatal(err)
			}
			if leaderConn.write.Len() != 0 || !reflect.DeepEqual(before, runtimePartyStateSnapshot(leader)) {
				t.Fatal("old departure overwrote or notified a replaced survivor")
			}
		})
	}
}
