package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"testing"
)

type epicFailCampaignAssets struct{ repo.CeraShopAssetUnitOfWork }

func (f epicFailCampaignAssets) WithinCeraShopAssets(ctx context.Context, a, c, scope string, fn func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.SettingsRepository) error) error {
	return f.CeraShopAssetUnitOfWork.WithinCeraShopAssets(ctx, a, c, scope, func(ar repo.AccountRepository, cr repo.CharacterRepository, ir repo.InventoryRepository, er repo.EquipmentRepository, sr repo.SettingsRepository) error {
		if e := fn(ar, cr, ir, er, sr); e != nil {
			return e
		}
		return errors.New("campaign physical abort")
	})
}
func TestEpicBenefitsEnabledCampaignFailureDoesNotChargeOrReplaceRuntime(t *testing.T) {
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
			under := g.CeraShopAssets
			g.CeraShopAssets = epicFailCampaignAssets{under}
			s.repositoryProvider = func() (repo.Group, bool) { return g, true }
			runtime, _, e := s.prepareDungeonRuntime(ctx, a, dungeoncmd.SelectDungeonRequest{DungeonID: id})
			if e == nil || runtime != nil {
				t.Fatal("campaign failure accepted", e)
			}
			v, _, _ := g.Inventory.Load(ctx, "99")
			after, _, _ := g.Character.Load(ctx, "99")
			if v.Slots["0:1"].Count != need || after.Stats["fatigue"] != c.Stats["fatigue"] || a.dungeon.runtime != old {
				t.Fatal("campaign failure consumed admission assets")
			}
			if _, found, _ := g.Settings.Load(ctx, onlineevent.EpicCampaignScope); found {
				t.Fatal("failed campaign committed")
			}
		})
	}
}
