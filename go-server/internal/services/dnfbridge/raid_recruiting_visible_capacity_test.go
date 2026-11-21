package dnfbridge

import (
	"reflect"
	"testing"
)

func TestRecruitingRaidVisibleVacancyIgnoresUneditedAdmissionSeats(t *testing.T) {
	for _, raidType := range []byte{0, 1} {
		for _, occupied := range []int{0, 1, 3, 4} {
			t.Run(fmtRaidVacancyCase(raidType, occupied), func(t *testing.T) {
				f := newR195SoloPrefixFixture(t)
				f.service.raidMu.Lock()
				live := f.service.raids[f.raidKey]
				live.RouteOrRaidType = raidType
				// Admission reserves four raw positions, but only explicit seats
				// occupy the recruiting panel when no ordinary party exists.
				for i := range live.Members {
					live.Members[i].GroupIndex = 2
					live.Members[i].OrdinaryPartySolo = false
				}
				for slot := byte(0); slot < 4; slot++ {
					live.Members = append(live.Members, runtimeRaidMemberState{
						CharID: 100 + uint16(slot), GroupIndex: 1, SlotOrder: slot,
						RecruitingPlacementExplicit: int(slot) < occupied,
					})
				}
				f.service.raidMu.Unlock()
				before := f.state(t)
				visible := f.service.currentRaidLivePartyProjection(before)
				if got := runtimeRaidGroupAssignedCount(visible, 1, f.memberID); got != occupied {
					t.Fatalf("fixture visible seats=%d want=%d", got, occupied)
				}
				f.leaderConn.write.Reset()
				f.memberConn.write.Reset()
				if err := f.service.handleGameCommand(f.leader, 1, 669, buildRaidManagerWorkRequestForBridgeTest(0, uint32(f.memberID), 1)); err != nil {
					t.Fatal(err)
				}
				after := f.state(t)
				if occupied == 4 {
					if !reflect.DeepEqual(before, after) {
						t.Fatal("full group changed")
					}
					return
				}
				m := mustR195SoloPrefixMember(t, after, f.memberID)
				if m.GroupIndex != 1 || m.SlotOrder != byte(occupied) || !m.RecruitingPlacementExplicit {
					t.Fatalf("visible free seat rejected or occupied by hidden admission: %+v", m)
				}
				want := map[uint16][2]byte{f.leaderID: {0, 0}, f.memberID: {1, byte(occupied)}}
				for slot := byte(0); slot < 4; slot++ {
					want[100+uint16(slot)] = [2]byte{0, 0}
					if int(slot) < occupied {
						want[100+uint16(slot)] = [2]byte{1, slot}
					}
				}
				f.assertRosters(t, f.leaderConn.write.Bytes(), want)
				f.assertRosters(t, f.memberConn.write.Bytes(), want)
				// The same accepted roster must also be a valid start plan.
				planned, err := f.service.prepareCurrentRaidStartPartyIntent(after)
				if err != nil {
					t.Fatal(err)
				}
				if runtimeRaidGroupAssignedCount(planned, 1, 0) != occupied+1 {
					t.Fatal("start plan changed visible occupancy")
				}
			})
		}
	}
}

func fmtRaidVacancyCase(raidType byte, occupied int) string {
	return []string{"anton", "luke"}[raidType] + "/occupied_" + string(rune('0'+occupied))
}

func TestRecruitingRaidVacancyCannotBeUsedByNonLeader(t *testing.T) {
	f := newR195SoloPrefixFixture(t)
	before := f.state(t)
	if _, ok, _ := f.service.moveRuntimeRaidMember(f.memberID, f.memberID, 2); ok {
		t.Fatal("non-leader moved a member")
	}
	if !reflect.DeepEqual(before, f.state(t)) {
		t.Fatal("unauthorized move changed state")
	}
}
