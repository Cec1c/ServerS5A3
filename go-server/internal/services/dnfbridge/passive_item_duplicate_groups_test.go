package dnfbridge

import (
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestPassiveItemPlanRepeatedDefinitionsDoNotMultiplyRewards(t *testing.T) {
	const group = "[special passive object item]\n0 -1 2 101 0 102 10000\n[/special passive object item]\n"
	for _, suffix := range []string{group, "[maze info]\n" + group} {
		doc, err := dnfpvf.Parse("test.dgn", group+suffix)
		if err != nil {
			t.Fatal(err)
		}
		dungeon := worldmap.ParseDungeon(1, doc.Path, doc)
		scene := worldmap.DungeonRoomScene{SpecialPassiveObjects: []worldmap.SpecialPassiveObject{{Spawns: []worldmap.SpecialObjectSpawn{{Kind: "[item]", Code: 0, Level: 2}}}}}
		plan, err := currentDungeonPVFPassiveItemPlan(dungeon, scene, 123)
		if err != nil || len(plan) != 2 || plan[0].itemID != 102 || plan[1].itemID != 102 {
			t.Fatalf("repeated identical definition: plan=%+v err=%v", plan, err)
		}
	}
}

func TestPassiveItemPlanConflictingRepeatedDefinitionFailsClosed(t *testing.T) {
	const prefix = "[special passive object item]\n0 -1 1 102 10000\n[/special passive object item]\n[special passive object item]\n"
	for _, tail := range []string{"0 -1 1 103 10000", "0 -1 1 102 5000", "0 2 1 102 10000", "0 -1 2 102 10000", "0 -1 1 102 -1"} {
		doc, err := dnfpvf.Parse("test.dgn", prefix+tail)
		if err != nil {
			t.Fatal(err)
		}
		scene := worldmap.DungeonRoomScene{SpecialPassiveObjects: []worldmap.SpecialPassiveObject{{Spawns: []worldmap.SpecialObjectSpawn{{Kind: "[item]", Code: 0, Level: 1}}}}}
		plan, err := currentDungeonPVFPassiveItemPlan(worldmap.ParseDungeon(1, doc.Path, doc), scene, 123)
		if err == nil || len(plan) != 0 {
			t.Fatalf("ambiguous/malformed definition accepted: %s %+v", tail, plan)
		}
	}
}

func TestPassiveItemDropsRespectPlannedActorsAndSharedAllocator(t *testing.T) {
	for _, sharedNext := range []uint32{0, 450, 900} {
		service, runtime, _ := prepareCurrentDungeonDropTest(t, 0, 10158700)
		doc, err := dnfpvf.Parse("test.dgn", "[special passive object item]\n0 -1 1 10158700 10000\n")
		if err != nil {
			t.Fatal(err)
		}
		runtime.Dungeon.SourceSections = worldmap.ParseDungeon(1, doc.Path, doc).SourceSections
		runtime.NextObjectKey = 401
		if sharedNext != 0 {
			runtime.partyInstance = &dungeonPartyInstance{nextObjectKey: sharedNext, dropOwner: runtime.DropOwner}
		}
		scene, _ := runtime.Session.Scene()
		scene.SpecialPassiveObjects = []worldmap.SpecialPassiveObject{{Spawns: []worldmap.SpecialObjectSpawn{{Kind: "[item]", Code: 0, Level: 2}}}}
		session := &gameSession{selectedCharacterID: 99}
		if err := service.prepareCurrentDungeonPassiveObjectDropsAfterActors(session, runtime, scene, "planned_target", 500); err != nil {
			t.Fatal(err)
		}
		rows, _ := currentDungeonPassiveObjectDropEntries(runtime, scene)
		want := max(uint32(500), sharedNext)
		if len(rows) != 2 || rows[0].GlobalSequence != want || rows[1].GlobalSequence != want+1 || runtime.NextObjectKey != want+2 {
			t.Fatalf("sharedNext=%d rows=%+v next=%d", sharedNext, rows, runtime.NextObjectKey)
		}
		if sharedNext != 0 && runtime.partyInstance.nextObjectKey != want+2 {
			t.Fatal("shared allocator lost reservations")
		}
	}
}
