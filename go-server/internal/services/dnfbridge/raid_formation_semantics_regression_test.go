package dnfbridge

import (
	"os"
	"reflect"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRaidUneditedSoloSeatsDoNotMaterializeOrdinaryParty(t *testing.T) {
	state := runtimeRaidState{CurrentLayout: true, Started: true}
	for id := uint16(1); id <= 8; id++ {
		state.Members = append(state.Members, runtimeRaidMemberState{CharID: id, GroupIndex: 1, OrdinaryPartySolo: true})
	}
	if groups := runtimeRaidPartyGroups(state); len(groups) != 0 {
		t.Fatalf("automatic admission seats created ordinary parties: %v", groupSizesForTest(groups))
	}
	if runtimeRaidHasOversizedGroup(state) {
		t.Fatal("independent solo seats were counted as one oversized party")
	}
	state.CurrentLayout = false
	if groups := runtimeRaidPartyGroups(state); len(groups) != 1 || len(groups[0]) != 8 {
		t.Fatal("legacy grouping semantics changed")
	}
}

func TestRaidFreshPartyReusesFirstGroupDespiteAutomaticSeats(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(strconv.FormatBool(started), func(t *testing.T) {
			f := newInviteAcceptDungeonFixture(t)
			state := installPartyRaidStateForTest(t, f, 0, []runtimeRaidMemberState{{CharID: 200, GroupIndex: 1}, {CharID: 201, GroupIndex: 1}})
			state.Members[0].GroupIndex = 2
			state.Started = started
			before := append([]runtimeRaidMemberState(nil), state.Members...)
			if started {
				state.Battle = &runtimeRaidBattle{MemberAccounts: map[uint16]string{99: "account-1", 100: "account-1"}, MemberGroups: map[uint16]byte{99: 2, 100: 2}}
			}
			f.formPartyThroughClientAccept(t)
			for _, id := range []uint16{99, 100} {
				member, _ := runtimeRaidMemberForTest(*state, id)
				if member.GroupIndex != 1 || member.OrdinaryPartySolo {
					t.Fatalf("fresh party retained stale admission group: %+v, prior=%+v", member, before)
				}
			}
			if started && (state.Battle.MemberGroups[99] != 2 || state.Battle.MemberGroups[100] != 2) {
				t.Fatal("new ordinary party changed frozen reward grouping")
			}
		})
	}
}

func TestRaidNewPartyDoesNotRenumberExistingPartyOnRefresh(t *testing.T) {
	party := alignedcmd.PartyState{PartyID: 9, UserID: 1, Members: []alignedcmd.PartyMemberState{{UserID: 1, Slot: 0}, {UserID: 2, Slot: 1}}}
	for _, started := range []bool{false, true} {
		state := runtimeRaidState{CurrentLayout: true, Started: started, Members: []runtimeRaidMemberState{{CharID: 1, GroupIndex: 2}, {CharID: 2, GroupIndex: 2, SlotOrder: 1}}}
		before := cloneRuntimeRaidState(state)
		if candidate, changed := planRuntimeRaidGroupSynchronization(state, party); changed || candidate != nil {
			t.Fatalf("stable party was renumbered during refresh: %+v", candidate)
		}
		if !reflect.DeepEqual(before, state) {
			t.Fatal("planning modified live members")
		}
	}
}

func TestRaidFreshFormationRespectsOtherPartiesAndExplicitPlacement(t *testing.T) {
	party := alignedcmd.PartyState{PartyID: 9, UserID: 1, Members: []alignedcmd.PartyMemberState{{UserID: 1}, {UserID: 2, Slot: 1}}}
	for _, tc := range []struct {
		name       string
		explicit   bool
		otherParty bool
		want       byte
	}{{"first_free", false, false, 1}, {"keep_other_party", false, true, 2}, {"explicit_anchor", true, false, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			state := runtimeRaidState{CurrentLayout: true, Members: []runtimeRaidMemberState{{CharID: 1, GroupIndex: 2, RecruitingPlacementExplicit: tc.explicit}, {CharID: 2, GroupIndex: 2, SlotOrder: 1}}}
			if tc.otherParty {
				state.Members = append(state.Members, runtimeRaidMemberState{CharID: 3, GroupIndex: 1}, runtimeRaidMemberState{CharID: 4, GroupIndex: 1, SlotOrder: 1})
			}
			before := cloneRuntimeRaidState(state)
			candidate, changed := planRuntimeRaidGroupSynchronization(state, party, true)
			if !changed {
				candidate = state.Members
			}
			if candidate[0].GroupIndex != tc.want || candidate[1].GroupIndex != tc.want {
				t.Fatalf("fresh party stole a live group or ignored explicit placement: %+v", candidate)
			}
			if !reflect.DeepEqual(before, state) || !reflect.DeepEqual(candidate[2:], state.Members[2:]) {
				t.Fatal("formation plan changed unrelated party or input")
			}
		})
	}
}

func TestAntonFourUneditedSolosStaySoloThroughActualStart(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires paired current PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		t.Run(strconv.FormatBool(explicit), func(t *testing.T) {
			f := raidSafetyFixture(t, archive, 4)
			f.service.options.currentClientVersion = "official-24.9"
			f.service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
			f.state.CurrentLayout, f.state.TailFlag = true, 0
			if f.service.onlinePlayers == nil {
				f.service.onlinePlayers = newOnlinePlayerManager()
			}
			var leader *gameSession
			for i := range f.state.Members {
				member := &f.state.Members[i]
				member.GroupIndex, member.SlotOrder = 1, byte(i)
				member.RecruitingPlacementExplicit = explicit && i < 2
				session := f.session()
				session.selectedCharacterID = member.CharID
				if i > 0 {
					session.accountID = "account-" + strconv.Itoa(int(member.CharID))
				}
				session.loginPacketSeen, session.accountFromToken = true, true
				f.service.bindGameSessionCharacter(session, member.CharID)
				if i == 0 {
					leader = session
				}
				rule, err := f.service.currentRaidAdmissionRule(session)
				if err != nil {
					t.Fatal(err)
				}
				f.service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: member.CharID, AccountID: session.accountID, Session: session, TownID: rule.WaitingTown, AreaID: rule.WaitingArea, ChannelID: session.channel.ID, Instance: f.state.RaidKey})
			}
			started, ok, err := f.service.startRuntimeRaidWithEntryCost(leader)
			if err != nil || !ok || !started.Started {
				t.Fatalf("legitimate start rejected: ok=%v err=%v", ok, err)
			}
			groups := runtimeRaidPartyGroups(started)
			if !explicit && len(groups) != 0 || explicit && (len(groups) != 1 || len(groups[0]) != 2) {
				t.Fatalf("only explicitly placed members may form a party: %v", groupSizesForTest(groups))
			}
			if err := f.service.applyRuntimeRaidParties(started); err != nil {
				t.Fatal(err)
			}
			for i, member := range started.Members {
				party, found := f.service.runtimePartyManagerForService().SnapshotByUser(member.CharID, 0)
				wantPair := explicit && i < 2
				if wantPair != (found && len(party.Members) == 2) || (!wantPair && found && len(party.Members) > 1) {
					t.Fatalf("character%d unexpected materialized party: %+v found=%v", member.CharID, party, found)
				}
			}
		})
	}
}

// Replay a delayed successful pair join after the manager has already admitted
// a third member. The old response must not split the now-current trio.
func TestRaidDelayedFreshPairCannotSplitNewerTrio(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(strconv.FormatBool(started), func(t *testing.T) {
			f := newInviteAcceptDungeonFixture(t)
			state := installPartyRaidStateForTest(t, f, 0, []runtimeRaidMemberState{{CharID: 101, GroupIndex: 2, SlotOrder: 2}})
			for i := range state.Members {
				state.Members[i].GroupIndex = 2
			}
			state.Started = started
			if started {
				state.Battle = &runtimeRaidBattle{MemberAccounts: map[uint16]string{99: "account-1", 100: "account-1", 101: "account-1"}, MemberGroups: map[uint16]byte{99: 2, 100: 2, 101: 2}}
			}
			third := &gameSession{conn: &bufferConn{}, connID: "delayed-pair-third", channel: f.channel, residentChannel: f.channel}
			f.service.bindGameSessionCharacter(third, 101)
			manager := f.service.runtimePartyManagerForService()
			created := manager.Create(party.RuntimePartyMember{UserID: 99, SessionGeneration: boundGeneration(t, f.service, f.leader)}, alignedcmd.PartyState{MaxMembers: 4})
			pair := manager.Join(created.Party.ID, party.RuntimePartyMember{UserID: 100, SessionGeneration: boundGeneration(t, f.service, f.follower)})
			if !created.OK || !pair.OK {
				t.Fatal("pair setup failed")
			}
			stale := pair.Party.StateFor(99)
			trio := manager.Join(created.Party.ID, party.RuntimePartyMember{UserID: 101, SessionGeneration: boundGeneration(t, f.service, third)})
			if !trio.OK {
				t.Fatal("trio setup failed")
			}
			// Existing-party growth keeps its current group2; fresh old pair would
			// incorrectly move only 99/100 to free group1 without the revision guard.
			f.service.synchronizeOrdinaryPartyRaidGroups(trio.Party.StateFor(99), "newer_trio", trio.Party)
			before := cloneRuntimeRaidState(*state)
			if f.service.synchronizeOrdinaryPartyRaidGroups(stale, "delayed_pair", pair.Party) {
				t.Fatal("obsolete pair committed")
			}
			if !reflect.DeepEqual(before.Members, state.Members) || (started && (!reflect.DeepEqual(before.Battle.MemberGroups, state.Battle.MemberGroups) || !reflect.DeepEqual(before.Battle.MemberAccounts, state.Battle.MemberAccounts))) {
				t.Fatalf("old pair changed current raid: before=%+v after=%+v", before.Members, state.Members)
			}
			for _, member := range state.Members {
				if member.GroupIndex != 2 || member.OrdinaryPartySolo {
					t.Fatalf("trio split: %+v", state.Members)
				}
			}
			// Same membership shape after a join/leave also has a newer revision.
			left := manager.Leave(101, boundGeneration(t, f.service, third))
			if !left.OK {
				t.Fatal("leave failed")
			}
			before = cloneRuntimeRaidState(*state)
			if f.service.synchronizeOrdinaryPartyRaidGroups(stale, "pair_aba", pair.Party) || !reflect.DeepEqual(before.Members, state.Members) || (started && (!reflect.DeepEqual(before.Battle.MemberGroups, state.Battle.MemberGroups) || !reflect.DeepEqual(before.Battle.MemberAccounts, state.Battle.MemberAccounts))) {
				t.Fatal("old pair survived membership ABA")
			}
		})
	}
}
