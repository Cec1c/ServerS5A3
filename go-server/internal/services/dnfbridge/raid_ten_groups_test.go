package dnfbridge

import (
	"reflect"
	"testing"
)

func TestAntonNativeFormationGroupsSixThroughTen(t *testing.T) {
	f := newR195SoloPrefixFixture(t)
	raidManualClearCommand(t, f, f.leader, 0, uint32(f.leaderID), 1)
	for group := uint32(6); group <= 10; group++ {
		f.leaderConn.write.Reset()
		f.memberConn.write.Reset()
		raidManualClearCommand(t, f, f.leader, 0, uint32(f.memberID), group)
		state := f.state(t)
		member := raidStartIntentMemberForTest(t, state, f.memberID)
		if member.GroupIndex != byte(group) || member.SlotOrder != 0 {
			t.Fatal(group, member)
		}
		f.assertRosters(t, f.leaderConn.write.Bytes(), map[uint16][2]byte{f.leaderID: {1, 0}, f.memberID: {byte(group), 0}})
		f.assertRosters(t, f.memberConn.write.Bytes(), map[uint16][2]byte{f.leaderID: {1, 0}, f.memberID: {byte(group), 0}})
		planned, err := f.service.prepareCurrentRaidStartPartyIntent(state)
		if err != nil {
			t.Fatal(group, err)
		}
		if raidStartIntentMemberForTest(t, planned, f.memberID).GroupIndex != byte(group) {
			t.Fatal("start rewrote selected group")
		}
	}
	before := f.state(t)
	for _, group := range []uint32{11, 256, 266} {
		raidManualClearCommand(t, f, f.leader, 0, uint32(f.memberID), group)
		if !reflect.DeepEqual(before, f.state(t)) {
			t.Fatal("invalid group aliased", group)
		}
	}
	if currentRaidMemberMaximum(before) != 20 || currentRaidGroupMaximum(runtimeRaidState{RouteOrRaidType: 1}) != 5 || currentRaidGroupMaximum(runtimeRaidState{RouteOrRaidType: 2}) != 8 {
		t.Fatal("changed other raid/member limits")
	}
}
