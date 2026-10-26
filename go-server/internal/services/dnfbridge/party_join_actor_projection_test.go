package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func countUpperMsgIDForTest(t *testing.T, buf *bufferConn, msgID uint16) int {
	t.Helper()
	count := 0
	for _, packet := range splitAllGameServerUpperPackets(t, buf.write.Bytes()) {
		if packet.Header.MsgID == msgID {
			count++
		}
	}
	return count
}

func TestPartyInviteAcceptanceProjectsTownPeerActorsInBothDirections(t *testing.T) {
	fixture := newTownResendFixture(t)
	fixture.service.bindGameSessionCharacter(fixture.local, 29)
	fixture.service.bindGameSessionCharacter(fixture.peer, 17)
	if ok, reason, _ := fixture.service.forceInviteParty(29, 17, 0, [3]uint32{}); !ok {
		t.Fatalf("register real invite: %s", reason)
	}
	// The invite is registered while co-present, then the invitee crosses to a
	// different town area before accepting, matching the report's party-only
	// roster without a scene actor.
	fixture.service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 17, AccountID: "dnf:2", Name: "peer",
		TownID: 38, AreaID: 1, PositionX: 420, PositionY: 210,
		Direction: 5, AreaState: 3, Session: fixture.peer,
	})
	fixture.localBuf.write.Reset()
	fixture.peerBuf.write.Reset()

	if err := fixture.service.handleOnlineResponsePeer(
		fixture.peer,
		uint16(dnfenum.CmdPacketResponsePeer),
		buildResponsePeerBody(29, 0, 0),
	); err != nil {
		t.Fatal(err)
	}

	if got := runtimePartyStateSnapshot(fixture.peer); got.PartyID <= 0 || len(runtimePartyMembers(got)) != 2 {
		t.Fatalf("invite acceptance did not form the party: %+v", got)
	}
	actorMsgID := uint16(dnfenum.CmdPacketSetUDPIPPort)
	for name, buf := range map[string]*bufferConn{"inviter": fixture.localBuf, "invitee": fixture.peerBuf} {
		if got := countUpperMsgIDForTest(t, buf, actorMsgID); got != 2 {
			t.Fatalf("%s actor mode0/mode1 count=%d, want 2", name, got)
		}
	}
}

func TestPartyInviteAcceptanceSameAreaKeepsActorGuard(t *testing.T) {
	fixture := newTownResendFixture(t)
	fixture.service.bindGameSessionCharacter(fixture.local, 29)
	fixture.service.bindGameSessionCharacter(fixture.peer, 17)
	if ok, reason, _ := fixture.service.forceInviteParty(29, 17, 0, [3]uint32{}); !ok {
		t.Fatalf("register real invite: %s", reason)
	}
	fixture.localBuf.write.Reset()
	fixture.peerBuf.write.Reset()
	if err := fixture.service.handleOnlineResponsePeer(
		fixture.peer,
		uint16(dnfenum.CmdPacketResponsePeer),
		buildResponsePeerBody(29, 0, 0),
	); err != nil {
		t.Fatal(err)
	}
	actorMsgID := uint16(dnfenum.CmdPacketSetUDPIPPort)
	if got := countUpperMsgIDForTest(t, fixture.localBuf, actorMsgID); got != 0 {
		t.Fatalf("same-area acceptance duplicated inviter actor rows: %d", got)
	}
	if got := countUpperMsgIDForTest(t, fixture.peerBuf, actorMsgID); got != 0 {
		t.Fatalf("same-area acceptance duplicated invitee actor rows: %d", got)
	}
}

func installPartyRaidStateForTest(t *testing.T, fixture *inviteAcceptDungeonFixture, route byte, extra []runtimeRaidMemberState) *runtimeRaidState {
	t.Helper()
	fixture.service.options.currentClientVersion = "official-24.9"
	key := uint32(23<<16 | 99)
	state := &runtimeRaidState{
		RaidKey: key, LeaderID: 99, RouteOrRaidType: route, CurrentLayout: true,
		Members: []runtimeRaidMemberState{
			{CharID: 99, GroupIndex: 1, SlotOrder: 0, UserState: 1, HPPercent: 100, MPPercent: 100},
			{CharID: 100, GroupIndex: 2, SlotOrder: 0, UserState: 1, HPPercent: 100, MPPercent: 100},
		},
	}
	state.Members = append(state.Members, extra...)
	fixture.service.raids = map[uint32]*runtimeRaidState{key: state}
	return state
}

func TestPartyAcceptSynchronizesDifferentRaidGroupsIntoAnchorGroup(t *testing.T) {
	fixture := newInviteAcceptDungeonFixture(t)
	state := installPartyRaidStateForTest(t, fixture, 0, nil)
	fixture.formPartyThroughClientAccept(t)
	anchor, _ := runtimeRaidMemberForTest(*state, 99)
	member, _ := runtimeRaidMemberForTest(*state, 100)
	if anchor.GroupIndex != 1 || member.GroupIndex != 1 {
		t.Fatalf("ordinary party did not synchronize raid groups: anchor=%+v member=%+v", anchor, member)
	}
	if anchor.SlotOrder == member.SlotOrder {
		t.Fatalf("synchronized members share slot %d", anchor.SlotOrder)
	}
}

func TestPartyAcceptDoesNotOverfillFullRaidGroup(t *testing.T) {
	fixture := newInviteAcceptDungeonFixture(t)
	state := installPartyRaidStateForTest(t, fixture, 0, []runtimeRaidMemberState{
		{CharID: 200, GroupIndex: 1, SlotOrder: 1},
		{CharID: 201, GroupIndex: 1, SlotOrder: 2},
		{CharID: 202, GroupIndex: 1, SlotOrder: 3},
	})
	fixture.formPartyThroughClientAccept(t)
	member, _ := runtimeRaidMemberForTest(*state, 100)
	if member.GroupIndex != 2 || member.SlotOrder != 0 {
		t.Fatalf("full group was overfilled or joiner was moved partially: %+v", member)
	}
	count := 0
	for _, row := range state.Members {
		if row.GroupIndex == 1 {
			count++
		}
	}
	if count != 4 {
		t.Fatalf("group 1 count=%d, want 4", count)
	}
}

func TestPartyAcceptDoesNotMergeSingleMemberFiendWarGroups(t *testing.T) {
	fixture := newInviteAcceptDungeonFixture(t)
	state := installPartyRaidStateForTest(t, fixture, 2, nil)
	fixture.formPartyThroughClientAccept(t)
	member, _ := runtimeRaidMemberForTest(*state, 100)
	if member.GroupIndex != 2 {
		t.Fatalf("FiendWar single-member group was merged: %+v", member)
	}
}

func TestPartyAcceptSynchronizesStartedRaidGroupAndBattleProjection(t *testing.T) {
	fixture := newInviteAcceptDungeonFixture(t)
	state := installPartyRaidStateForTest(t, fixture, 0, nil)
	state.Started = true
	state.Battle = &runtimeRaidBattle{
		InstanceID: "party-sync-started", Mode: 1, Phase: 1,
		MemberAccounts: map[uint16]string{99: "account-1", 100: "account-1"},
		MemberGroups:   map[uint16]byte{99: 1, 100: 2},
	}
	fixture.formPartyThroughClientAccept(t)
	member, _ := runtimeRaidMemberForTest(*state, 100)
	if member.GroupIndex != 1 {
		t.Fatalf("started party member stayed in old raid group: %+v", member)
	}
	if state.Battle.MemberGroups[100] != 2 {
		t.Fatalf("ordinary party changed reward-frozen battle groups: %+v", state.Battle.MemberGroups)
	}
}

func TestPartyAcceptMaterializesRawZeroMemberInAnchorGroup(t *testing.T) {
	fixture := newInviteAcceptDungeonFixture(t)
	state := installPartyRaidStateForTest(t, fixture, 0, nil)
	state.Members[1].GroupIndex, state.Members[1].SlotOrder = 0, 0
	fixture.formPartyThroughClientAccept(t)
	member, _ := runtimeRaidMemberForTest(*state, 100)
	if member.GroupIndex != 1 || member.SlotOrder != 1 {
		t.Fatalf("raw group 0 was left unplaced or used the wrong slot: %+v", member)
	}
}

func TestPartyAcceptCountsRawZeroAgainstFullGroupCapacity(t *testing.T) {
	fixture := newInviteAcceptDungeonFixture(t)
	state := installPartyRaidStateForTest(t, fixture, 0, []runtimeRaidMemberState{
		{CharID: 200, GroupIndex: 1, SlotOrder: 1},
		{CharID: 201, GroupIndex: 1, SlotOrder: 2},
		{CharID: 202, GroupIndex: 1, SlotOrder: 3},
	})
	state.Members[1].GroupIndex, state.Members[1].SlotOrder = 0, 0
	fixture.formPartyThroughClientAccept(t)
	member, _ := runtimeRaidMemberForTest(*state, 100)
	if member.GroupIndex != 0 || member.SlotOrder != 0 {
		t.Fatalf("raw group 0 was overfilled into a full group: %+v", member)
	}
	count := 0
	for _, row := range state.Members {
		if normalizedRuntimeRaidGroup(row.GroupIndex) == 1 {
			count++
		}
	}
	if count != 5 {
		t.Fatalf("pre-plan capacity count=%d, want 5 including raw 0", count)
	}
}

func TestPartyAcceptSkipsActiveDungeonParticipant(t *testing.T) {
	fixture := newInviteAcceptDungeonFixture(t)
	state := installPartyRaidStateForTest(t, fixture, 0, nil)
	state.Started = true
	state.Battle = &runtimeRaidBattle{
		InstanceID: "party-sync-active", Mode: 1, Phase: 1,
		MemberAccounts: map[uint16]string{99: "account-1", 100: "account-1"},
		MemberGroups:   map[uint16]byte{99: 1, 100: 2},
		Participation:  map[uint16]currentRaidParticipation{100: {RunID: "active-run", Phase: 1}},
	}
	fixture.formPartyThroughClientAccept(t)
	member, _ := runtimeRaidMemberForTest(*state, 100)
	if member.GroupIndex != 2 {
		t.Fatalf("active dungeon participant was regrouped: %+v", member)
	}
	if state.Battle.MemberGroups[100] != 2 {
		t.Fatalf("active reward-frozen group changed: %+v", state.Battle.MemberGroups)
	}
}

func TestPartyRaidGroupSyncPlansAllMembersBeforeCommit(t *testing.T) {
	state := &runtimeRaidState{
		RaidKey: 2<<16 | 99, LeaderID: 99, RouteOrRaidType: 0,
		Members: []runtimeRaidMemberState{
			{CharID: 99, GroupIndex: 1, SlotOrder: 0},
			{CharID: 200, GroupIndex: 1, SlotOrder: 1},
			{CharID: 201, GroupIndex: 1, SlotOrder: 2},
			{CharID: 100, GroupIndex: 2, SlotOrder: 0},
			{CharID: 101, GroupIndex: 3, SlotOrder: 0},
		},
	}
	party := alignedcmd.PartyState{
		PartyID: 9, UserID: 99,
		Members: []alignedcmd.PartyMemberState{{UserID: 99}, {UserID: 100}, {UserID: 101}},
	}
	if candidate, ok := planRuntimeRaidGroupSynchronization(*state, party); ok || candidate != nil {
		t.Fatal("over-capacity plan reported success")
	}
	for _, tc := range []struct {
		charID uint16
		group  byte
	}{{100, 2}, {101, 3}} {
		member, _ := runtimeRaidMemberForTest(*state, tc.charID)
		if member.GroupIndex != tc.group {
			t.Fatalf("partial commit: member=%d group=%d want=%d", tc.charID, member.GroupIndex, tc.group)
		}
	}
}

func TestPartyAcceptRepairsSlotCollisionInSameNormalizedGroup(t *testing.T) {
	fixture := newInviteAcceptDungeonFixture(t)
	state := installPartyRaidStateForTest(t, fixture, 0, nil)
	state.Members[1].GroupIndex = 1
	state.Members[1].SlotOrder = 0
	fixture.formPartyThroughClientAccept(t)
	anchor, _ := runtimeRaidMemberForTest(*state, 99)
	member, _ := runtimeRaidMemberForTest(*state, 100)
	if anchor.SlotOrder == member.SlotOrder {
		t.Fatalf("historical slot collision was not repaired: anchor=%+v member=%+v", anchor, member)
	}
	if member.GroupIndex != 1 || member.SlotOrder != 1 {
		t.Fatalf("colliding member was not moved to the free slot: %+v", member)
	}
}

func TestPartyAcceptSkipsRaidBattleOwnerMismatch(t *testing.T) {
	fixture := newInviteAcceptDungeonFixture(t)
	state := installPartyRaidStateForTest(t, fixture, 0, nil)
	state.Started = true
	state.Battle = &runtimeRaidBattle{
		InstanceID: "party-sync-owner-mismatch", Mode: 1, Phase: 1,
		MemberAccounts: map[uint16]string{99: "account-1", 100: "other-account"},
		MemberGroups:   map[uint16]byte{99: 1, 100: 2},
	}
	fixture.formPartyThroughClientAccept(t)
	member, _ := runtimeRaidMemberForTest(*state, 100)
	if member.GroupIndex != 2 {
		t.Fatalf("owner-mismatched raid member was regrouped: %+v", member)
	}
}
