package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func raidStartIntentRealPairsForTest(t *testing.T) (*Service, runtimeRaidState, []*gameSession) {
	t.Helper()
	f := newInviteAcceptDungeonFixture(t)
	s := f.service
	s.options.currentClientVersion = "official-24.9"
	channel := f.leader.channel
	channel.ID, channel.Type = 23, dnfenum.RaidChannelType
	repos, ok := s.repositoryGroup()
	if !ok {
		t.Fatal("missing memory repositories")
	}
	sessions := []*gameSession{f.leader, f.follower}
	for _, id := range []uint16{101, 102} {
		if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{
			CharacterID: strconv.Itoa(int(id)), AccountID: "account-1", Name: fmt.Sprintf("intent%d", id),
			Job: "0", Level: 90, Stats: map[string]int64{"town_id": 38, "area_id": 1, "fatigue": 100},
		}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
			CharacterID: strconv.Itoa(int(id)), Slots: map[string]dnfrepo.ItemStack{},
		}); err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, &gameSession{conn: &bufferConn{}, connID: fmt.Sprintf("intent%d", id),
			accountID: "account-1", selectedCharacterID: id})
	}
	for _, session := range sessions {
		// The dungeon fixture uses Service.options.accountID as a fallback.
		// Raid authentication requires the actual selected session account too.
		session.accountID = "account-1"
		session.channel, session.residentChannel = channel, channel
		session.loginPacketSeen, session.accountFromToken = true, true
		s.bindGameSessionCharacter(session, session.selectedCharacterID)
		s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: session.selectedCharacterID,
			AccountID: session.accountID, Session: session, TownID: 38, AreaID: 1,
			PositionX: 450, PositionY: 234, ChannelID: channel.ID})
		if !s.currentRaidAuthenticated(session) {
			t.Fatalf("fixture session%d is not authenticated for its repository account/channel", session.selectedCharacterID)
		}
	}
	for _, pair := range [][2]*gameSession{{sessions[0], sessions[1]}, {sessions[2], sessions[3]}} {
		if ok, reason, _ := s.forceInviteParty(pair[0].selectedCharacterID, pair[1].selectedCharacterID, 0, [3]uint32{}); !ok {
			t.Fatalf("real ordinary-party invite rejected: %s", reason)
		}
		if err := s.handleOnlineResponsePeer(pair[1], uint16(dnfenum.CmdPacketResponsePeer),
			buildResponsePeerBody(pair[0].selectedCharacterID, 0, 0)); err != nil {
			t.Fatal(err)
		}
		snapshot, found := s.runtimePartyManagerForService().SnapshotByUser(pair[0].selectedCharacterID, 0)
		if !found || snapshot.Leader != pair[0].selectedCharacterID || len(snapshot.Members) != 2 ||
			len(runtimePartyMembers(runtimePartyStateSnapshot(pair[0]))) != 2 {
			t.Fatalf("real invite/accept did not form and publish pair%d/%d", pair[0].selectedCharacterID, pair[1].selectedCharacterID)
		}
	}
	if err := s.handleCurrentCreateRaid(sessions[0], 664, currentRaidRequestFixture(0, "intent raid", 0, 0, 0)); err != nil {
		t.Fatal(err)
	}
	created, found := s.runtimeRaidForMember(sessions[0].selectedCharacterID)
	if !found || created.LeaderID != sessions[0].selectedCharacterID {
		t.Fatal("real raid creation rejected the authenticated leader")
	}
	for _, session := range sessions[1:] {
		if existing, joined := s.runtimeRaidForMember(session.selectedCharacterID); joined {
			if existing.RaidKey != created.RaidKey {
				t.Fatalf("fixture member%d already belongs to another raid", session.selectedCharacterID)
			}
			continue
		}
		request := currentRaidApplyFixture(sessions[0].selectedCharacterID)
		request[0] = byte(channel.ID)
		request[9] = byte(s.channelAdvertiseID())
		if err := s.handleGameCommand(session, 1, 820, request); err != nil {
			t.Fatal(err)
		}
		pending, pendingFound := s.currentRaidApplications[session.selectedCharacterID]
		if !pendingFound || pending.raidKey != created.RaidKey || pending.applicant.character != session.selectedCharacterID {
			t.Fatalf("real raid application rejected applicant%d on channel%d/type%d", session.selectedCharacterID, session.channel.ID, session.channel.Type)
		}
		if err := s.handleGameCommand(sessions[0], 1, 11, currentRaidApprovalFixture(session.selectedCharacterID)); err != nil {
			t.Fatal(err)
		}
		if admitted, joined := s.runtimeRaidForMember(session.selectedCharacterID); !joined || admitted.RaidKey != created.RaidKey {
			t.Fatalf("real raid approval did not admit applicant%d", session.selectedCharacterID)
		}
	}
	state, found := s.runtimeRaidForMember(sessions[0].selectedCharacterID)
	if !found || len(state.Members) != 4 {
		t.Fatal("real creation/application/approval did not admit four members")
	}
	for _, member := range state.Members {
		if member.GroupIndex != 1 || member.RecruitingPlacementExplicit {
			t.Fatalf("fixture must reproduce four automatic group1 seats: %+v", member)
		}
	}
	for _, session := range sessions {
		session.conn.(*bufferConn).write.Reset()
	}
	return s, state, sessions
}

func raidStartIntentMemberForTest(t *testing.T, state runtimeRaidState, id uint16) runtimeRaidMemberState {
	t.Helper()
	member, found := runtimeRaidMemberForTest(state, id)
	if !found {
		t.Fatalf("planned raid member %d missing", id)
	}
	return member
}

func raidStartIntentManagerPairForTest(t *testing.T, manager *party.RuntimePartyManager, a, b uint16) party.RuntimePartySnapshot {
	t.Helper()
	created := manager.Create(party.RuntimePartyMember{UserID: a, SessionGeneration: 1}, alignedcmd.PartyState{})
	if !created.OK {
		t.Fatalf("create ordinary party: %s", created.Reason)
	}
	joined := manager.Join(created.Party.ID, party.RuntimePartyMember{UserID: b, SessionGeneration: 1})
	if !joined.OK {
		t.Fatalf("join ordinary party: %s", joined.Reason)
	}
	return joined.Party
}

func TestRaidStartPartyIntentKeepsRealPairsAndPartyIDsFromAutomaticSameGroup(t *testing.T) {
	s, state, sessions := raidStartIntentRealPairsForTest(t)
	manager := s.runtimePartyManagerForService()
	beforeParties := manager.Snapshots()
	if len(beforeParties) != 2 || len(beforeParties[0].Members) != 2 || len(beforeParties[1].Members) != 2 {
		t.Fatal("fixture did not retain two actual ordinary pairs")
	}
	before := cloneRuntimeRaidState(state)
	for _, route := range []byte{0, 1} {
		t.Run(strconv.Itoa(int(route)), func(t *testing.T) {
			input := cloneRuntimeRaidState(state)
			input.RouteOrRaidType = route
			planned, err := s.prepareCurrentRaidStartPartyIntent(input)
			if err != nil {
				t.Fatal(err)
			}
			groupA := raidStartIntentMemberForTest(t, planned, 99).GroupIndex
			groupB := raidStartIntentMemberForTest(t, planned, 101).GroupIndex
			if groupA == 0 || groupB == 0 || groupA == groupB {
				t.Fatalf("distinct actual parties were merged: groups=%d/%d", groupA, groupB)
			}
			for _, snapshot := range beforeParties {
				group := raidStartIntentMemberForTest(t, planned, snapshot.Leader).GroupIndex
				for _, member := range snapshot.Members {
					row := raidStartIntentMemberForTest(t, planned, member.UserID)
					if row.GroupIndex != group || row.SlotOrder != member.Slot || row.OrdinaryPartySolo || row.RecruitingPlacementExplicit {
						t.Fatalf("party%d lost its actual membership/slot: %+v, actual=%+v", snapshot.ID, row, member)
					}
				}
			}
			if !reflect.DeepEqual(manager.Snapshots(), beforeParties) || !reflect.DeepEqual(state, before) {
				t.Fatal("intent helper changed input or party manager")
			}
			for _, session := range sessions {
				if session.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("intent helper wrote to a client")
				}
			}
			planned.Started = true
			if err := s.applyRuntimeRaidParties(planned); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(manager.Snapshots(), beforeParties) {
				t.Fatal("start materialization replaced original parties, leaders, slots or membership revisions")
			}
			for _, snapshot := range beforeParties {
				for _, member := range snapshot.Members {
					current, found := manager.SnapshotByUser(member.UserID, member.SessionGeneration)
					if !found || current.ID != snapshot.ID {
						t.Fatalf("member%d lost original party%d", member.UserID, snapshot.ID)
					}
				}
			}
			for _, session := range sessions {
				conn := session.conn.(*bufferConn)
				for _, packet := range splitAllGameServerUpperPackets(t, conn.write.Bytes()) {
					if packet.Header.MsgID == 11 {
						t.Fatal("unchanged actual party replayed op11 formation")
					}
				}
				conn.write.Reset()
			}
		})
	}
}

func TestRaidStartPartyIntentExplicitRegroupLeavesOtherHalvesSolo(t *testing.T) {
	s, state, sessions := raidStartIntentRealPairsForTest(t)
	for _, id := range []uint16{99, 101} {
		body := make([]byte, 12)
		binary.LittleEndian.PutUint32(body[4:8], uint32(id))
		binary.LittleEndian.PutUint32(body[8:12], 3)
		if err := s.handleGameCommand(sessions[0], 1, uint16(dnfenum.CmdPacketRaidManagerWork), body); err != nil {
			t.Fatal(err)
		}
	}
	state, _ = s.runtimeRaidForMember(99)
	before := cloneRuntimeRaidState(state)
	parties := s.runtimePartyManagerForService().Snapshots()
	planned, err := s.prepareCurrentRaidStartPartyIntent(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint16{99, 101} {
		member := raidStartIntentMemberForTest(t, planned, id)
		original := raidStartIntentMemberForTest(t, before, id)
		if member.GroupIndex != 3 || member.SlotOrder != original.SlotOrder || member.OrdinaryPartySolo || !member.RecruitingPlacementExplicit {
			t.Fatalf("explicit manager placement lost: %+v", member)
		}
	}
	for _, id := range []uint16{100, 102} {
		member := raidStartIntentMemberForTest(t, planned, id)
		original := raidStartIntentMemberForTest(t, before, id)
		if !member.OrdinaryPartySolo || member.GroupIndex != original.GroupIndex || member.SlotOrder != original.SlotOrder {
			t.Fatalf("unassigned half of an explicitly split party must stay solo: %+v", member)
		}
	}
	groups := runtimeRaidPartyGroups(planned)
	if len(groups) != 1 || len(groups[0]) != 2 || groups[0][0].GroupIndex != 3 {
		t.Fatal("explicit regroup silently reattached the other halves")
	}
	if !reflect.DeepEqual(state, before) || !reflect.DeepEqual(parties, s.runtimePartyManagerForService().Snapshots()) {
		t.Fatal("preparing explicit regroup mutated owners")
	}
}

func TestRaidStartPartyIntentReservesExplicitGroupsButNotSoloSeats(t *testing.T) {
	manager := party.NewRuntimePartyManager()
	raidStartIntentManagerPairForTest(t, manager, 1, 2)
	raidStartIntentManagerPairForTest(t, manager, 3, 4)
	s := &Service{options: options{currentClientVersion: "official-24.9"}, runtimePartyManager: manager}
	state := runtimeRaidState{CurrentLayout: true, Members: []runtimeRaidMemberState{
		{CharID: 1, GroupIndex: 1}, {CharID: 2, GroupIndex: 1, SlotOrder: 1},
		{CharID: 3, GroupIndex: 1, SlotOrder: 2}, {CharID: 4, GroupIndex: 1, SlotOrder: 3},
		{CharID: 5, GroupIndex: 1, RecruitingPlacementExplicit: true},
		{CharID: 6, GroupIndex: 2, SlotOrder: 3},
	}}
	planned, err := s.prepareCurrentRaidStartPartyIntent(state)
	if err != nil {
		t.Fatal(err)
	}
	for id, group := range map[uint16]byte{1: 2, 2: 2, 3: 3, 4: 3, 5: 1} {
		member := raidStartIntentMemberForTest(t, planned, id)
		if member.GroupIndex != group || member.OrdinaryPartySolo {
			t.Fatalf("member%d group=%d solo=%t, want group%d", id, member.GroupIndex, member.OrdinaryPartySolo, group)
		}
	}
	solo := raidStartIntentMemberForTest(t, planned, 6)
	if !solo.OrdinaryPartySolo || solo.GroupIndex != 2 || solo.SlotOrder != 3 {
		t.Fatal("independent solo reserved a group or lost its canonical raw seat")
	}
}

func TestRaidStartPartyIntentPreservesDelegatedLeaderAndFixedSlots(t *testing.T) {
	manager := party.NewRuntimePartyManager()
	snapshot := raidStartIntentManagerPairForTest(t, manager, 1, 2)
	third := manager.Join(snapshot.ID, party.RuntimePartyMember{UserID: 3, SessionGeneration: 1})
	if !third.OK || !manager.Leave(2, 1).OK || !manager.TransferLeader(1, 1, 3, 1).OK {
		t.Fatal("failed to prepare actual slot hole and delegated leader")
	}
	before := manager.Snapshots()
	if len(before) != 1 || before[0].Leader != 3 || len(before[0].Members) != 2 || before[0].Members[1].Slot != 2 {
		t.Fatal("fixture lacks a leader outside slot0 and a fixed slot hole")
	}
	s := &Service{options: options{currentClientVersion: "official-24.9"}, runtimePartyManager: manager}
	state := runtimeRaidState{CurrentLayout: true, Members: []runtimeRaidMemberState{
		{CharID: 3, GroupIndex: 1, SlotOrder: 0}, {CharID: 1, GroupIndex: 1, SlotOrder: 1},
	}}
	planned, err := s.prepareCurrentRaidStartPartyIntent(state)
	if err != nil {
		t.Fatal(err)
	}
	if a, b := raidStartIntentMemberForTest(t, planned, 1), raidStartIntentMemberForTest(t, planned, 3); a.SlotOrder != 0 || b.SlotOrder != 2 || a.GroupIndex != b.GroupIndex || a.OrdinaryPartySolo || b.OrdinaryPartySolo {
		t.Fatalf("actual fixed slots were compacted or leader was forced into slot0: %+v", planned.Members)
	}
	if !reflect.DeepEqual(before, manager.Snapshots()) {
		t.Fatal("intent helper changed actual party leader or slot hole")
	}
}

func TestRaidStartPartyIntentRejectsMixedRaidMembership(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(strconv.FormatBool(explicit), func(t *testing.T) {
			manager := party.NewRuntimePartyManager()
			raidStartIntentManagerPairForTest(t, manager, 1, 2)
			s := &Service{options: options{currentClientVersion: "official-24.9"}, runtimePartyManager: manager}
			state := runtimeRaidState{CurrentLayout: true, Members: []runtimeRaidMemberState{{CharID: 1, GroupIndex: 1, RecruitingPlacementExplicit: explicit}}}
			before, parties := cloneRuntimeRaidState(state), manager.Snapshots()
			_, err := s.prepareCurrentRaidStartPartyIntent(state)
			if err == nil || !strings.Contains(err.Error(), "non-raid member 2") {
				t.Fatalf("mixed raid/non-raid party must reject without silently importing or detaching member2: %v", err)
			}
			if !reflect.DeepEqual(state, before) || !reflect.DeepEqual(manager.Snapshots(), parties) {
				t.Fatal("mixed-party rejection mutated input or actual ordinary party")
			}
		})
	}
}

func TestRaidStartPartyIntentRejectsGroupExhaustionWithoutMerging(t *testing.T) {
	manager := party.NewRuntimePartyManager()
	state := runtimeRaidState{CurrentLayout: true}
	for id := uint16(1); id <= 12; id += 2 {
		raidStartIntentManagerPairForTest(t, manager, id, id+1)
		state.Members = append(state.Members, runtimeRaidMemberState{CharID: id, GroupIndex: 1}, runtimeRaidMemberState{CharID: id + 1, GroupIndex: 1, SlotOrder: 1})
	}
	s := &Service{options: options{currentClientVersion: "official-24.9"}, runtimePartyManager: manager}
	before, parties := cloneRuntimeRaidState(state), manager.Snapshots()
	_, err := s.prepareCurrentRaidStartPartyIntent(state)
	if err == nil || !strings.Contains(err.Error(), "no free group") {
		t.Fatalf("six distinct ordinary parties must reject before admission: %v", err)
	}
	if !reflect.DeepEqual(state, before) || !reflect.DeepEqual(manager.Snapshots(), parties) {
		t.Fatal("group exhaustion partially committed or merged parties")
	}
}

func TestRaidStartPartyIntentExcludedModesRemainUnchanged(t *testing.T) {
	for _, kind := range []string{"route2", "single", "legacy_layout", "legacy_profile", "already_started"} {
		t.Run(kind, func(t *testing.T) {
			manager := party.NewRuntimePartyManager()
			raidStartIntentManagerPairForTest(t, manager, 1, 2)
			s := &Service{options: options{currentClientVersion: "official-24.9"}, runtimePartyManager: manager}
			state := runtimeRaidState{CurrentLayout: true, Members: []runtimeRaidMemberState{{CharID: 1, GroupIndex: 2}}}
			switch kind {
			case "route2":
				state.RouteOrRaidType = 2
			case "single":
				state.CurrentUIFlags[0] = 1
			case "legacy_layout":
				state.CurrentLayout = false
			case "legacy_profile":
				s.options.currentClientVersion = "legacy"
			case "already_started":
				state.Started = true
			}
			before, parties := cloneRuntimeRaidState(state), manager.Snapshots()
			got, err := s.prepareCurrentRaidStartPartyIntent(state)
			if err != nil || !reflect.DeepEqual(got, before) || !reflect.DeepEqual(state, before) || !reflect.DeepEqual(manager.Snapshots(), parties) {
				t.Fatalf("excluded mode changed or ran normal-raid mixed-member validation: %v", err)
			}
		})
	}
}

func TestRaidStartPartyIntentSoloDoesNotCreateManagerOrReadRepositories(t *testing.T) {
	s := &Service{options: options{currentClientVersion: "official-24.9"}, repositoryProvider: func() (dnfrepo.Group, bool) {
		t.Fatal("intent helper must not read repositories")
		return dnfrepo.Group{}, false
	}}
	state := runtimeRaidState{CurrentLayout: true, Members: []runtimeRaidMemberState{{CharID: 1, GroupIndex: 5, SlotOrder: 3}, {CharID: 2}}}
	before := cloneRuntimeRaidState(state)
	planned, err := s.prepareCurrentRaidStartPartyIntent(state)
	if err != nil {
		t.Fatal(err)
	}
	for i, member := range planned.Members {
		if !member.OrdinaryPartySolo || member.GroupIndex != before.Members[i].GroupIndex || member.SlotOrder != before.Members[i].SlotOrder {
			t.Fatal("independent solo raw admission identity changed")
		}
	}
	if s.runtimePartyManager != nil || !reflect.DeepEqual(state, before) {
		t.Fatal("intent helper lazily created a manager or mutated its input")
	}
}
