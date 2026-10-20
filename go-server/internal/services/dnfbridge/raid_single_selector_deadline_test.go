package dnfbridge

import (
	"encoding/binary"
	"testing"
)

// This uses actual admission configuration and frozen single resources. The
// fixture's completed maps are not forged; malformed states below are explicit
// negative model controls. Public666/16/37/deadline/15 is separately unchanged.
func TestRealPVFSingleAntonDeadlineSelectorExactClosedOwner(t *testing.T) {
	f, _ := realSingleAntonHPFixture(t, 0)
	b := f.state.Battle
	active := cloneRuntimeRaidBattle(b)
	states, errs := f.service.advanceCurrentRaidClocks(b.Deadline)
	if len(errs) != 0 || len(states) != 1 || !b.TimedOut || b.PhaseCompleted {
		t.Fatalf("actual clock failure owner: states%d errs%v", len(states), errs)
	}
	for _, row := range b.Dungeons {
		if row.State != 2 {
			t.Fatalf("clock native gate not closed: %+v", row)
		}
	}
	body, err := currentRaidDungeonProjectionBody(b, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 2+int(body[1])*5+4 {
		t.Fatal("585 row boundary")
	}
	for i := 0; i < int(body[1]); i++ {
		off := 2 + i*5
		if body[off+4] != 2 {
			t.Fatalf("timeout row%d id%d reopened", i, binary.LittleEndian.Uint32(body[off:]))
		}
	}
	// Same mapping, HP journal and active gate validation are still authoritative.
	if _, err := currentRaidDungeonProjectionBody(active, 0); err != nil {
		t.Fatal("valid active mapping rejected", err)
	}
	for _, name := range []string{"timeout_current_open", "timeout_future_one", "completed_timeout", "completed_index_timeout", "missing_row", "wrong_native_order", "duplicate_alias", "hp_journal", "active_current_closed", "active_future_closed"} {
		t.Run(name, func(t *testing.T) {
			candidate := cloneRuntimeRaidBattle(b)
			switch name {
			case "timeout_current_open":
				candidate.Dungeons[0].State = 0
			case "timeout_future_one":
				candidate.Dungeons[1].State = 1
			case "completed_timeout":
				candidate.PhaseCompleted = true
			case "completed_index_timeout":
				candidate.SingleIndex = len(candidate.SingleSequence)
			case "missing_row":
				candidate.Dungeons = candidate.Dungeons[:len(candidate.Dungeons)-1]
			case "wrong_native_order":
				candidate.Dungeons[0].ID = candidate.Dungeons[1].ID
			case "duplicate_alias":
				candidate.SingleSequence[1] = candidate.SingleSequence[0]
				candidate.Dungeons[1].ID = candidate.SingleSequence[0]
			case "hp_journal":
				for id := range candidate.MemberAccounts {
					candidate.MemberClears[id]++
				}
			case "active_current_closed":
				candidate = cloneRuntimeRaidBattle(active)
				candidate.Dungeons[0].State = 2
			case "active_future_closed":
				candidate = cloneRuntimeRaidBattle(active)
				candidate.Dungeons[1].State = 2
			}
			if _, err := currentRaidDungeonProjectionBody(candidate, 0); err == nil {
				t.Fatal("malformed owner admitted", name)
			}
		})
	}
}
