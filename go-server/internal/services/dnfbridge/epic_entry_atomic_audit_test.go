package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"testing"
)

func TestEpicRoadActualActiveEntryRejectionPreservesTickets(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	actual, _, _, _ := prepareMonthlyBridge(t)
	table, err := worldmap.LoadSource(context.Background(), actual.initialEquipmentArchive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []uint32{6063, 6064, 6065} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			s, a, _ := newBackToVillageRuntime(t)
			g, _ := s.repositoryGroup()
			c, _, e := g.Character.Load(context.Background(), "99")
			if e != nil {
				t.Fatal(e)
			}
			c.Level = 90
			if e = g.Character.Save(context.Background(), c); e != nil {
				t.Fatal(e)
			}
			need := int64(30 + i*15)
			if e = g.Inventory.Save(context.Background(), repo.InventoryRecord{CharacterID: "99", Slots: map[string]repo.ItemStack{"0:1": {ItemID: 3330, Count: need}, "0:2": {ItemID: 3329, Count: 7}}}); e != nil {
				t.Fatal(e)
			}
			s.options.currentClientVersion = "official-24.9"
			s.worldMapTable = table
			s.worldMapResolver = resolver
			active := a.dungeon.runtime
			_, _, e = s.prepareDungeonRuntime(context.Background(), a, dungeoncmd.SelectDungeonRequest{DungeonID: id})
			if !errors.Is(e, errDungeonAlreadyActive) {
				t.Fatalf("wrong failure: %v", e)
			}
			v, _, e := g.Inventory.Load(context.Background(), "99")
			if e != nil {
				t.Fatal(e)
			}
			if v.Slots["0:1"].Count != need || v.Slots["0:2"].Count != 7 || a.dungeon.runtime != active {
				t.Fatalf("rejected active entry consumed tickets: dungeon%d got%d want%d activeRetained=%v", id, v.Slots["0:1"].Count, need, a.dungeon.runtime == active)
			}
		})
	}
}
func TestEpicRoadActualPreparedEntryCommitChargesOnce(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	actual, _, _, _ := prepareMonthlyBridge(t)
	ctx := context.Background()
	table, e := worldmap.LoadSource(ctx, actual.initialEquipmentArchive, worldmap.Options{})
	if e != nil {
		t.Fatal(e)
	}
	resolver, e := worldmap.NewResolver(table)
	if e != nil {
		t.Fatal(e)
	}
	monsters, e := newPVFDungeonMonsterCatalog(actual.initialEquipmentArchive)
	if e != nil {
		t.Fatal(e)
	}
	ai, e := newPVFDungeonAICharacterCatalog(actual.initialEquipmentArchive)
	if e != nil {
		t.Fatal(e)
	}
	for i, id := range []uint32{6063, 6064, 6065} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			s, a, old := newBackToVillageRuntime(t)
			if e := old.Session.Abandon(); e != nil {
				t.Fatal(e)
			}
			g, _ := s.repositoryGroup()
			c, _, e := g.Character.Load(ctx, "99")
			if e != nil {
				t.Fatal(e)
			}
			c.Level = 90
			c.Stats["town_id"] = 50
			c.Stats["area_id"] = 0
			c.Stats["pos_x"] = 441
			c.Stats["pos_y"] = 207
			if e = g.Character.Save(ctx, c); e != nil {
				t.Fatal(e)
			}
			need := int64(30 + i*15)
			if e = g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "99", Slots: map[string]repo.ItemStack{"0:1": {ItemID: 3330, Count: need}, "0:2": {ItemID: 3329, Count: 7}}}); e != nil {
				t.Fatal(e)
			}
			s.options.currentClientVersion = "official-24.9"
			s.worldMapTable = table
			s.worldMapResolver = resolver
			s.dungeonMonsterTable = monsters
			s.dungeonAICharacterTable = ai
			s.initialEquipmentArchive = actual.initialEquipmentArchive
			s.bindGameSessionCharacter(a, 99)
			bindDungeonSelectorOriginForTestAt(t, s, a, 50, 0, 441, 207)
			runtime, _, e := s.prepareDungeonRuntime(ctx, a, dungeoncmd.SelectDungeonRequest{DungeonID: id})
			if e != nil {
				t.Fatal("actual epic prepare", e)
			}
			v, _, _ := g.Inventory.Load(ctx, "99")
			if v.Slots["0:1"].Count != need {
				t.Fatal("prepare charged", v.Slots["0:1"].Count)
			}
			before, _, _ := g.Character.Load(ctx, "99")
			if e = freezeCurrentDungeonTownReturnOrigin(a, runtime); e != nil {
				t.Fatal(e)
			}
			if e = s.commitDungeonRuntime(a, runtime); e != nil {
				t.Fatal("commit", e)
			}
			after, _, _ := g.Character.Load(ctx, "99")
			v, _, _ = g.Inventory.Load(ctx, "99")
			if a.dungeon.runtime != runtime || v.Slots["0:1"].Count != 0 || v.Slots["0:2"].Count != 7 || after.Stats["fatigue"] != before.Stats["fatigue"]-1 || after.Stats["epic_entry_sequence"] != 1 {
				t.Fatalf("actual atomic admission before%v after%v bag%v", before.Stats, after.Stats, v.Slots)
			}
			if e = s.commitDungeonRuntime(a, runtime); e == nil {
				t.Fatal("repeat active admission accepted")
			}
			final, _, _ := g.Character.Load(ctx, "99")
			if final.Stats["fatigue"] != after.Stats["fatigue"] || final.Stats["epic_entry_sequence"] != 1 {
				t.Fatal("repeat charged")
			}
		})
	}
}

type epicBridgeFaultAssets struct {
	repo.CharacterAssetUnitOfWork
	after func() error
}

func (v epicBridgeFaultAssets) WithinCharacterAssets(ctx context.Context, id string, fn func(repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return v.CharacterAssetUnitOfWork.WithinCharacterAssets(ctx, id, func(c repo.CharacterRepository, b repo.InventoryRepository, e repo.EquipmentRepository) error {
		if err := fn(c, b, e); err != nil {
			return err
		}
		return v.after()
	})
}
func TestEpicRoadActualCommitFailureAndIdentityRejectsKeepPaidAssets(t *testing.T) {
	actual, _, _, _ := prepareMonthlyBridge(t)
	ctx := context.Background()
	table, e := worldmap.LoadSource(ctx, actual.initialEquipmentArchive, worldmap.Options{})
	if e != nil {
		t.Fatal(e)
	}
	resolver, e := worldmap.NewResolver(table)
	if e != nil {
		t.Fatal(e)
	}
	monsters, e := newPVFDungeonMonsterCatalog(actual.initialEquipmentArchive)
	if e != nil {
		t.Fatal(e)
	}
	ai, e := newPVFDungeonAICharacterCatalog(actual.initialEquipmentArchive)
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"rollback-after-two-writes", "cancel-after-two-writes", "panic-after-two-writes", "generation-after-two-writes", "changed-character", "changed-account", "foreign-canonical", "missing-canonical", "foreign-stored-account", "different-runtime-dungeon", "missing-town-origin"} {
		t.Run(name, func(t *testing.T) {
			s, a, old := newBackToVillageRuntime(t)
			if e = old.Session.Abandon(); e != nil {
				t.Fatal(e)
			}
			g, _ := s.repositoryGroup()
			c, _, _ := g.Character.Load(ctx, "99")
			c.Level = 90
			c.Stats["town_id"] = 50
			c.Stats["area_id"] = 0
			c.Stats["pos_x"] = 441
			c.Stats["pos_y"] = 207
			if e = g.Character.Save(ctx, c); e != nil {
				t.Fatal(e)
			}
			if e = g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "99", Slots: map[string]repo.ItemStack{"0:1": {ItemID: 3330, Count: 30}, "0:2": {ItemID: 3329, Count: 7}}}); e != nil {
				t.Fatal(e)
			}
			s.options.currentClientVersion = "official-24.9"
			s.worldMapTable = table
			s.worldMapResolver = resolver
			s.dungeonMonsterTable = monsters
			s.dungeonAICharacterTable = ai
			s.initialEquipmentArchive = actual.initialEquipmentArchive
			s.bindGameSessionCharacter(a, 99)
			bindDungeonSelectorOriginForTestAt(t, s, a, 50, 0, 441, 207)
			runtime, _, e := s.prepareDungeonRuntime(ctx, a, dungeoncmd.SelectDungeonRequest{DungeonID: 6063})
			if e != nil {
				t.Fatal(e)
			}
			if e = freezeCurrentDungeonTownReturnOrigin(a, runtime); e != nil {
				t.Fatal(e)
			}
			before, _, _ := g.Character.Load(ctx, "99")
			under := g.CharacterAssets
			fault := errors.New("injected before physical commit")

			applyAssets := func(after func() error) {
				g.CharacterAssets = epicBridgeFaultAssets{under, after}
				s.repositoryProvider = func() (repo.Group, bool) { return g, true }
			}
			switch name {
			case "rollback-after-two-writes":
				applyAssets(func() error { return fault })
			case "cancel-after-two-writes":
				parent, cancel := context.WithCancel(ctx)
				a.ctx = parent
				applyAssets(func() error { cancel(); return context.Canceled })
			case "panic-after-two-writes":
				applyAssets(func() error { panic(fault) })
			case "generation-after-two-writes":
				applyAssets(func() error { a.characterGeneration++; return fault })
			case "changed-character":
				a.selectedCharacterID = 100
			case "changed-account":
				a.accountID = "foreign"
			case "foreign-canonical":
				s.gameSessions[99] = &gameSession{selectedCharacterID: 99}
			case "missing-canonical":
				delete(s.gameSessions, 99)
			case "foreign-stored-account":
				v := before
				v.AccountID = "foreign"
				g.Character.Save(ctx, v)
			case "different-runtime-dungeon":
				d, ok := table.FindDungeon(6064)
				if !ok {
					t.Fatal("6064 missing")
				}
				runtime.Dungeon = d
			case "missing-town-origin":
				runtime.townReturnOrigin = currentTownPositionSnapshot{}
			}
			if name == "panic-after-two-writes" {
				seen := false
				func() { defer func() { seen = recover() != nil }(); s.commitDungeonRuntime(a, runtime) }()
				if !seen {
					t.Fatal("fault not reached")
				}
				if !s.mu.TryLock() {
					t.Fatal("global mutex leaked on panic")
				}
				s.mu.Unlock()
			} else {
				if e = s.commitDungeonRuntime(a, runtime); e == nil {
					t.Fatal("failure accepted")
				}
			}
			after, _, _ := g.Character.Load(ctx, "99")
			v, _, _ := g.Inventory.Load(ctx, "99")
			if v.Slots["0:1"].Count != 30 || v.Slots["0:2"].Count != 7 || after.Stats["fatigue"] != before.Stats["fatigue"] || after.Stats["epic_entry_sequence"] != 0 || a.dungeon.runtime != old {
				t.Fatalf("failed commit changed assets fp%d/%d bag%v seq%d old%v", after.Stats["fatigue"], before.Stats["fatigue"], v.Slots, after.Stats["epic_entry_sequence"], a.dungeon.runtime == old)
			}
			if name == "rollback-after-two-writes" {
				g.CharacterAssets = under
				s.repositoryProvider = func() (repo.Group, bool) { return g, true }
				if e = s.commitDungeonRuntime(a, runtime); e != nil {
					t.Fatal("legitimate retry", e)
				}
				after, _, _ = g.Character.Load(ctx, "99")
				v, _, _ = g.Inventory.Load(ctx, "99")
				if after.Stats["epic_entry_sequence"] != 1 || after.Stats["fatigue"] != before.Stats["fatigue"]-1 || v.Slots["0:1"].Count != 0 {
					t.Fatal("retry not exact")
				}
			}

		})
	}
}
