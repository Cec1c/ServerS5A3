package dnfbridge

import "testing"

func TestSingleAntonGroundPolicySurvivesBattleRetirement(t *testing.T) {
	for _, drift := range []string{"live", "retired", "phase"} {
		t.Run(drift, func(t *testing.T) {
			f := review452GroundRun(t, "anton", true, false)
			monster := review452PositiveMonsterRules(t, f.service, f.runtime)
			scene, _ := f.runtime.Session.Scene()
			visit, err := f.runtime.currentDungeonRoomVisit(scene)
			if err != nil {
				t.Fatal(err)
			}
			seed, key := visit.DropRNG, f.runtime.NextObjectKey
			if drift == "retired" {
				f.service.raids = nil
			}
			if drift == "phase" {
				f.battle.Phase++
				f.battle.PhaseCompleted = true
			}
			wires, err := f.service.planCurrentDungeonMonsterDrops(f.runtime, monster, currentSceneActorObjectKey(19))
			if err != nil {
				t.Fatal(err)
			}
			if len(wires) != 0 || review452GroundCount(f.runtime) != 0 || visit.DropRNG != seed || f.runtime.NextObjectKey != key {
				t.Fatalf("single Anton generated floor loot: wires=%d ledger=%d", len(wires), review452GroundCount(f.runtime))
			}
		})
	}
}
