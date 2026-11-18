package dnfbridge

import (
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func raidManualClearCommand(t *testing.T, f *r195SoloPrefixFixture, actor *gameSession, action, member, group uint32) {
	t.Helper()
	if err := f.service.handleGameCommand(actor, 1, uint16(dnfenum.CmdPacketRaidManagerWork), buildRaidManagerWorkRequestForBridgeTest(action, member, group)); err != nil {
		t.Fatal(err)
	}
}

// Current public V215's group-selector callback emits 669/{0, uid, 0}
// for every row in the selected group. Group zero clears a planned placement.
func TestRaidManualClearNativeGroupZeroAndReassign(t *testing.T) {
	f := newR195SoloPrefixFixture(t)
	for _, id := range []uint16{f.leaderID, f.memberID} {
		raidManualClearCommand(t, f, f.leader, 0, uint32(id), 1)
	}
	before := f.state(t)
	parties := f.service.runtimePartyManagerForService().Snapshots()
	f.leaderConn.write.Reset()
	f.memberConn.write.Reset()
	for repeat := 0; repeat < 2; repeat++ {
		raidManualClearCommand(t, f, f.leader, 0, uint32(f.memberID), 0)
		state := f.state(t)
		cleared := raidStartIntentMemberForTest(t, state, f.memberID)
		if cleared.GroupIndex != 0 || cleared.SlotOrder != 0 || cleared.RecruitingPlacementExplicit || !cleared.OrdinaryPartySolo {
			t.Fatalf("native zero-group request did not clear planned placement: %+v", cleared)
		}
		if !reflect.DeepEqual(raidStartIntentMemberForTest(t, before, f.leaderID), raidStartIntentMemberForTest(t, state, f.leaderID)) {
			t.Fatal("clear changed an unrelated member")
		}
		f.assertRosters(t, f.leaderConn.write.Bytes(), map[uint16][2]byte{f.leaderID: {1, 0}, f.memberID: {0, 0}})
		f.assertRosters(t, f.memberConn.write.Bytes(), map[uint16][2]byte{f.leaderID: {1, 0}, f.memberID: {0, 0}})
		for _, conn := range []*bufferConn{f.leaderConn, f.memberConn} {
			for _, frame := range splitUpperFrames(t, conn.write.Bytes()) {
				if frame.msgID == 669 || frame.msgID == 587 {
					t.Fatal("clear sent an unsolicited ACK or phase-start packet")
				}
			}
		}
	}
	raidManualClearCommand(t, f, f.leader, 0, uint32(f.memberID), 2)
	reassigned := raidStartIntentMemberForTest(t, f.state(t), f.memberID)
	if reassigned.GroupIndex != 2 || !reassigned.RecruitingPlacementExplicit || reassigned.OrdinaryPartySolo {
		t.Fatal("cleared member cannot be explicitly reassigned", reassigned)
	}
	if !reflect.DeepEqual(parties, f.service.runtimePartyManagerForService().Snapshots()) {
		t.Fatal("planning edit mutated ordinary-party ownership")
	}
}

func TestRaidManualClearFullGroupsCanSwapMembers(t *testing.T) {
	state := &runtimeRaidState{RaidKey: 1, LeaderID: 19, CurrentLayout: true}
	for id := uint16(19); id <= 26; id++ {
		state.Members = append(state.Members, runtimeRaidMemberState{CharID: id, GroupIndex: 1 + byte((id-19)/4), SlotOrder: byte((id - 19) % 4), RecruitingPlacementExplicit: true})
	}
	s := &Service{options: options{currentClientVersion: "official-24.9"}, raids: map[uint32]*runtimeRaidState{1: state}}
	before := cloneRuntimeRaidState(*state)
	for _, step := range [][2]uint16{{20, 0}, {24, 0}, {24, 1}, {20, 2}} {
		if _, ok, _ := s.moveRuntimeRaidMember(19, step[0], byte(step[1])); !ok {
			t.Fatalf("native clear-then-place sequence rejected: member=%d group=%d", step[0], step[1])
		}
	}
	for _, id := range []uint16{19, 21, 22, 23, 25, 26} {
		if !reflect.DeepEqual(raidStartIntentMemberForTest(t, before, id), raidStartIntentMemberForTest(t, *state, id)) {
			t.Fatal("swap changed an unrelated member", id)
		}
	}
	for group := byte(1); group <= 2; group++ {
		slots := map[byte]bool{}
		for _, member := range state.Members {
			if member.GroupIndex != group {
				continue
			}
			if member.SlotOrder >= 4 || slots[member.SlotOrder] {
				t.Fatal("swap repeated or overflowed a fixed slot", member)
			}
			slots[member.SlotOrder] = true
		}
		if len(slots) != 4 {
			t.Fatal("swap lost a group seat", group, slots)
		}
	}
}

func TestRaidManualClearRejectsAliasedFieldsAndForeignRequests(t *testing.T) {
	f := newR195SoloPrefixFixture(t)
	raidManualClearCommand(t, f, f.leader, 0, uint32(f.memberID), 2)
	before := f.state(t)
	f.leaderConn.write.Reset()
	f.memberConn.write.Reset()
	for _, tc := range []struct {
		actor                 *gameSession
		action, member, group uint32
	}{
		{f.member, 0, uint32(f.leaderID), 0},
		{f.leader, 0, 65536 + uint32(f.memberID), 0},
		{f.leader, 0, uint32(f.memberID), 256},
		{f.leader, 0, uint32(f.memberID), 257},
		{f.leader, 1, uint32(f.memberID), 0},
		{f.leader, 0, 0, 0},
		{f.leader, 0, 65000, 0},
	} {
		raidManualClearCommand(t, f, tc.actor, tc.action, tc.member, tc.group)
		if !reflect.DeepEqual(before, f.state(t)) {
			t.Fatalf("rejected request changed state: action=%d member=%d group=%d", tc.action, tc.member, tc.group)
		}
	}
	if f.leaderConn.write.Len() != 0 || f.memberConn.write.Len() != 0 {
		t.Fatal("rejected zero-group request published a roster")
	}
}

func TestRaidManualClearLegacyAndFiendWarKeepTheirPolicy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current bool
		route   byte
	}{{"legacy", false, 0}, {"fiendwar", true, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			state := &runtimeRaidState{RaidKey: 1, LeaderID: 19, CurrentLayout: tc.current, RouteOrRaidType: tc.route, Members: []runtimeRaidMemberState{{CharID: 19, GroupIndex: 1}}}
			s := &Service{raids: map[uint32]*runtimeRaidState{1: state}}
			before := cloneRuntimeRaidState(*state)
			if _, ok, _ := s.moveRuntimeRaidMember(19, 19, 0); ok || !reflect.DeepEqual(before, *state) {
				t.Fatal("unsupported route/layout accepted clear")
			}
		})
	}
}

func TestRaidManualClearStartIntentStaysSolo(t *testing.T) {
	f := newR195SoloPrefixFixture(t)
	for _, id := range []uint16{f.leaderID, f.memberID} {
		raidManualClearCommand(t, f, f.leader, 0, uint32(id), 1)
	}
	for _, id := range []uint16{f.leaderID, f.memberID} {
		raidManualClearCommand(t, f, f.leader, 0, uint32(id), 0)
	}
	planned, err := f.service.prepareCurrentRaidStartPartyIntent(f.state(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range planned.Members {
		if member.RecruitingPlacementExplicit || !member.OrdinaryPartySolo {
			t.Fatal("cleared manager placement returned at start", member)
		}
	}
	if len(runtimeRaidPartyGroups(planned)) != 0 {
		t.Fatal("cleared placements materialized an ordinary party")
	}
}

func TestRaidManualClearPreservesExistingOrdinaryPartyAuthority(t *testing.T) {
	s, state, sessions := raidStartIntentRealPairsForTest(t)
	parties := s.runtimePartyManagerForService().Snapshots()
	if _, ok, _ := s.moveRuntimeRaidMember(state.LeaderID, sessions[0].selectedCharacterID, 0); !ok {
		t.Fatal("clear rejected")
	}
	if !reflect.DeepEqual(parties, s.runtimePartyManagerForService().Snapshots()) {
		t.Fatal("planning clear dissolved an existing ordinary party")
	}
	current, ok := s.runtimeRaidForMember(state.LeaderID)
	if !ok {
		t.Fatal("raid disappeared")
	}
	if _, err := s.prepareCurrentRaidStartPartyIntent(current); err != nil {
		t.Fatal("clear left an invalid start intent", err)
	}
}

func TestRealPVFRaidManualClearKeepsFrozenBattleOwnership(t *testing.T) {
	x := reviewManagerMoveSetup(t)
	// The older progress fixture predates the current-layout flag. A real
	// current-client raid sets it during creation/admission.
	x.f.state.CurrentLayout = true
	s := x.f.service
	before := cloneRuntimeRaidState(*x.f.state).Battle
	reviewManagerCommand(t, x, x.peers[0], 20, 0)
	if raidStartIntentMemberForTest(t, *x.f.state, 20).GroupIndex != 0 {
		t.Fatal("actual current-layout manager clear was rejected")
	}
	reviewManagerWaitVersion(t, x.peers[0], before.ProgressVersion+1)
	reviewManagerWaitVersion(t, x.peers[1], before.ProgressVersion+1)
	s.raidMu.Lock()
	after := cloneRuntimeRaidState(*x.f.state).Battle
	s.raidMu.Unlock()
	if after.ProgressVersion != before.ProgressVersion+1 {
		t.Fatal("clear did not publish one progress revision")
	}
	before.ProgressVersion = after.ProgressVersion
	if !reflect.DeepEqual(before, after) {
		t.Fatal("clear changed frozen admission, reward, run or deadline owners")
	}
	for _, conn := range x.conns {
		rows, rosters := reviewManagerRows(t, conn)
		if len(rows) == 0 || rosters != 1 {
			t.Fatal("clear did not publish current roster/progress", len(rows), rosters)
		}
		conn.reset()
	}
	reviewManagerCommand(t, x, x.peers[0], 20, 0)
	if x.f.state.Battle.ProgressVersion != after.ProgressVersion {
		t.Fatal("repeated clear changed progress version")
	}
	for _, conn := range x.conns {
		rows, rosters := reviewManagerRows(t, conn)
		if len(rows) != 0 || rosters != 1 {
			t.Fatal("repeat clear emitted another progress transition")
		}
	}
}

func TestRealPVFRaidManualClearNextPhaseKeepsSoloAndSettledRewards(t *testing.T) {
	for _, mode := range []string{"leader", "clock"} {
		t.Run(mode, func(t *testing.T) {
			f, leader, _ := firstAntonFlowFixture(t)
			if _, ok, _ := f.service.moveRuntimeRaidMember(19, 20, 0); !ok {
				t.Fatal("started raid rejected the native clear")
			}
			flow := f.state.Battle.RewardFlow[0]
			owners := append([]currentAntonRewardRecipient(nil), flow.Recipients...)
			for _, offset := range []time.Duration{0, 20 * time.Second, 40 * time.Second} {
				if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(offset), zeroAntonRoll); len(errs) != 0 {
					t.Fatal(errs)
				}
			}
			if !reflect.DeepEqual(owners, flow.Recipients) || f.state.Battle.MemberGroups[20] != 1 {
				t.Fatal("clearing the live row changed the earned reward owners")
			}
			before := cloneRuntimeRaidState(*f.state)
			bags := raidWaitingInventories(t, f)
			// A refused next phase must not leak its planned canonical seats.
			f.service.raidMu.Lock()
			ok, err := f.service.beginCurrentAntonNextPhaseLocked(f.state, flow.BreakUntil)
			f.service.raidMu.Unlock()
			if ok || err == nil || !reflect.DeepEqual(before, cloneRuntimeRaidState(*f.state)) {
				t.Fatal("waiting-area refusal changed the pending placement", ok, err)
			}
			enterRaidStartWaitingFixture(t, f, f.state.Battle.InstanceID)
			if mode == "clock" {
				states, errs := f.service.advanceCurrentRaidClocks(flow.BreakUntil.Add(time.Second))
				if len(states) != 1 || len(errs) != 0 {
					t.Fatal("cleared row blocked automatic next phase", states, errs)
				}
			} else if _, ok, err := f.service.startRuntimeRaidWithEntryCost(leader); !ok || err != nil {
				t.Fatal("cleared row blocked leader next phase", ok, err)
			}
			member := raidStartIntentMemberForTest(t, *f.state, 20)
			if f.state.Battle.Phase != 1 || member.GroupIndex < 1 || member.GroupIndex > runtimeRaidMaxGroups ||
				!member.OrdinaryPartySolo || member.RecruitingPlacementExplicit {
				t.Fatal("next phase lost the cleared member's admission or solo intent", member)
			}
			if !reflect.DeepEqual(bags, raidWaitingInventories(t, f)) || !reflect.DeepEqual(owners, flow.Recipients) {
				t.Fatal("next phase changed settled assets or phase-zero recipients")
			}
		})
	}
}
