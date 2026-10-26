package dnfbridge

import (
	"os"
	"strconv"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func runtimeRaidMemberForTest(state runtimeRaidState, charID uint16) (runtimeRaidMemberState, bool) {
	for _, member := range state.Members {
		if member.CharID == charID {
			return member, true
		}
	}
	return runtimeRaidMemberState{}, false
}

func fillRuntimeRaidDefaultGroupForTest(state *runtimeRaidState, chars ...uint16) {
	if state == nil {
		return
	}
	for i, charID := range chars {
		state.Members = append(state.Members, runtimeRaidMemberState{
			CharID: charID, GroupIndex: runtimeRaidDefaultGroup,
			SlotOrder: byte(i + 1), UserState: 1, HPPercent: 100, MPPercent: 100,
		})
	}
}

func TestCurrentRaidInvitationAcceptanceAssignsCapacityAwareGroupAndSlot(t *testing.T) {
	s, a, b, _, _, summary := prepareCurrentRaidInvitationTest(t)
	live := s.raids[summary.RaidKey]
	if live == nil {
		t.Fatal("fixture raid missing")
	}
	// The leader occupies the last free slot in the default group. A newcomer
	// must use the next legal group instead of overfilling group 1 or staying 0.
	fillRuntimeRaidDefaultGroupForTest(live, 200, 201, 202)

	if err := s.handleGameCommand(a, 1, 10, currentRaidInviteFixture(b.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameCommand(b, 1, 11, currentRaidApprovalFixture(a.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}

	state, ok := s.runtimeRaidForMember(b.selectedCharacterID)
	if !ok {
		t.Fatal("accepted invitee is not a raid member")
	}
	member, found := runtimeRaidMemberForTest(state, b.selectedCharacterID)
	if !found {
		t.Fatal("accepted invitee row missing")
	}
	if member.GroupIndex != runtimeRaidDefaultGroup+1 || member.SlotOrder != 0 {
		t.Fatalf("accepted invitee group=%d slot=%d, want group=%d slot=0",
			member.GroupIndex, member.SlotOrder, runtimeRaidDefaultGroup+1)
	}
	groups := runtimeRaidPartyGroups(state)
	if len(groups) != 2 || len(groups[0]) != 4 || len(groups[1]) != 1 {
		t.Fatalf("raid party groups=%v, want 4+1 members without overfill", groupSizesForTest(groups))
	}
}

func TestCurrentRaidApplicationApprovalAssignsCapacityAwareGroupAndSlot(t *testing.T) {
	s, a, b, _, _, summary := prepareCurrentRaidApplicationTest(t)
	live := s.raids[summary.RaidKey]
	if live == nil {
		t.Fatal("fixture raid missing")
	}
	fillRuntimeRaidDefaultGroupForTest(live, 200, 201, 202)

	body := currentRaidApplyFixture(a.selectedCharacterID)
	if err := s.handleGameCommand(b, 1, 820, body); err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameCommand(a, 1, 11, currentRaidApprovalFixture(b.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}

	state, ok := s.runtimeRaidForMember(b.selectedCharacterID)
	if !ok {
		t.Fatal("approved applicant is not a raid member")
	}
	member, found := runtimeRaidMemberForTest(state, b.selectedCharacterID)
	if !found {
		t.Fatal("approved applicant row missing")
	}
	if member.GroupIndex != runtimeRaidDefaultGroup+1 || member.SlotOrder != 0 {
		t.Fatalf("approved applicant group=%d slot=%d, want group=%d slot=0",
			member.GroupIndex, member.SlotOrder, runtimeRaidDefaultGroup+1)
	}
}

func TestAntonStartMaterializesUnassignedMemberGroupBeforePartyProjection(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	f := raidSafetyFixture(t, archive, 2)
	f.service.options.currentClientVersion = "official-24.9"
	f.service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
	f.state.CurrentLayout = true
	f.state.CurrentUIFlags = [3]byte{}
	f.state.TailFlag = 0
	f.state.NameBytes = []byte("group-normalization")
	f.state.Members[0].GroupIndex = runtimeRaidDefaultGroup
	f.state.Members[0].SlotOrder = 0
	f.state.Members[1].GroupIndex = 0
	f.state.Members[1].SlotOrder = 0

	leader := f.session()
	leader.loginPacketSeen, leader.accountFromToken = true, true
	f.service.bindGameSessionCharacter(leader, 19)
	peer := f.session()
	peer.selectedCharacterID, peer.accountID = 20, "account-20"
	peer.loginPacketSeen, peer.accountFromToken = true, true
	f.service.bindGameSessionCharacter(peer, 20)
	if f.service.onlinePlayers == nil {
		f.service.onlinePlayers = newOnlinePlayerManager()
	}
	for _, member := range f.state.Members {
		p, found := f.service.onlineGameSession(member.CharID)
		if !found {
			t.Fatal("missing authenticated start participant")
		}
		rule, err := f.service.currentRaidAdmissionRule(p)
		if err != nil {
			t.Fatal(err)
		}
		f.service.onlinePlayers.EnterArea(&onlinePlayerInfo{
			CharacterID: member.CharID, AccountID: p.accountID, Session: p,
			TownID: rule.WaitingTown, AreaID: rule.WaitingArea,
			ChannelID: p.channel.ID, Instance: f.state.RaidKey,
		})
	}

	started, ok, err := f.service.startRuntimeRaidWithEntryCost(leader)
	if err != nil || !ok || !started.Started {
		t.Fatalf("legitimate two-member raid start rejected: ok=%t err=%v state=%+v", ok, err, f.state)
	}
	follower, found := runtimeRaidMemberForTest(*f.state, 20)
	if !found || follower.GroupIndex == 0 {
		t.Fatalf("unassigned follower remained group 0 after start: %+v", follower)
	}
	if follower.GroupIndex != runtimeRaidDefaultGroup || follower.SlotOrder != 1 {
		t.Fatalf("follower canonical placement group=%d slot=%d, want group=%d slot=1",
			follower.GroupIndex, follower.SlotOrder, runtimeRaidDefaultGroup)
	}
	if groups := runtimeRaidPartyGroups(*f.state); len(groups) != 1 || len(groups[0]) != 2 {
		t.Fatalf("raid parties after canonical start=%v, want one two-member group", groupSizesForTest(groups))
	}
}

func groupSizesForTest(groups [][]runtimeRaidMemberState) []string {
	out := make([]string, 0, len(groups))
	for _, group := range groups {
		out = append(out, strconv.Itoa(len(group)))
	}
	return out
}

func TestRuntimeRaidGroupPlacementHonorsRouteCapacity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		route     byte
		members   []runtimeRaidMemberState
		wantGroup byte
		wantSlot  byte
	}{
		{
			name:  "anton_default_group_fills_four_places",
			route: 0,
			members: []runtimeRaidMemberState{
				{CharID: 1, GroupIndex: 1, SlotOrder: 0},
				{CharID: 2, GroupIndex: 0, SlotOrder: 0},
			},
			wantGroup: 1, wantSlot: 1,
		},
		{
			name:  "anton_full_default_group_uses_next_group",
			route: 0,
			members: []runtimeRaidMemberState{
				{CharID: 1, GroupIndex: 1, SlotOrder: 0},
				{CharID: 2, GroupIndex: 1, SlotOrder: 1},
				{CharID: 3, GroupIndex: 1, SlotOrder: 2},
				{CharID: 4, GroupIndex: 1, SlotOrder: 3},
				{CharID: 5, GroupIndex: 0, SlotOrder: 0},
			},
			wantGroup: 2, wantSlot: 0,
		},
		{
			name:  "fiendwar_uses_first_unique_single_member_group",
			route: 2,
			members: []runtimeRaidMemberState{
				{CharID: 1, GroupIndex: 1, SlotOrder: 0},
				{CharID: 2, GroupIndex: 3, SlotOrder: 0},
				{CharID: 3, GroupIndex: 0, SlotOrder: 0},
			},
			wantGroup: 2, wantSlot: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &runtimeRaidState{RouteOrRaidType: tc.route, Members: append([]runtimeRaidMemberState(nil), tc.members...)}
			if !normalizeRuntimeRaidMemberGroups(state) {
				t.Fatal("unassigned member was not normalized")
			}
			last := state.Members[len(state.Members)-1]
			if last.GroupIndex != tc.wantGroup || last.SlotOrder != tc.wantSlot {
				t.Fatalf("placement group=%d slot=%d, want group=%d slot=%d",
					last.GroupIndex, last.SlotOrder, tc.wantGroup, tc.wantSlot)
			}
		})
	}
}
