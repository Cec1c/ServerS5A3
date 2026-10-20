package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The ordinary memory store saves the whole record, which hid the missing
// warehouse dirty field. This adapter honors fields like the MySQL store.
type epicPieceFieldScopedInventory struct {
	dnfrepo.InventoryRepository
}

func (r epicPieceFieldScopedInventory) SaveFields(ctx context.Context, next dnfrepo.InventoryRecord, fields ...dnfrepo.InventoryField) error {
	current, _, err := r.Load(ctx, next.CharacterID)
	if err != nil {
		return err
	}
	current.CharacterID = next.CharacterID
	current.UpdatedAt = next.UpdatedAt
	for _, field := range fields {
		switch field {
		case dnfrepo.InventoryFieldSlots:
			current.Slots = next.Slots
		case dnfrepo.InventoryFieldWarehouse:
			current.Warehouse = next.Warehouse
		}
	}
	return r.Save(ctx, dnfrepo.CloneInventory(current))
}

type epicPieceFieldScopedItems struct {
	dnfrepo.CharacterItemUnitOfWork
}

func (u epicPieceFieldScopedItems) WithinCharacterItems(ctx context.Context, id string, apply func(dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
	return u.CharacterItemUnitOfWork.WithinCharacterItems(ctx, id, func(inventory dnfrepo.InventoryRepository, equipment dnfrepo.EquipmentRepository) error {
		return apply(epicPieceFieldScopedInventory{inventory}, equipment)
	})
}

func TestEpicPieceInventoryPurgePersistsWarehouseRemovalWithFieldScopedStore(t *testing.T) {
	for _, test := range []struct {
		name           string
		bag, warehouse int64
	}{
		{name: "warehouse_only", warehouse: 7},
		{name: "bag_and_warehouse", bag: 3, warehouse: 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			service, _, repositories := prepareCurrentEpicPieceDropTest(t)
			repositories.CharacterItems = epicPieceFieldScopedItems{repositories.CharacterItems}
			service.repositoryProvider = func() (dnfrepo.Group, bool) { return repositories, true }
			material := dnfrepo.ItemStack{ItemID: int64(epicPieceBookTestMaterial), Count: 9, Extra: map[string]string{"preserve": "yes"}}
			seed := dnfrepo.InventoryRecord{
				CharacterID: epicPieceBookTestCharacterA,
				Slots:       map[string]dnfrepo.ItemStack{"0:121": material},
				Warehouse:   map[string]dnfrepo.ItemStack{"2:1": material},
			}
			if test.bag != 0 {
				seed.Slots["0:122"] = dnfrepo.ItemStack{ItemID: int64(epicPieceBookTestPieceA), Count: test.bag}
			}
			seed.Warehouse["2:0"] = dnfrepo.ItemStack{ItemID: int64(epicPieceBookTestPieceA), Count: test.warehouse}
			if err := repositories.Inventory.Save(ctx, seed); err != nil {
				t.Fatal(err)
			}
			session := &gameSession{connID: "epic-piece-warehouse-purge", selectedCharacterID: 99}
			wantRows := 1
			if test.bag != 0 {
				wantRows++
			}
			for attempt := 0; attempt < 2; attempt++ {
				purged, err := service.purgeCurrentInventoryEpicPieces(ctx, session, "test_warehouse_purge")
				if err != nil {
					t.Fatal(err)
				}
				want := wantRows
				if attempt != 0 {
					want = 0
				}
				if purged != want {
					t.Fatalf("attempt=%d purged=%d want=%d", attempt, purged, want)
				}
				inventory := loadCurrentEpicPieceTestInventory(t, ctx, repositories, epicPieceBookTestCharacterA)
				if _, found := inventory.Slots["0:122"]; found {
					t.Fatal("bag fragment survived")
				}
				if _, found := inventory.Warehouse["2:0"]; found {
					t.Fatal("warehouse fragment survived; next login can credit it again")
				}
				if !reflect.DeepEqual(inventory.Slots["0:121"], material) || !reflect.DeepEqual(inventory.Warehouse["2:1"], material) {
					t.Fatalf("ordinary materials changed: %+v", inventory)
				}
				account, found, err := repositories.Account.Load(ctx, epicPieceBookTestAccountID)
				if err != nil || !found {
					t.Fatalf("account found=%t error=%v", found, err)
				}
				if got := dnfrepo.AccountEpicPieceBalances(account)[epicPieceBookTestPieceA]; got != uint32(test.bag+test.warehouse) {
					t.Fatalf("attempt=%d ledger=%d want=%d", attempt, got, test.bag+test.warehouse)
				}
			}
		})
	}
}
