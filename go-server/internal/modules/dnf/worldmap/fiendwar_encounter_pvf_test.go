package worldmap

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// A shared-directory filename match must not replace the chosen maze's
// explicit boss room. Otherwise the board and the encounter name different bosses.
func TestFiendWarEncounterUsesDeclaredBossRealPVF(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("requires current real PVF")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	table, err := LoadSource(context.Background(), archive, Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, dungeon := range table.dungeons {
		if !strings.Contains(strings.ToLower(dungeon.Path), "180621_fiendwar/") {
			continue
		}
		for mi, maze := range dungeon.Mazes {
			if maze.Boss == nil {
				t.Fatalf("dungeon %d maze %d lacks boss", dungeon.ID, mi)
			}
			var declared []int64
			for _, spec := range append(append([]MapSpecification(nil), maze.MapSpecifications...), maze.BossSpecifications...) {
				if spec.Coordinate.X == maze.Boss.X && spec.Coordinate.Y == maze.Boss.Y {
					declared = append(declared, spec.MapIDs...)
				}
			}
			if len(declared) != 1 {
				t.Fatalf("dungeon %d maze %d boss declarations=%v", dungeon.ID, mi, declared)
			}
			checked++
			declaredMap, present := table.FindMap(declared[0])
			if !present {
				t.Fatalf("declared map %d absent", declared[0])
			}
			if len(declaredMap.DungeonIDs) != 1 || declaredMap.DungeonIDs[0] != dungeon.ID {
				t.Fatalf("foreign declaration dungeon=%d map=%d", dungeon.ID, declared[0])
			}
			resolved, resolveErr := resolver.Resolve(ResolveRequest{DungeonID: dungeon.ID, MazeIndex: mi, X: maze.Boss.X, Y: maze.Boss.Y})
			if resolveErr != nil {
				t.Errorf("dungeon %d maze %d: %v", dungeon.ID, mi, resolveErr)
				continue
			}
			var monsters []int64
			for _, spawn := range resolved.Map.Monsters {
				if spawn.Rank == "[boss]" || spawn.Rank == "boss" {
					monsters = append(monsters, spawn.MonsterID)
				}
			}
			t.Logf("dungeon=%d maze=%d declared=%v actual=%d source=%s bosses=%v", dungeon.ID, mi, declared, resolved.Map.ID, resolved.Source, monsters)
			if !slices.Contains(declared, resolved.Map.ID) {
				t.Errorf("dungeon %d maze %d uses foreign boss map %d instead of %v", dungeon.ID, mi, resolved.Map.ID, declared)
			}
			boardBoss, fixed := fiendwar.FiendWarFixtureBossID(int(dungeon.ID), mi)
			if !fixed {
				if mi < 4 {
					boardBoss, fixed = fiendwar.FiendWarArmyBossID(byte(mi + 1))
				} else {
					boardBoss, fixed = fiendwar.FiendWarOrcBossID(byte(mi - 3))
				}
			}
			if !fixed || !slices.Contains(monsters, int64(boardBoss)) {
				t.Errorf("dungeon %d maze %d board boss %d absent from encounter %v", dungeon.ID, mi, boardBoss, monsters)
			}
			layout, layoutErr := BuildDungeonLayout(resolver, dungeon.ID, mi, func(choice DungeonMapChoice) (int64, error) {
				return choice.Candidates[len(choice.Candidates)-1].ID, nil
			})
			if layoutErr != nil {
				t.Errorf("dungeon %d maze %d layout: %v", dungeon.ID, mi, layoutErr)
				continue
			}
			for _, room := range layout.rooms {
				var ids []int64
				for _, spec := range append(append([]MapSpecification(nil), maze.MapSpecifications...), maze.BossSpecifications...) {
					if spec.Coordinate.X == room.Coordinate.X && spec.Coordinate.Y == room.Coordinate.Y {
						ids = append(ids, spec.MapIDs...)
					}
				}
				if len(ids) == 1 && (room.Map == nil || room.Map.Map.ID != ids[0]) {
					t.Errorf("layout dungeon %d maze %d room %v escaped declaration %v: %+v", dungeon.ID, mi, room.Coordinate, ids, room.Map)
				}
				if room.Boss && (room.Map == nil || room.Map.Map.ID != declared[0]) {
					t.Errorf("final layout boss dungeon=%d maze=%d differs from %d", dungeon.ID, mi, declared[0])
				}
			}
		}
	}
	if checked != 118 {
		t.Fatalf("unexpected corpus size: %d", checked)
	}
}
