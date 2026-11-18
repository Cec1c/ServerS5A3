package dnfbridge

import (
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func TestRecruitingRaidExplicitPlacementSurvivesLiveSoloProjection(t *testing.T) {
	f := newR195SoloPrefixFixture(t)
	initial := f.state(t)
	beforeParty := runtimePartyStateSnapshot(f.leader)
	for _, row := range currentRuntimeRaidMembers(f.service.currentRaidLivePartyProjection(initial)) {
		if row.Core.GroupIndex != 0 {
			t.Fatal("untouched admission seat should retain initial solo display")
		}
	}
	// Same-group selection is also an explicit placement, not just a change
	// in GroupIndex. Repeating it must keep the visible assignment.
	initialGroup := mustR195SoloPrefixMember(t, initial, f.leaderID).GroupIndex
	for _, group := range []byte{initialGroup, 2, 2} {
		body := make([]byte, 12)
		binary.LittleEndian.PutUint32(body[4:8], uint32(f.leaderID))
		binary.LittleEndian.PutUint32(body[8:12], uint32(group))
		f.leaderConn.write.Reset()
		f.memberConn.write.Reset()
		if err := f.service.handleGameCommand(f.leader, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketRaidManagerWork), body); err != nil {
			t.Fatal(err)
		}
		state := f.state(t)
		leader := mustR195SoloPrefixMember(t, state, f.leaderID)
		if state.Started || state.Battle != nil || !leader.RecruitingPlacementExplicit || leader.GroupIndex != group {
			t.Fatal("manager placement did not retain the recruiting seat")
		}
		want := map[uint16][2]byte{f.leaderID: {group, leader.SlotOrder}, f.memberID: {0, 0}}
		f.assertRosters(t, f.leaderConn.write.Bytes(), want)
		f.assertRosters(t, f.memberConn.write.Bytes(), want)
		f.memberConn.write.Reset()
		if err := f.service.sendRuntimeRaidMetadata(f.member, state, 2); err != nil {
			t.Fatal(err)
		}
		packet, _ := splitGameServerUpperPacket(t, f.memberConn.write.Bytes())
		if got := r195SoloPrefixMetadataLeaderGroup(t, packet.Body, f.raidKey); got != group {
			t.Fatalf("reopened metadata hid manual placement: got=%d want=%d", got, group)
		}
		if !reflect.DeepEqual(state, f.state(t)) || !reflect.DeepEqual(beforeParty, runtimePartyStateSnapshot(f.leader)) {
			t.Fatal("wire projection mutated raid seats or created an ordinary party")
		}
	}
	// Once started, only live ordinary parties decide the chip. The retained
	// recruiting marker must not hide later solo/leave transitions or rewards.
	f.installBattle(t)
	frozen := f.frozen(t)
	assertStarted := func(wantSolo bool) {
		t.Helper()
		state := f.state(t)
		body, err := f.service.buildRuntimeRaidRosterBody(state)
		if err != nil {
			t.Fatal(err)
		}
		rows := parseR195CurrentRaidRoster(t, body, f.raidKey)
		for _, id := range []uint16{f.leaderID, f.memberID} {
			row := r195SoloPrefixRosterMember(t, rows, id)
			if (row.GroupIndex == 0) != wantSolo {
				t.Fatalf("started member %d solo=%v want=%v", id, row.GroupIndex == 0, wantSolo)
			}
		}
		f.assertFrozen(t, frozen)
	}
	assertStarted(true)
	f.formOrdinaryParty(t)
	assertStarted(false)
	f.leaveOrdinaryParty(t, f.member)
	assertStarted(true)
}

func TestJoinedRaidMemberCanBeRepositionedBeforeStartWithoutTownRebind(t *testing.T) {
	s, leader, member, lc, mc, state := prepareCurrentRaidInvitationTest(t)
	if err := s.handleGameCommand(leader, 1, 10, currentRaidInviteFixture(member.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameCommand(member, 1, 11, currentRaidApprovalFixture(leader.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}
	live, ok := s.runtimeRaidForMember(member.selectedCharacterID)
	if !ok || live.RaidKey != state.RaidKey || live.Started || live.Battle != nil {
		t.Fatal("join did not preserve a pre-start raid")
	}
	// Management authority comes from the raid leader and current identity,
	// not from both players sharing a town area.
	s.onlinePlayers = nil
	lc.write.Reset()
	mc.write.Reset()
	// No town transition or reconnect occurs here. The just-admitted member
	// must be movable by the same leader immediately.
	if err := s.handleGameCommand(
		leader,
		1,
		uint16(dnfenum.CmdPacketRaidManagerWork),
		buildRaidManagerWorkRequestForBridgeTest(0, uint32(member.selectedCharacterID), 2),
	); err != nil {
		t.Fatal(err)
	}
	after, _ := s.runtimeRaidForMember(member.selectedCharacterID)
	row, found := runtimeRaidMemberForTest(after, member.selectedCharacterID)
	if !found || row.GroupIndex != 2 || !row.RecruitingPlacementExplicit || row.OrdinaryPartySolo || after.Started || after.Battle != nil {
		t.Fatalf("pre-start manager placement=%+v started=%t battle=%t", row, after.Started, after.Battle != nil)
	}
	for _, conn := range []*bufferConn{lc, mc} {
		members, rest := splitRaidRefreshPacket(t, conn.write.Bytes(), state.RaidKey)
		if len(rest) != 0 {
			t.Fatal("manager placement emitted an unexpected second packet")
		}
		assertRaidRefreshMember(t, members, member.selectedCharacterID, 2, row.SlotOrder)
	}
}
