package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestPassiveItemPickupCommitsOnceAndRemovesNativeWireKey(t *testing.T) {
	service, runtime, repositories := prepareCurrentDungeonDropTest(t, 0, 10158700)
	doc, err := dnfpvf.Parse("test.dgn", "[special passive object item]\n0 -1 1 10158700 10000\n")
	if err != nil {
		t.Fatal(err)
	}
	runtime.Dungeon.SourceSections = worldmap.ParseDungeon(1, doc.Path, doc).SourceSections
	scene, _ := runtime.Session.Scene()
	scene.SpecialPassiveObjects = []worldmap.SpecialPassiveObject{{Spawns: []worldmap.SpecialObjectSpawn{{Kind: "[item]", Code: 0, Level: 1, Params: [3]int64{-1, -1, -1}}}}}
	connection := &bufferConn{}
	session := &gameSession{conn: connection, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
	if err := service.prepareCurrentDungeonPassiveObjectDrops(session, runtime, scene, "test"); err != nil {
		t.Fatal(err)
	}
	rows, _ := currentDungeonPassiveObjectDropEntries(runtime, scene)
	if len(rows) != 1 {
		t.Fatalf("rows=%+v", rows)
	}
	key := rows[0].GlobalSequence
	for n := 0; n < 2; n++ {
		connection.write.Reset()
		if err := service.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(key)); err != nil {
			t.Fatal(err)
		}
		packet, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
		if packet.Header.MsgID != currentDungeonPickupResultOpcode || binary.LittleEndian.Uint32(packet.Body[:4]) != key {
			t.Fatalf("pickup must remove the ID sent in START_MAP: %+v %x", packet.Header, packet.Body)
		}
	}
	inventory, found, err := repositories.Inventory.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatal(err)
	}
	assertCurrentNPCShopInventoryAmount(t, inventory, 10158700, 1)
}

func TestPassiveItemPlanHonorsPVFChildren(t *testing.T) {
	doc, err := dnfpvf.Parse("test.dgn", "[special passive object item]\n0 -1 2 101 0 102 10000\n")
	if err != nil {
		t.Fatal(err)
	}
	dungeon := worldmap.ParseDungeon(1, "test.dgn", doc)
	scene := worldmap.DungeonRoomScene{SpecialPassiveObjects: []worldmap.SpecialPassiveObject{
		{Spawns: []worldmap.SpecialObjectSpawn{{Kind: "[item]", Code: 0, Level: 2, Params: [3]int64{-1, -1, -1}}}},
		{HellParty: []worldmap.HellPartyEntry{{GroupID: 1}}},
	}}
	plan, err := currentDungeonPVFPassiveItemPlan(dungeon, scene, 123)
	if err != nil || len(plan) != 2 || plan[0].itemID != 102 || plan[1].itemID != 102 || plan[1].objectIndex != 0 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	scene.SpecialPassiveObjects[0].Spawns = nil
	plan, err = currentDungeonPVFPassiveItemPlan(dungeon, scene, 123)
	if err != nil || len(plan) != 0 {
		t.Fatalf("an object without [item] must not drop: %+v %v", plan, err)
	}
}

func TestRealPVFTimePlazaPassiveDropsAreIndependent(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parseDungeonCardPVFDocument(archive, "dungeon/metrocenter/time_plaza/time_plaza.dgn")
	if err != nil {
		t.Fatal(err)
	}
	dungeon := worldmap.ParseDungeon(1, doc.Path, doc)
	mapDoc, err := parseDungeonCardPVFDocument(archive, "map/MetroCenter/Time_Plaza/36765.map")
	if err != nil {
		t.Fatal(err)
	}
	mapValue := worldmap.ParseMap(36765, mapDoc.Path, mapDoc)
	scene := worldmap.DungeonRoomScene{SpecialPassiveObjects: mapValue.SpecialPassiveObjects}
	scene.Map.Map = mapValue
	catalog, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{dungeonMonsterTable: catalog}
	owner := newRuntimeDungeonDropOwner()
	var runtimes []*runtimeDungeonState
	for _, id := range []uint16{19, 20} {
		runtime := &runtimeDungeonState{Dungeon: dungeon, Seed: 123, NextObjectKey: 401, DropOwner: owner}
		if id == 20 {
			runtime.NextObjectKey = 405
		}
		if err := service.prepareCurrentDungeonPassiveObjectDrops(&gameSession{selectedCharacterID: id}, runtime, scene, "test"); err != nil {
			t.Fatal(err)
		}
		rows, vendor := currentDungeonPassiveObjectDropEntries(runtime, scene)
		if len(rows) != 4 || vendor {
			t.Fatalf("rows=%+v vendor=%v", rows, vendor)
		}
		for index, row := range rows {
			if int(row.PassiveObjectIndex) != index || row.ItemID != 10158700 || row.StackCount != 1 {
				t.Fatalf("row=%+v", row)
			}
			drop, err := owner.owned(row.GlobalSequence, runtimeDungeonRoomKeyFromScene(scene), currentSceneActorObjectKey(id))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := owner.ownedMapOwnedPassiveObjectBySceneSlot(drop.SceneSlot, drop.Room, currentSceneActorObjectKey(id+1)); err == nil {
				t.Fatal("teammate could pick up this member's stone")
			}
		}
		before := owner.nextSceneSlot
		if err := service.prepareCurrentDungeonPassiveObjectDrops(&gameSession{selectedCharacterID: id}, runtime, scene, "rebuild"); err != nil {
			t.Fatal(err)
		}
		if owner.nextSceneSlot != before {
			t.Fatal("rebuilding map minted duplicate rewards")
		}
		packet := liveHellRoomStartMapPacket()
		packet.ExtraEntries = rows
		body, err := packet.Build()
		if err != nil {
			t.Fatal(err)
		}
		offset := 19 + 21*len(packet.Actors)
		if len(body) != 254+4*20 || body[offset] != 4 || binary.LittleEndian.Uint32(body[offset+2:offset+6]) != rows[0].GlobalSequence || binary.LittleEndian.Uint32(body[offset+6:offset+10]) != 10158700 {
			t.Fatal("current EXE 20-byte extra row layout mismatch")
		}
		runtimes = append(runtimes, runtime)
	}
	if len(owner.bySceneSlot) != 8 {
		t.Fatalf("shared ledger has %d drops", len(owner.bySceneSlot))
	}
	first, _ := currentDungeonPassiveObjectDropEntries(runtimes[0], scene)
	second, _ := currentDungeonPassiveObjectDropEntries(runtimes[1], scene)
	if first[0].GlobalSequence == second[0].GlobalSequence {
		t.Fatal("members shared a reward")
	}
	owner.byObjectKey[first[0].GlobalSequence].Status = runtimeDungeonDropConsumed
	remaining, _ := currentDungeonPassiveObjectDropEntries(runtimes[0], scene)
	if len(remaining) != 3 {
		t.Fatal("a picked up stone reappeared on room reentry")
	}
}
