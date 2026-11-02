package dnfbridge

import (
	"fmt"
	"reflect"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func passiveProbabilityFixture(t *testing.T, group string, draws int64) (worldmap.Dungeon, worldmap.DungeonRoomScene) {
	t.Helper()
	doc, err := dnfpvf.Parse("probability.dgn", "[special passive object item]\n"+group+"\n")
	if err != nil {
		t.Fatal(err)
	}
	scene := worldmap.DungeonRoomScene{SpecialPassiveObjects: []worldmap.SpecialPassiveObject{{Spawns: []worldmap.SpecialObjectSpawn{{Kind: "[item]", Code: 0, Level: draws}}}}}
	return worldmap.ParseDungeon(1, doc.Path, doc), scene
}

func TestPassiveItemProbabilityKeepsEmptyOutcome(t *testing.T) {
	for _, probability := range []int{0, 1500, 3000, 10000} {
		t.Run(fmt.Sprint(probability), func(t *testing.T) {
			dungeon, scene := passiveProbabilityFixture(t, fmt.Sprintf("0 -1 1 3341 %d", probability), 1)
			count := 0
			for seed := uint32(0); seed < 10000; seed++ {
				plan, err := currentDungeonPVFPassiveItemPlan(dungeon, scene, seed)
				if err != nil {
					t.Fatal(err)
				}
				if len(plan) > 1 {
					t.Fatalf("one draw created %d items", len(plan))
				}
				if len(plan) == 1 {
					if plan[0].itemID != 3341 || plan[0].objectIndex != 0 {
						t.Fatal(plan)
					}
					count++
				}
			}
			if probability == 0 || probability == 10000 {
				if count != probability {
					t.Fatalf("rate=%d drops=%d/10000", probability, count)
				}
			} else if count < probability-200 || count > probability+200 {
				t.Fatalf("rate=%d drops=%d/10000; missing no-drop probability", probability, count)
			}
			t.Logf("configured=%d/10000 actual=%d/10000", probability, count)
		})
	}
}

func TestPassiveItemProbabilityMixedGroupAndDrawCount(t *testing.T) {
	dungeon, scene := passiveProbabilityFixture(t, "0 -1 2 101 1000 102 2000", 3)
	counts := map[uint32]int{}
	for seed := uint32(0); seed < 5000; seed++ {
		plan, err := currentDungeonPVFPassiveItemPlan(dungeon, scene, seed)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan) > 3 {
			t.Fatal("more items than PVF draws", plan)
		}
		for _, row := range plan {
			counts[row.itemID]++
		}
		again, err := currentDungeonPVFPassiveItemPlan(dungeon, scene, seed)
		if err != nil || !reflect.DeepEqual(plan, again) {
			t.Fatal("same visit rerolled", err)
		}
	}
	if counts[101] < 1300 || counts[101] > 1700 || counts[102] < 2700 || counts[102] > 3300 || len(counts) != 2 {
		t.Fatalf("15000 draws: %+v; unassigned probability must stay empty", counts)
	}
}

func TestPassiveItemProbabilityPreservesOverfullGroup(t *testing.T) {
	// Existing PVFs contain overfull groups (8000+4000). Keep their weighted
	// selection behavior while fixing only groups with unassigned probability.
	dungeon, scene := passiveProbabilityFixture(t, "0 -1 2 101 8000 102 4000", 1)
	counts := map[uint32]int{}
	for seed := uint32(0); seed < 6000; seed++ {
		plan, err := currentDungeonPVFPassiveItemPlan(dungeon, scene, seed)
		if err != nil || len(plan) != 1 {
			t.Fatalf("plan=%v error=%v", plan, err)
		}
		counts[plan[0].itemID]++
	}
	if counts[101] < 3700 || counts[101] > 4300 || counts[102] < 1700 || counts[102] > 2300 {
		t.Fatal(counts)
	}
}

func TestPassiveItemProbabilityEmptyVisitDoesNotReroll(t *testing.T) {
	service, runtime, _ := prepareCurrentDungeonDropTest(t, 0, 3341)
	dungeon, fixture := passiveProbabilityFixture(t, "0 -1 1 3341 1500", 1)
	runtime.Dungeon.SourceSections = dungeon.SourceSections
	scene, _ := runtime.Session.Scene()
	scene.SpecialPassiveObjects = fixture.SpecialPassiveObjects
	session := &gameSession{selectedCharacterID: 99}
	var miss, hit uint32
	foundMiss, foundHit := false, false
	for seed := uint32(0); seed < 100 && (!foundMiss || !foundHit); seed++ {
		plan, err := currentDungeonPVFPassiveItemPlan(runtime.Dungeon, scene, seed^uint32(currentSceneActorObjectKey(99))*0x9e3779b9^uint32(scene.Map.Map.ID))
		if err != nil {
			t.Fatal(err)
		}
		if len(plan) == 0 {
			miss, foundMiss = seed, true
		} else {
			hit, foundHit = seed, true
		}
	}
	if !foundMiss || !foundHit {
		t.Fatal("missing hit/miss fixtures")
	}
	runtime.Seed = miss
	key := runtime.NextObjectKey
	if err := service.prepareCurrentDungeonPassiveObjectDrops(session, runtime, scene, "empty-visit"); err != nil {
		t.Fatal(err)
	}
	rows, _ := currentDungeonPassiveObjectDropEntries(runtime, scene)
	if len(rows) != 0 || runtime.NextObjectKey != key {
		t.Fatal("miss minted drops or consumed IDs", rows)
	}
	runtime.Seed = hit // Another room's seed cannot reroll an already visited room.
	if err := service.prepareCurrentDungeonPassiveObjectDrops(session, runtime, scene, "return-to-empty-room"); err != nil {
		t.Fatal(err)
	}
	rows, _ = currentDungeonPassiveObjectDropEntries(runtime, scene)
	if len(rows) != 0 || runtime.NextObjectKey != key {
		t.Fatal("return rerolled an empty visit", rows)
	}
}
