package dnfbridge

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestFiendWarBoardSelectionMatchesEncounterRealPVF(t *testing.T) {
	f, peers := actualCurrentFiendWar(t, 1)
	p, b := peers[0], f.state.Battle.FiendWar
	table, err := worldmap.LoadSource(context.Background(), f.service.initialEquipmentArchive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	b.Symbols[b.Config.Symbols["radar state"]] = 1
	checked := 0
	for id, area := range b.Config.Areas {
		if area.Dungeon <= 0 {
			continue
		}
		d, found := table.FindDungeon(int64(area.Dungeon))
		if !found {
			t.Fatalf("missing dungeon %d", area.Dungeon)
		}
		b.MemberAreas[p.selectedCharacterID] = id
		if area.Kind != "" && area.Kind != "radar" && area.Kind != "battery" {
			b.Symbols[b.Config.Symbols[area.Kind+" state"]] = 1
		}
		if area.Kind == "radar" {
			continue
		} // cleared radar no longer exposes an encounter
		count := 1
		if area.Kind == "" {
			count = 6
		}
		for variant := 0; variant < count; variant++ {
			if area.Kind == "" {
				state := b.Areas[id]
				state.Enemy, state.Monster = fiendwar.FiendWarUnit{}, fiendwar.FiendWarUnit{}
				if variant < 4 {
					state.Enemy = fiendwar.FiendWarUnit{ID: 1, Type: byte(variant + 1)}
				} else {
					state.Monster = fiendwar.FiendWarUnit{ID: 2, Type: byte(variant - 3)}
				}
				b.Areas[id] = state
			}
			selected, err := f.service.selectCurrentFiendWarMaze(p, d)
			if err != nil {
				t.Fatalf("area %d variant %d: %v", id, variant, err)
			}
			maze := d.Mazes[selected.Index]
			room, err := resolver.Resolve(worldmap.ResolveRequest{DungeonID: d.ID, MazeIndex: selected.Index, X: maze.Boss.X, Y: maze.Boss.Y})
			if err != nil {
				t.Fatal(err)
			}
			board, visible := runtimeFiendWarRevealEnemy(b, id, runtimeFiendWarBoardEnemy(b, id))
			if area.Kind == "" && variant >= 4 {
				board, visible = runtimeFiendWarRevealMonster(b, id, b.Areas[id].Monster)
			}
			if !visible || board.Type == 0 {
				t.Fatalf("area %d no revealed monster", id)
			}
			matched := false
			for _, monster := range room.Map.Monsters {
				if uint32(monster.MonsterID) == board.ID && (monster.Rank == "[boss]" || monster.Rank == "boss") {
					matched = true
				}
			}
			if !matched {
				t.Fatalf("area %d variant %d board=%d maze=%d map=%d does not contain board boss", id, variant, board.ID, selected.Index, room.Map.ID)
			}
			checked++
		}
	}
	if checked != 105 {
		t.Fatalf("unexpected encounter coverage %d", checked)
	}
}
