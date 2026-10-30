package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func loadMonsterTarotRealPVFCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the matching deployed PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestRealPVFAllMonsterTarotCardsParse(t *testing.T) {
	catalog := loadMonsterTarotRealPVFCatalog(t)
	seen := 0
	for itemID := uint32(10159697); itemID <= 10159718; itemID++ {
		itemID := itemID
		t.Run(fmt.Sprint(itemID), func(t *testing.T) {
			definition, err := resolveCurrentBoosterDefinition(catalog, itemID, currentBoosterRequestRandom)
			if err != nil {
				t.Fatal(err)
			}
			if definition.StackableType != "[monster tarot card]" ||
				definition.LotteryGoldCost != 100000 ||
				definition.Random.MaterialItemID != 0 ||
				definition.Random.MaterialCountPerUse != 0 {
				t.Fatalf("definition=%+v", definition)
			}
			if definition.Random.Kind != "random" || len(definition.Random.Groups) != 1 {
				t.Fatalf("random resolution=%+v", definition.Random)
			}
			group := definition.Random.Groups[0]
			if group.DrawCount != 1 || len(group.Entries) == 0 {
				t.Fatalf("group=%+v", group)
			}
			wantPath := fmt.Sprintf("stackable/10159001/%d.stk", itemID)
			if definition.Random.BoxPVFPath != wantPath {
				t.Fatalf("path=%q want=%q", definition.Random.BoxPVFPath, wantPath)
			}
			var totalWeight int64
			for _, entry := range group.Entries {
				if entry.ItemID <= 0 || entry.ItemID > int64(^uint32(0)) || entry.Weight < 0 || entry.Count <= 0 {
					t.Fatalf("invalid reward row=%+v", entry)
				}
				totalWeight += entry.Weight
				if _, err := catalog.ResolveItem(uint32(entry.ItemID)); err != nil {
					t.Fatalf("unresolved reward item=%d err=%v", entry.ItemID, err)
				}
			}
			if totalWeight <= 0 {
				t.Fatal("zero total reward weight")
			}
		})
		seen++
	}
	if seen != 22 {
		t.Fatalf("monster tarot files checked=%d, want 22", seen)
	}
}

func TestRealPVFMonsterTarotRepresentativeCommits(t *testing.T) {
	catalog := loadMonsterTarotRealPVFCatalog(t)
	for _, itemID := range []uint32{10159698, 10159712} {
		itemID := itemID
		t.Run(fmt.Sprint(itemID), func(t *testing.T) {
			ctx := context.Background()
			repositories := memory.NewMemoryGroup()
			if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
				CharacterID: "19",
				AccountID:   "account-1",
				Level:       90,
				Stats:       map[string]int64{"gold": 100007, "fatigue": 120},
			}); err != nil {
				t.Fatal(err)
			}
			if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
				CharacterID: "19",
				Slots: map[string]dnfrepo.ItemStack{
					"0:75": {ItemID: int64(itemID), Count: 2},
				},
			}); err != nil {
				t.Fatal(err)
			}
			definition, err := resolveCurrentBoosterDefinition(catalog, itemID, currentBoosterRequestRandom)
			if err != nil {
				t.Fatal(err)
			}
			service := currentBoosterTestService(catalog, repositories)
			session := &gameSession{selectedCharacterID: 19}
			request := currentBoosterOpenRequest{Kind: currentBoosterRequestRandom, SourceSlot: 75}
			if _, err := service.prepareCurrentBooster(ctx, session, catalog, request); err != nil {
				t.Fatal(err)
			}
			result, err := service.commitCurrentBooster(ctx, session, catalog, definition, request)
			if err != nil {
				t.Fatal(err)
			}
			if result.SourceItemID != itemID || result.SourceRemaining != 1 || len(result.Rewards) != 1 || result.Rewards[0].ItemID == 0 {
				t.Fatalf("result=%+v", result)
			}
			character, found, err := repositories.Character.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("character found=%t err=%v", found, err)
			}
			if character.Stats["gold"] != 7 || character.Stats["fatigue"] != 120 {
				t.Fatalf("character stats=%v", character.Stats)
			}
			inventory, found, err := repositories.Inventory.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("inventory found=%t err=%v", found, err)
			}
			source := inventory.Slots["0:75"]
			if source.ItemID != int64(itemID) || source.Count != 1 {
				t.Fatalf("source=%+v", source)
			}
			if got := monsterTarotInventoryCount(inventory, int64(result.Rewards[0].ItemID)); got < int64(result.Rewards[0].Count) {
				t.Fatalf("reward item=%d inventory count=%d result count=%d", result.Rewards[0].ItemID, got, result.Rewards[0].Count)
			}
		})
	}
}
