package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestAuditRealPVFAntonPhaseTwoAllRoomEntryPackets(t *testing.T) {
	name := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if name == "" {
		t.Skip("requires independently repaired candidate PVF, not a production replacement")
	}
	a, err := platformpvf.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	config, err := raid.LoadBattleConfiguration(a, "etc/raid/anton.etc")
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, d := range config.PhaseDungeons[1] {
		ids = append(ids, int64(d.ID))
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
	for _, id := range ids {
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
	t.Logf("actual369d second phase: %d DGN, %d in-memory room entry packet fixtures", len(ids), count)
}

// Exercises actual369d reducer outputs against the real bridge admission and
// second commit validator. Logical time advances in-memory; no live raid run.
func TestAuditRealPVFAntonPhaseTwoSelectionAndCommitBoundaries(t *testing.T) {
	s, session, r, b, at := actualEnergySymbolFixture(t, false)
	a, err := platformpvf.Open(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	audit := func(label string) {
		for _, d := range b.Dungeons {
			dungeon, found := table.FindDungeon(int64(d.ID))
			if !found {
				t.Fatal(d.ID, "actual dungeon absent")
			}
			target := *r
			target.Dungeon = dungeon
			target.raidEntry = nil
			allowed := s.currentRaidDungeonSelectionAllowed(session, d.ID)
			err := s.freezeCurrentRaidDungeonGroupEntry(session, &target, nil)
			if allowed != (d.State == 0) || (err == nil) != (d.State == 0) {
				t.Fatalf("%s dungeon%d state%d allowed%v freeze%v", label, d.ID, d.State, allowed, err)
			}
			if d.State == 0 {
				if err := s.validateCurrentRaidDungeonEntry(session, &target); err != nil {
					t.Fatal(label, d.ID, err)
				}
				original := b.Dungeons
				b.Dungeons = append([]raid.DungeonState(nil), original...)
				for i := range b.Dungeons {
					if b.Dungeons[i].ID == d.ID {
						b.Dungeons[i].State = 1
					}
				}
				if err := s.validateCurrentRaidDungeonEntry(session, &target); err == nil {
					t.Fatal(label, d.ID, "closed after prepare remained admitted")
				}
				b.Dungeons = original
			}
		}
	}
	audit("initial")
	next, err := b.Anton.Tick(at.Add(180 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	b.Anton, b.Dungeons = next, next.Dungeons()
	opened := 0
	for _, d := range b.Dungeons {
		if d.ID >= 221 && d.ID <= 224 && d.State == 0 {
			opened++
		}
	}
	if opened != 3 {
		t.Fatal("actual selected hatcheries", opened)
	}
	audit("180sec three hatcheries including infection")
	infection := next.InfectionDungeon()
	next, _, err = next.ClearDungeon("audit-hatchery", infection, next.Epoch(), at.Add(181*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	b.Anton, b.Dungeons = next, next.Dungeons()
	audit("cleared hatchery closed")
	next, err = next.Tick(at.Add(421 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	b.Anton, b.Dungeons = next, next.Dungeons()
	audit("hatchery recovery")
	next, _, err = next.ClearDungeon("audit-volcano", 219, next.Epoch(), at.Add(422*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	b.Anton, b.Dungeons = next, next.Dungeons()
	audit("volcano clear heart unlock energy volcano close")
	b.PhaseCompleted = true
	if s.currentRaidDungeonSelectionAllowed(session, 220) {
		t.Fatal("phase completed admits heart")
	}
	t.Log("actual369d initial/180sec/closed/recovery/heart plus prepare-commit close validated")
}
