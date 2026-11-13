package dnfbridge

import (
	"context"
	"testing"
	"time"
)

func TestPartyLoadingPreparationWaitsForSlowPeerAndSurvivesDuplicate(t *testing.T) {
	for _, leave := range []bool{false, true} {
		t.Run(map[bool]string{false: "slow", true: "departure"}[leave], func(t *testing.T) {
			s, a, b, ac, bc, _ := partyDungeonSyncFixture(t)
			runPartyDungeonSyncEntry(t, s, a)
			partyID, members, ok := s.partyDungeonLoadingBarrierMembers(a)
			if !ok {
				t.Fatal("no barrier")
			}
			s.partyDungeonLoadingBarrierArrive(a, partyID, members)
			_, _, _, _, cycle := s.partyDungeonLoadingBarrierArrive(b, partyID, members)
			plans := map[uint16]*partyLoadingPreparation{}
			for id, owner := range cycle.owners {
				plans[id] = &partyLoadingPreparation{owner: owner, wantTail: true}
			}
			cycle.preparing = plans
			t.Cleanup(func() {
				if cycle.Timer != nil {
					cycle.Timer.Stop()
				}
			})
			s.releasePartyDungeonLoadingMemberPhase(partyID, cycle, a.selectedCharacterID, plans[a.selectedCharacterID].owner, true, "first_ready", 0)
			if partyDungeonSyncConnHasPlacement(t, ac) || partyDungeonSyncConnHasPlacement(t, bc) {
				t.Fatal("activation preceded slow peer preparation")
			}
			if a.partyLoadingPrepared != nil {
				t.Fatal("preparation pointer escaped session callback")
			}
			cycle.released = time.Now().Add(-time.Hour)
			_, first, _, _, duplicate := s.partyDungeonLoadingBarrierArrive(a, partyID, members)
			if duplicate != cycle || first {
				t.Fatal("duplicate replaced a slow preparation cycle")
			}
			if leave {
				c, cancel := context.WithCancel(context.Background())
				cancel()
				b.ctx = c
				s.checkPartyLoadingPreparation(partyID, cycle, "peer_disconnected")
			} else {
				s.releasePartyDungeonLoadingMemberPhase(partyID, cycle, b.selectedCharacterID, plans[b.selectedCharacterID].owner, true, "last_prepared", 0)
			}
			if !partyDungeonSyncConnHasPlacement(t, ac) {
				t.Fatal("prepared survivor not released")
			}
			if partyDungeonSyncConnHasPlacement(t, bc) == leave {
				t.Fatal("peer release did not match liveness")
			}
			if a.partyLoadingPrepared != nil || b.partyLoadingPrepared != nil {
				t.Fatal("activation retained preparation pointer")
			}
			before := len(ac.frames())
			s.releasePartyDungeonLoadingMemberPhase(partyID, cycle, a.selectedCharacterID, plans[a.selectedCharacterID].owner, true, "duplicate_prepare", 0)
			if len(ac.frames()) != before {
				t.Fatal("duplicate preparation emitted activation twice")
			}
		})
	}
}
