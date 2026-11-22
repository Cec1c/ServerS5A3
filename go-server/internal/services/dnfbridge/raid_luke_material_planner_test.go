package dnfbridge

import (
	"encoding/binary"
	"strconv"
	"testing"
)

// Exercise the production planner and registered drop ledger, rather than
// only testing the filter predicate. The raid identity comes from the real
// Luke fixture; deterministic synthetic drop rules are test inputs only.
func TestRealPVFLukeMaterialPlannerFiltersBeforeRegisteringDrops(t *testing.T) {
	f, _, _ := normalLukeFixture(t)
	b := f.state.Battle
	for _, mode := range []string{"ordinary", "multiplayer", "single", "stale_attempt"} {
		t.Run(mode, func(t *testing.T) {
			s, r, _ := prepareCurrentDungeonDropTest(t, 0, 3227)
			s.raids = f.service.raids
			oldSingle := b.Single
			defer func() { b.Single = oldSingle }()
			if mode != "ordinary" {
				r.raidEntry = &currentRaidDungeonEntry{RaidKey: f.state.RaidKey, InstanceID: b.InstanceID, Phase: b.Phase, Attempt: b.Anton.Epoch(), ChannelID: b.ChannelID, ChannelType: 32, RunID: "planner-test"}
			}
			if mode == "single" {
				b.Single = true
			}
			if mode == "stale_attempt" {
				r.raidEntry.Attempt++
			}
			source := s.dungeonMonsterTable.source.(bridgePVFSource)
			source[dungeonCardDropRulePath] = currentDungeonGoldDropTestRuleText()
			source[dungeonCardGoldReferencePath] = "[gold drop ref table]\n20 100 0\n"
			monster := r.Room.Snapshot().Monsters[0]
			source[currentDungeonIndependentDropPath] = "[independent drop]\n0 " + strconv.FormatInt(monster.Spawn.MonsterID, 10) + " 3227 1000000 1000000 0 0 0 1 1 0 0 0 1 90 -1 0\n[/independent drop]\n"
			currentDungeonMonsterDropRulesByCatalog.Delete(s.dungeonMonsterTable)
			currentDungeonSpecialDropsByMonsterCatalog.Delete(s.dungeonMonsterTable)
			wires, err := s.planCurrentDungeonMonsterDrops(r, monster, currentSceneActorObjectKey(99))
			if err != nil {
				t.Fatal(err)
			}
			material, gold := 0, 0
			for _, wire := range wires {
				id := binary.LittleEndian.Uint32(wire.Item.data[2:6])
				if id == 3227 {
					material++
				}
				if id == 0 {
					gold++
				}
				if r.DropOwner == nil || r.DropOwner.byObjectKey[wire.SceneObjectKey] == nil {
					t.Fatal("wire lacks registered owner")
				}
			}
			if gold == 0 {
				t.Fatal("material suppression removed gold")
			}
			if mode == "multiplayer" {
				if material != 0 {
					t.Fatal("multiplayer planner emitted material")
				}
				for _, drop := range r.DropOwner.byObjectKey {
					if drop.Item.ItemID == 3227 {
						t.Fatal("suppressed material retained a pickup owner")
					}
				}
			} else if material == 0 {
				t.Fatal("control planner lost guaranteed material", mode)
			}
		})
	}
}
