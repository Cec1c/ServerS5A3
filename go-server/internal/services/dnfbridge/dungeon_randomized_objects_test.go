package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func actualAntonRandomizedBridgeFixture(t *testing.T) (*Service, worldmap.Dungeon, *worldmap.DungeonTopology, *worldmap.DungeonSession, *runtimeDungeonRoom) {
	t.Helper()
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("requires actual formal PVF")
	}
	a, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	dungeon, ok := table.FindDungeon(218)
	if !ok {
		t.Fatal("actual energy source missing")
	}
	topology, err := worldmap.BuildDungeonLayout(resolver, 218, 0, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
	if err != nil {
		t.Fatal(err)
	}
	run, err := worldmap.NewDungeonRun(topology)
	if err != nil {
		t.Fatal(err)
	}
	session, err := worldmap.NewDungeonSession(run)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{initialEquipmentArchive: a, dungeonMonsterTable: monsters, dungeonChoice: func(int) (int, error) { return 0, nil }}
	scene, _ := session.Scene()
	room, next, err := newRuntimeDungeonRoom(scene, monsters, firstDungeonMonsterObjectKey)
	if err != nil {
		t.Fatal(err)
	}
	var ai *pvfDungeonAICharacterCatalog
	if len(scene.AICharacters) > 0 {
		ai, err = newPVFDungeonAICharacterCatalog(a)
		if err != nil {
			t.Fatal(err)
		}
	}
	actors, err := planRuntimeDungeonExtendedActors(scene, monsters, ai, dungeon.Metadata.BasisLevel, next)
	if err != nil {
		t.Fatal(err)
	}
	if err := room.AttachExtendedActors(actors); err != nil {
		t.Fatal(err)
	}
	return s, dungeon, topology, session, room
}

func TestRealPVFAntonRandomizedOnlinePreparationAndCurrentEntryPackets(t *testing.T) {
	s, dungeon, topology, session, room := actualAntonRandomizedBridgeFixture(t)
	owner, err := s.prepareCurrentDungeonRandomizedOwner(dungeon, 0, topology, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(owner.Entries) != 6 {
		t.Fatal("actual random object count")
	}
	for _, entry := range owner.Entries {
		if entry.Faction != 100 || (entry.Source.ObjectID == 18865 && entry.SpawnMode != 1) || (entry.Source.ObjectID == 58530 && entry.SpawnMode != 0) {
			t.Fatal("PVF namespaces and faction", entry)
		}
	}
	s.dungeonChoice = func(int) (int, error) { t.Fatal("follower rerandomized instead of using leader choice"); return 0, nil }
	follower, err := s.prepareCurrentDungeonRandomizedOwner(dungeon, 0, topology, owner)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(owner.Entries, follower.Entries) {
		t.Fatal("follower selection differs")
	}
	follower.Entries[0].Faction = 0
	if owner.Entries[0].Faction != 100 {
		t.Fatal("follower mutable data aliases leader")
	}
	if _, err := s.prepareCurrentDungeonRandomizedOwner(dungeon, 0, topology, follower); err == nil {
		t.Fatal("modified frozen faction reused")
	}
	for _, field := range []string{"code", "mode", "room", "count", "plan"} {
		corrupt := owner.clone()
		switch field {
		case "code":
			corrupt.Entries[0].Source.ObjectID++
		case "mode":
			corrupt.Entries[0].SpawnMode = 0
		case "room":
			corrupt.Entries[0].Source.Map.X++
		case "count":
			corrupt.Entries = corrupt.Entries[:1]
		case "plan":
			corrupt.Plan = nil
		}
		if _, err := s.prepareCurrentDungeonRandomizedOwner(dungeon, 0, topology, corrupt); err == nil {
			t.Fatal("modified frozen owner reused", field)
		}
	}
	scene, _ := session.Scene()
	runtime := &runtimeDungeonState{Request: dungeoncmd.SelectDungeonRequest{DungeonID: 218}, Dungeon: dungeon, MazeIndex: 0, Session: session, Room: room, BossCoordinate: topology.Bosses[0], BossSet: true, Seed: 17834, randomizedObjects: owner}
	packets, err := buildCurrentDungeonEntryPackets(runtime, scene)
	if err != nil {
		t.Fatal("actual energy entry no longer blocked by randomized object owner", err)
	}
	groups := owner.minimapGroups()
	if len(groups) != 2 || len(groups[0]) != 5 || len(groups[1]) != 1 || packets.DungeonInfo[13] != 2 {
		t.Fatal("actual minimap groups", groups)
	}
	if rows := owner.roomGroups(scene.Coordinate); len(rows) != 0 {
		t.Fatal("energy start room received objects from other rooms")
	}
	for _, entry := range owner.Entries {
		rows := owner.roomGroups(entry.Source.Map)
		if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0].ObjectCode != uint32(entry.Source.ObjectID) {
			t.Fatal("randomized room ownership", entry, rows)
		}
		body, err := (currentDungeonStartMap{X: byte(entry.Source.Map.X), Y: byte(entry.Source.Map.Y), MapID: 1, RidableGroups: rows, PartyMemberIndex: 0xff}).Build()
		if err != nil {
			t.Fatal(err)
		}
		if len(body) != 44 || body[21] != 1 || body[22] != 1 || binary.LittleEndian.Uint32(body[31:35]) != uint32(entry.Source.ObjectID) {
			t.Fatal("current five-u32 row", body)
		}
	}
	if dir := os.Getenv("DNF90_ANTON_NATIVE_DIR"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, "go-anton-energy-randomized-info.bin"), packets.DungeonInfo, 0o644); err != nil {
			t.Fatal(err)
		}
		for _, entry := range owner.Entries {
			label := "passive"
			if entry.SpawnMode == 0 {
				label = "monster"
			}
			body, _ := (currentDungeonStartMap{X: byte(entry.Source.Map.X), Y: byte(entry.Source.Map.Y), MapID: 19100, RidableGroups: owner.roomGroups(entry.Source.Map), PartyMemberIndex: 0xff}).Build()
			if err := os.WriteFile(filepath.Join(dir, "go-anton-energy-randomized-"+label+".bin"), body, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
