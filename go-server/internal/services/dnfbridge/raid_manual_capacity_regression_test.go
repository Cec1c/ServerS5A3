package dnfbridge

import (
	"reflect"
	"sync"
	"testing"
)

func raidManualCapacityFixture() (*Service, *runtimeRaidState) {
	state := &runtimeRaidState{RaidKey: 1, LeaderID: 1, CurrentLayout: true}
	for id := uint16(1); id <= 6; id++ {
		group, slot := byte(1), byte(id-1)
		if id > 4 {
			group, slot = 2, byte(id-5)
		}
		state.Members = append(state.Members, runtimeRaidMemberState{CharID: id, GroupIndex: group, SlotOrder: slot, RecruitingPlacementExplicit: true})
	}
	return &Service{raids: map[uint32]*runtimeRaidState{1: state}}, state
}

func TestRaidManualFormationRejectsFifthMemberWithoutMutation(t *testing.T) {
	s, state := raidManualCapacityFixture()
	before := cloneRuntimeRaidState(*state)
	if _, ok, changed := s.moveRuntimeRaidMember(1, 5, 1); ok || changed {
		t.Fatal("manual formation accepted a fifth member")
	}
	if !reflect.DeepEqual(before, *state) {
		t.Fatal("rejected formation changed the roster, slots or explicit placement")
	}
	if _, ok, _ := s.moveRuntimeRaidMember(1, 3, 1); !ok {
		t.Fatal("same member could not retain its seat in a full group")
	}
	if _, ok, _ := s.moveRuntimeRaidMember(1, 4, 2); !ok {
		t.Fatal("moving out of full group failed")
	}
	if _, ok, _ := s.moveRuntimeRaidMember(1, 5, 1); !ok {
		t.Fatal("freed fourth seat could not be filled")
	}
	used := map[byte]bool{}
	for _, m := range state.Members {
		if m.GroupIndex == 1 {
			if m.SlotOrder >= 4 || used[m.SlotOrder] {
				t.Fatal("accepted formation has out-of-range or duplicate slot", m)
			}
			used[m.SlotOrder] = true
		}
	}
	if len(used) != 4 {
		t.Fatal("expected four distinct seats", used)
	}
}

func TestRaidManualFormationConcurrentFourthSeatHasOneWinner(t *testing.T) {
	s, state := raidManualCapacityFixture()
	state.Members[3].GroupIndex, state.Members[3].SlotOrder = 3, 0
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for _, id := range []uint16{5, 6} {
		wg.Add(1)
		go func() { defer wg.Done(); _, ok, _ := s.moveRuntimeRaidMember(1, id, 1); results <- ok }()
	}
	wg.Wait()
	close(results)
	winners := 0
	for ok := range results {
		if ok {
			winners++
		}
	}
	if winners != 1 || runtimeRaidGroupAssignedCount(*state, 1, 0) != 4 {
		t.Fatal("concurrent formation overfilled the final seat", winners, state.Members)
	}
}
