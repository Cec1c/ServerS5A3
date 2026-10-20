package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"reflect"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func epicBatchCostFixture(t *testing.T, costID uint32, costCount int64) (dnfrepo.Group, *Service, *gameSession, currentEpicProductionCompoundPlan, currentEpicProductionCompoundRequest) {
	t.Helper()
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	if err := g.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "batch-account", Level: 90, Stats: map[string]int64{}}); err != nil {
		t.Fatal(err)
	}
	inv := dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{"0:125": {ItemID: 10163136, Count: 1000}}}
	account := dnfrepo.AccountInventoryRecord{AccountID: "batch-account", Slots: map[string]dnfrepo.ItemStack{}}
	if slot, shared := currentDisjointWarehouseFixedSlots[costID]; shared {
		account.Slots[dnfrepo.AccountSharedInventorySlotKey(slot)] = dnfrepo.ItemStack{ItemID: int64(costID), Count: costCount}
	} else if costID != 0 {
		inv.Slots["0:126"] = dnfrepo.ItemStack{ItemID: int64(costID), Count: costCount}
	}
	if err := g.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if err := g.AccountInventory.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	s := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return g, true }}
	plan := currentEpicProductionCompoundPlan{
		Recipe:           currentEpicProductionCatalyst{ItemID: 10163134, NeedCount: 1, MinMakeCount: 1, MaxMakeCount: 1, MustNeedItemID: costID, MustNeedItemCount: 1},
		OutputDefinition: dungeonDropItemDefinition{ItemID: 10163134, Kind: dungeonDropItemStackable, StackableType: "[material]", StackLimit: 9999, SlotStart: 97, SlotEnd: 120},
		Contribution:     1000,
	}
	request := currentEpicProductionCompoundRequest{CatalystItemID: 10163134, Materials: []currentEpicProductionCompoundMaterial{{ItemID: 10163136, Slot: 125, Count: 1000}}}
	return g, s, &gameSession{accountID: "batch-account", selectedCharacterID: 1}, plan, request
}

func TestEpicProductionBatchCostScalesWithRecipeUnits(t *testing.T) {
	for _, costID := range []uint32{10099775, 2610030} {
		for _, size := range []uint32{1, 1000} {
			g, s, session, plan, request := epicBatchCostFixture(t, costID, 2004)
			plan.Contribution = uint64(size)
			request.Materials[0].Count = size
			result, err := s.commitCurrentEpicProductionCompound(context.Background(), session, plan, request, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			if result.OutputCount != size {
				t.Fatalf("output=%d want=%d", result.OutputCount, size)
			}
			account, _, _ := g.AccountInventory.Load(context.Background(), session.accountID)
			inv, _, _ := g.Inventory.Load(context.Background(), "1")
			cost := inv.Slots["0:126"].Count
			costSlot := int16(126)
			if slot, shared := currentDisjointWarehouseFixedSlots[costID]; shared {
				costSlot = slot
				cost = account.Slots[dnfrepo.AccountSharedInventorySlotKey(slot)].Count
			}
			if cost != 2004-int64(size) {
				t.Fatalf("cost item=%d batch=%d remaining=%d want=%d", costID, size, cost, 2004-int64(size))
			}
			found := false
			for _, update := range result.Updates {
				if int16(binary.LittleEndian.Uint16(update.data[:2])) == costSlot {
					found = binary.LittleEndian.Uint32(update.data[6:10]) == uint32(cost)
				}
			}
			if !found {
				t.Fatal("committed cost balance missing from item refresh")
			}
		}
	}
}

func TestEpicProductionBatchCostIncludesCarryButNotOutputMultiplier(t *testing.T) {
	g, s, session, plan, request := epicBatchCostFixture(t, 10099775, 7)
	ctx := context.Background()
	character, _, _ := g.Character.Load(ctx, "1")
	character.Stats = map[string]int64{currentEpicProductionCarryGroup1Stat: 2}
	if err := g.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	plan.Contribution = 8
	plan.Recipe.GroupKey = 1
	plan.Recipe.NeedCount = 3
	plan.Recipe.MinMakeCount = 3
	plan.Recipe.MaxMakeCount = 3
	plan.Recipe.MustNeedItemCount = 2
	request.Materials[0].Count = 8
	result, err := s.commitCurrentEpicProductionCompound(ctx, session, plan, request, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	account, _, _ := g.AccountInventory.Load(ctx, session.accountID)
	character, _, _ = g.Character.Load(ctx, "1")
	if result.OutputCount != 9 || result.CarryValue != 1 || character.Stats[currentEpicProductionCarryGroup1Stat] != 1 || account.Slots[dnfrepo.AccountSharedInventorySlotKey(364)].Count != 1 {
		t.Fatalf("three units must yield nine products for six souls: result=%+v account=%+v stats=%+v", result, account.Slots, character.Stats)
	}
}

var errEpicBatchLateFailure = errors.New("injected batch transaction failure")

type epicBatchLateFailure struct {
	dnfrepo.AccountCharacterAssetUnitOfWork
}

func (u epicBatchLateFailure) WithinAccountCharacterAssets(ctx context.Context, account, character string, apply func(dnfrepo.AccountInventoryRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
	return u.AccountCharacterAssetUnitOfWork.WithinAccountCharacterAssets(ctx, account, character, func(a dnfrepo.AccountInventoryRepository, c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, e dnfrepo.EquipmentRepository) error {
		if err := apply(a, c, i, e); err != nil {
			return err
		}
		return errEpicBatchLateFailure
	})
}

func TestEpicProductionBatchCostFailurePreservesAllAssets(t *testing.T) {
	for _, kind := range []string{"insufficient_shared", "insufficient_bag", "cost_overflow", "late_failure", "foreign_owner", "full_inventory"} {
		t.Run(kind, func(t *testing.T) {
			costID := uint32(10099775)
			if kind == "insufficient_bag" {
				costID = 2610030
			}
			g, s, session, plan, request := epicBatchCostFixture(t, costID, 999)
			switch kind {
			case "cost_overflow":
				plan.Recipe.MustNeedItemCount = math.MaxUint32
			case "late_failure":
				plan.Contribution = 2
				plan.Recipe.GroupKey = 1
				failing := g
				failing.AccountAssets = epicBatchLateFailure{g.AccountAssets}
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return failing, true }
			case "foreign_owner":
				session.accountID = "other-account"
			case "full_inventory":
				plan.Contribution = 2
				request.Materials[0].Count = 2
				inv, _, _ := g.Inventory.Load(context.Background(), "1")
				for slot := int16(3); slot < 354; slot++ {
					key := currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, slot)
					if _, exists := inv.Slots[key]; !exists {
						inv.Slots[key] = dnfrepo.ItemStack{ItemID: 2610030, Count: 9999}
					}
				}
				if err := g.Inventory.Save(context.Background(), inv); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.Background()
			beforeA, _, _ := g.AccountInventory.Load(ctx, "batch-account")
			beforeC, _, _ := g.Character.Load(ctx, "1")
			beforeI, _, _ := g.Inventory.Load(ctx, "1")
			_, err := s.commitCurrentEpicProductionCompound(ctx, session, plan, request, time.Now().UTC())
			if err == nil {
				t.Fatal("invalid batch succeeded")
			}
			if kind == "late_failure" && !errors.Is(err, errEpicBatchLateFailure) {
				t.Fatalf("late failure was not reached: %v", err)
			}
			afterA, _, _ := g.AccountInventory.Load(ctx, "batch-account")
			afterC, _, _ := g.Character.Load(ctx, "1")
			afterI, _, _ := g.Inventory.Load(ctx, "1")
			if !reflect.DeepEqual(beforeA, afterA) || !reflect.DeepEqual(beforeC, afterC) || !reflect.DeepEqual(beforeI, afterI) {
				t.Fatal("failed batch changed account, character or inventory")
			}
		})
	}
}

func TestEpicProductionBatchCostRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE for matching catalyst recipe")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := parseCurrentEpicProductionCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	g, s, session, _, request := epicBatchCostFixture(t, 10099775, 2004)
	plan, err := validateCurrentEpicProductionCompoundPlan(archive, items, catalog, request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Recipe.NeedCount != 1 || plan.Recipe.MinMakeCount != 1 || plan.Recipe.MaxMakeCount != 1 || plan.Recipe.MustNeedItemID != 10099775 || plan.Recipe.MustNeedItemCount != 1 || plan.Contribution != 1000 {
		t.Fatalf("matching-PVF recipe differs: %+v", plan.Recipe)
	}
	t.Logf("matching PVF catalyst=%d input=%d batch=%d must=%d x %d per unit", plan.Recipe.ItemID, request.Materials[0].ItemID, plan.Contribution, plan.Recipe.MustNeedItemID, plan.Recipe.MustNeedItemCount)
	result, err := s.commitCurrentEpicProductionCompound(context.Background(), session, plan, request, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	account, _, _ := g.AccountInventory.Load(context.Background(), session.accountID)
	if result.OutputCount != 1000 || account.Slots[dnfrepo.AccountSharedInventorySlotKey(364)].Count != 1004 {
		t.Fatalf("output=%d remaining souls=%d want output=1000 souls=1004", result.OutputCount, account.Slots[dnfrepo.AccountSharedInventorySlotKey(364)].Count)
	}
}
