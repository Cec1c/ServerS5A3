package dnfbridge

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"testing"
	"time"
)

func TestRealPVFLukeSanctumMazeFrozenGroupOwnership(t *testing.T) {
	f, p, _ := normalLukeFixture(t)
	s := f.service
	table, err := worldmap.LoadSource(context.Background(), s.initialEquipmentArchive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	d, ok := table.FindDungeon(5054)
	if !ok || len(d.Mazes) != 15 {
		t.Fatal("actual birth mazes unavailable")
	}
	b := f.state.Battle
	selectMaze := func(raw int, frozen bool) (dungeonMazeSelection, *currentLukeSanctumMazePreparation, error) {
		return s.selectCurrentLukeSanctumMaze(p, d, func() (dungeonMazeSelection, error) { return dungeonMazeSelection{Index: raw}, nil }, frozen)
	}
	t.Run("invalid_first_does_not_freeze", func(t *testing.T) {
		if _, _, err := selectMaze(-1, false); err == nil || b.LukeSanctumMazeGroup != nil {
			t.Fatal("invalid first selection froze group", err)
		}
	})
	selected, w, err := selectMaze(5, false)
	if err != nil || selected.Index != 3 || w == nil || w.Record != 1 || w.Ordinal != 0 {
		t.Fatalf("first raw5 selection%+v witness%+v err%v", selected, w, err)
	}
	t.Run("repeated_prepare_bypasses_rng", func(t *testing.T) {
		repeat, ww, err := s.selectCurrentLukeSanctumMaze(p, d, func() (dungeonMazeSelection, error) {
			t.Fatal("frozen group called RNG")
			return dungeonMazeSelection{}, fmt.Errorf("unreachable")
		}, false)
		if err != nil || repeat.Index != 3 || *ww != *w || b.Anton.ClearCount(5054) != 0 {
			t.Fatal(repeat, ww, err)
		}
	})
	t.Run("other_sanctums_share_record_independent_counts", func(t *testing.T) {
		for _, id := range []int64{5055, 5056} {
			dd, ok := table.FindDungeon(id)
			if !ok {
				t.Fatal(id)
			}
			selected, ww, err := s.selectCurrentLukeSanctumMaze(p, dd, func() (dungeonMazeSelection, error) {
				t.Fatal("other sanctuary rerolled")
				return dungeonMazeSelection{}, nil
			}, false)
			if err != nil || selected.Index != 3 || ww.Record != 1 || ww.Ordinal != 0 || ww.DungeonID != uint32(id) {
				t.Fatal(selected, ww, err)
			}
		}
	})
	t.Run("leader_exact_maze_required", func(t *testing.T) {
		if _, _, err := selectMaze(4, true); err == nil {
			t.Fatal("mismatching frozen party maze accepted")
		}
		if selected, _, err := selectMaze(3, true); err != nil || selected.Index != 3 {
			t.Fatal(selected, err)
		}
	})
	runtime := &runtimeDungeonState{Dungeon: d, MazeIndex: 3, lukeSanctumMaze: w}
	if err := validateCurrentLukeSanctumMazePreparation(f.state, runtime); err != nil {
		t.Fatal(err)
	}
	t.Run("witness_tamper_controls", func(t *testing.T) {
		controls := []struct {
			name   string
			mutate func(*currentLukeSanctumMazePreparation)
		}{
			{"instance", func(v *currentLukeSanctumMazePreparation) { v.InstanceID = "foreign" }},
			{"raid", func(v *currentLukeSanctumMazePreparation) { v.RaidKey++ }},
			{"phase", func(v *currentLukeSanctumMazePreparation) { v.Phase = 1 }},
			{"attempt", func(v *currentLukeSanctumMazePreparation) { v.Attempt++ }},
			{"record", func(v *currentLukeSanctumMazePreparation) { v.Record = 2 }},
			{"ordinal", func(v *currentLukeSanctumMazePreparation) { v.Ordinal = 1 }},
			{"dungeon", func(v *currentLukeSanctumMazePreparation) { v.DungeonID = 5055 }},
			{"maze", func(v *currentLukeSanctumMazePreparation) { v.MazeIndex = 4 }},
		}
		for _, row := range controls {
			t.Run(row.name, func(t *testing.T) {
				v := *w
				row.mutate(&v)
				runtime.lukeSanctumMaze = &v
				if err := validateCurrentLukeSanctumMazePreparation(f.state, runtime); err == nil {
					t.Fatal("stale/tampered witness accepted")
				}
			})
		}
		runtime.lukeSanctumMaze = w
	})
	t.Run("clone_isolation", func(t *testing.T) {
		clone := cloneRuntimeRaidBattle(b)
		clone.LukeSanctumMazeGroup.Record = 2
		clone.LukeSanctumMazeGroup.Configuration.Records[1][0] = 99
		if b.LukeSanctumMazeGroup.Record != 1 || b.LukeSanctumMazeGroup.Configuration.Records[1][0] != 3 {
			t.Fatal("battle selection clone aliased")
		}
	})
	t.Run("phase_change_invalidates_preparation", func(t *testing.T) {
		old := b.Phase
		b.Phase = 1
		defer func() { b.Phase = old }()
		if err := validateCurrentLukeSanctumMazePreparation(f.state, runtime); err == nil {
			t.Fatal("old phase witness accepted")
		}
		selected, ww, err := selectMaze(8, false)
		if err != nil || selected.Index != 8 || ww != nil {
			t.Fatal("phase1 ordinary selection changed", selected, ww, err)
		}
	})
	t.Run("accepted_count_advances_only_own_sanctum", func(t *testing.T) {
		original := b.Anton
		defer func() { b.Anton = original }()
		progress := &raid.LukeProgression{AntonProgression: b.Anton}
		entered, _, err := progress.EnterDungeon("maze-fixture-accepted", 5054, b.Anton.Epoch(), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		cleared, replay, err := entered.ClearDungeon("maze-fixture-accepted", 5054, entered.Epoch(), time.Now())
		if err != nil || replay {
			t.Fatal(err, replay)
		}
		b.Anton = cleared.AntonProgression
		selected, ww, err := selectMaze(14, false)
		if err != nil || selected.Index != 4 || ww.Ordinal != 1 {
			t.Fatal(selected, ww, err)
		}
		if err := validateCurrentLukeSanctumMazePreparation(f.state, runtime); err == nil {
			t.Fatal("preclear prepared witness survived accepted count change")
		}
		replayed, replay, err := cleared.ClearDungeon("maze-fixture-accepted", 5054, cleared.Epoch(), time.Now())
		if err != nil || !replay || replayed.ClearCount(5054) != 1 {
			t.Fatal("clear replay advanced ordinal", err)
		}
		dd, _ := table.FindDungeon(5055)
		other, ww, err := s.selectCurrentLukeSanctumMaze(p, dd, func() (dungeonMazeSelection, error) { return dungeonMazeSelection{Index: 14}, nil }, false)
		if err != nil || other.Index != 3 || ww.Ordinal != 0 {
			t.Fatal("cross-sanctum count leaked", other, ww, err)
		}
	})
}

// Real public preparation fails after a group choice (seed dependency), then a
// normal public retry and duplicate op16 keep that group without a clear.
func TestRealPVFLukeSanctumMazeFailedPrepareAndPublicReplay(t *testing.T) {
	f, p, _ := normalLukeFixture(t)
	s := f.service
	ctx := context.Background()
	table, err := worldmap.LoadSource(ctx, s.initialEquipmentArchive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.worldMapTable = table
	s.worldMapResolver, err = worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	s.dungeonMonsterTable, err = newPVFDungeonMonsterCatalog(s.initialEquipmentArchive)
	if err != nil {
		t.Fatal(err)
	}
	s.dungeonAICharacterTable, err = newPVFDungeonAICharacterCatalog(s.initialEquipmentArchive)
	if err != nil {
		t.Fatal(err)
	}
	s.dungeonChoice = func(limit int) (int, error) { return 0, nil }
	s.dungeonSeed = func() (uint32, error) { return 1, nil }
	char, found, err := f.repos.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatal(found, err)
	}
	char.Level = 90
	if char.Stats == nil {
		char.Stats = map[string]int64{}
	}
	char.Stats["fatigue"] = 156
	char.Stats["town_id"] = 50
	char.Stats["area_id"] = 0
	char.Stats["pos_x"] = 580
	char.Stats["pos_y"] = 243
	char.Stats["direction"], char.Stats["area_state"] = 0, 0
	if err = f.repos.Character.Save(ctx, char); err != nil {
		t.Fatal(err)
	}

	first := true
	s.dungeonChoice = func(limit int) (int, error) {
		if first {
			first = false
			if limit != 15 {
				t.Fatalf("unexpected first chooser limit%d", limit)
			}
			return 5, nil
		}
		return 0, nil
	}
	s.dungeonSeed = func() (uint32, error) { return 0, fmt.Errorf("explicit seed failure fixture") }
	body := encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: 5054, Difficulty: 2})
	if err := s.handleDungeonSelectUpper(p, body); err != nil {
		t.Fatal(err)
	}
	if p.dungeon.runtime != nil || f.state.Battle.Anton.ClearCount(5054) != 0 || f.state.Battle.LukeSanctumMazeGroup == nil || f.state.Battle.LukeSanctumMazeGroup.Record != 1 {
		t.Fatal("failed prepare advanced or lost selected group")
	}
	s.dungeonChoice = func(limit int) (int, error) { return 0, nil }
	s.dungeonSeed = func() (uint32, error) { return 1, nil }
	if err := s.handleDungeonSelectUpper(p, body); err != nil {
		t.Fatal(err)
	}
	runtime := p.dungeon.runtime
	if runtime == nil || runtime.MazeIndex != 3 || runtime.raidEntry == nil {
		t.Fatal("public retry did not preserve first group")
	}
	run := runtime.raidEntry.RunID
	if err := s.handleDungeonSelectUpper(p, body); err != nil {
		t.Fatal(err)
	}
	if p.dungeon.runtime != runtime || runtime.raidEntry.RunID != run || f.state.Battle.Anton.ClearCount(5054) != 0 || runtime.MazeIndex != 3 {
		t.Fatal("duplicate public op16 advanced group or ordinal")
	}
}
