package memory

import (
	"context"
	"reflect"
	"strconv"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestWeddingAssetsRollbackCharactersInventoriesAndSharedRoomTogether(t *testing.T) {
	for _, existed := range []bool{false, true} {
		t.Run(strconv.FormatBool(existed), func(t *testing.T) {
			ctx := context.Background()
			g := NewMemoryGroup()
			ids := []string{"2", "10"}
			scope, err := r.WeddingPairScope(ids)
			if err != nil {
				t.Fatal(err)
			}
			beforeCharacters := map[string]r.CharacterRecord{}
			beforeInventories := map[string]r.InventoryRecord{}
			for _, id := range ids {
				c := r.CharacterRecord{CharacterID: id, AccountID: "a" + id, Stats: map[string]int64{"gold": 1000, "wedding_v1_love_points": 100}}
				bag := r.InventoryRecord{CharacterID: id, Slots: map[string]r.ItemStack{"0:65": {ItemID: 10002611, Count: 1, Extra: map[string]string{"identity": id}}}}
				if err := r.SaveCharacterFields(ctx, g.Character, c, append(r.AllCharacterFields(), r.CharacterFieldWedding)...); err != nil {
					t.Fatal(err)
				}
				if err := g.Inventory.Save(ctx, bag); err != nil {
					t.Fatal(err)
				}
				beforeCharacters[id], _, _ = g.Character.Load(ctx, id)
				beforeInventories[id], _, _ = g.Inventory.Load(ctx, id)
			}
			if existed {
				if err := g.Settings.Save(ctx, r.SettingsRecord{Scope: scope, Values: map[string]string{"room": "before"}}); err != nil {
					t.Fatal(err)
				}
			}
			beforeSettings, _, _ := g.Settings.Load(ctx, scope)
			u := g.CharacterTrade.(*memoryCharacterTradeUnitOfWork)
			// Fail after the settings write itself, so rollback must restore or
			// remove the room as well as both previously committed owners.
			u.settings = &failRaidSettingsOnce{SettingsRepository: g.Settings, fail: true}
			mutate := func(chars r.CharacterRepository, bags r.InventoryRepository, settings r.SettingsRepository) error {
				if _, _, err := chars.Load(ctx, "3"); err == nil {
					t.Fatal("third character escaped pair scope")
				}
				if _, _, err := settings.Load(ctx, "character-pair:wedding:v1:2:3"); err == nil {
					t.Fatal("other couple room escaped settings scope")
				}
				for _, id := range []string{"10", "2"} {
					c, _, err := chars.Load(ctx, id)
					if err != nil {
						return err
					}
					c.Stats["gold"] -= 500
					c.Stats["wedding_v1_love_points"] -= 50
					if err := r.SaveCharacterFields(ctx, chars, c, r.CharacterFieldStats, r.CharacterFieldWedding); err != nil {
						return err
					}
					bag, _, err := bags.Load(ctx, id)
					if err != nil {
						return err
					}
					delete(bag.Slots, "0:65")
					if err := r.SaveInventoryFields(ctx, bags, bag, r.InventoryFieldSlots); err != nil {
						return err
					}
				}
				return settings.Save(ctx, r.SettingsRecord{Scope: scope, Values: map[string]string{"room": "after"}})
			}
			if err := u.WithinWeddingAssets(ctx, ids, scope, mutate); err == nil {
				t.Fatal("settings failure accepted")
			}
			for _, id := range ids {
				c, _, _ := g.Character.Load(ctx, id)
				bag, _, _ := g.Inventory.Load(ctx, id)
				if !reflect.DeepEqual(c, beforeCharacters[id]) || !reflect.DeepEqual(bag, beforeInventories[id]) {
					t.Fatalf("owner %s retained partial asset commit", id)
				}
			}
			afterSettings, found, _ := g.Settings.Load(ctx, scope)
			if found != existed || found && !reflect.DeepEqual(beforeSettings, afterSettings) {
				t.Fatal("failed settings write retained room mutation")
			}
			if err := u.WithinWeddingAssets(ctx, ids, scope, mutate); err != nil {
				t.Fatal(err)
			}
			for _, id := range ids {
				c, _, _ := g.Character.Load(ctx, id)
				bag, _, _ := g.Inventory.Load(ctx, id)
				if c.Stats["gold"] != 500 || c.Stats["wedding_v1_love_points"] != 50 || len(bag.Slots) != 0 {
					t.Fatalf("successful owner %s state %+v %+v", id, c, bag)
				}
			}
		})
	}
}
