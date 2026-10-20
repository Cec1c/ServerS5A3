package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRepairedSingleAntonActualAllRoomsBuildCurrentEntryPackets(t *testing.T) {
	name := os.Getenv("DNF90_RAID_REPAIRED_PVF")
	if name == "" {
		t.Skip("requires independently repaired candidate PVF, not a production replacement")
	}
	a, err := platformpvf.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	options, err := raid.ValidateSingleRaidDependenciesForRoute(a, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raid.ValidateSingleRaidDependenciesForRoute(a, 1); err == nil {
		t.Fatal("Anton-only repair claimed Luke assets complete")
	}
	table, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	ai, err := newPVFDungeonAICharacterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{initialEquipmentArchive: a, dungeonMonsterTable: monsters, dungeonChoice: func(int) (int, error) { return 0, nil }}
	count := 0
	for _, id := range options.SupplementalDungeonIDs {
		dungeon, ok := table.FindDungeon(id)
		if !ok {
			t.Fatal("repaired primary dungeon not loaded", id)
		}
		topology, err := worldmap.BuildDungeonLayout(resolver, id, 0, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
		if err != nil {
			t.Fatal(err)
		}
		owner, err := s.prepareCurrentDungeonRandomizedOwner(dungeon, 0, topology, nil)
		if err != nil {
			t.Fatal(id, err)
		}
		for _, realRoom := range topology.Rooms() {
			// Explicit in-memory packet fixture at each real room, rather
			// than claiming an actual client walked the complete route.
			coord := realRoom.Coordinate
			topology.Start = &coord
			run, err := worldmap.NewDungeonRun(topology)
			if err != nil {
				t.Fatal(err)
			}
			ds, err := worldmap.NewDungeonSession(run)
			if err != nil {
				t.Fatal(id, coord, err)
			}
			scene, _ := ds.Scene()
			room, next, err := newRuntimeDungeonRoom(scene, monsters, firstDungeonMonsterObjectKey)
			if err != nil {
				t.Fatal(id, coord, err)
			}
			actors, err := planRuntimeDungeonExtendedActors(scene, monsters, ai, dungeon.Metadata.BasisLevel, next)
			if err != nil {
				t.Fatal(id, coord, err)
			}
			if err := room.AttachExtendedActors(actors); err != nil {
				t.Fatal(err)
			}
			runtime := &runtimeDungeonState{Request: dungeoncmd.SelectDungeonRequest{DungeonID: uint32(id), Difficulty: 2}, Dungeon: dungeon, MazeIndex: 0, Session: ds, Room: room, BossCoordinate: topology.Bosses[0], BossSet: true, Seed: 19871, randomizedObjects: owner}
			packets, err := buildCurrentDungeonEntryPackets(runtime, scene)
			if err != nil {
				t.Fatal("actual single room packet rejected", id, coord, err)
			}
			if len(packets.DungeonInfo) == 0 || len(packets.StartMap) == 0 || packets.StartMapSuppressed {
				t.Fatal("actual single room missing entry", id, coord)
			}
			if dir := os.Getenv("DNF90_ANTON_NATIVE_DIR"); dir != "" {
				for suffix, body := range map[string][]byte{"info": packets.DungeonInfo, "start": packets.StartMap} {
					file := fmt.Sprintf("go-single-anton-%d-%d-%d-%s.bin", id, coord.X, coord.Y, suffix)
					if err := os.WriteFile(filepath.Join(dir, file), body, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			count++
		}
	}
	t.Logf("repaired existing Anton assets: %d DGN, %d actual room entry packets; Luke missing assets still rejected", len(options.SupplementalDungeonIDs), count)
}
