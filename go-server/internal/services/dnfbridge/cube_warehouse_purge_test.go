package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type cubeWarehouseFieldScopedItems struct {
	dnfrepo.AccountCharacterItemUnitOfWork
}

func (u cubeWarehouseFieldScopedItems) WithinAccountCharacterItems(
	ctx context.Context,
	accountID string,
	characterID string,
	apply func(dnfrepo.AccountInventoryRepository, dnfrepo.InventoryRepository) error,
) error {
	return u.AccountCharacterItemUnitOfWork.WithinAccountCharacterItems(ctx, accountID, characterID, func(
		accounts dnfrepo.AccountInventoryRepository,
		inventory dnfrepo.InventoryRepository,
	) error {
		return apply(accounts, epicPieceFieldScopedInventory{InventoryRepository: inventory})
	})
}

// MySQL distinguishes a character's slots and warehouse collections. The
// memory store normally saves both maps together, which would hide a purge that
// removes a warehouse crystal but asks the store to persist only Slots.
func TestCubeWarehousePurgePersistsWarehouseRemovalWithFieldScopedStore(t *testing.T) {
	const (
		accountID   = "account-1"
		characterID = "19"
		cubeItemID  = int64(3037)
		materialID  = int64(3166)
	)

	ctx := context.Background()
	repositories, catalog := mustCurrentDisjointTestAssets(t, nil)
	definition, err := catalog.ResolveItem(uint32(cubeItemID))
	if err != nil {
		t.Fatal(err)
	}
	if definition.StackLimit <= 12 {
		t.Fatalf("cube stack limit=%d, test needs room for partial transfer", definition.StackLimit)
	}

	repositories.AccountItems = cubeWarehouseFieldScopedItems{AccountCharacterItemUnitOfWork: repositories.AccountItems}
	service := currentDisjointTestService(repositories, catalog)
	session := &gameSession{
		connID:              "cube-warehouse-purge",
		accountID:           accountID,
		accountFromToken:    true,
		selectedCharacterID: 19,
	}
	if got := service.accountIDForSession(session); got != accountID {
		t.Fatalf("session account=%q want=%q", got, accountID)
	}
	character, found, err := repositories.Character.Load(ctx, characterID)
	if err != nil {
		t.Fatal(err)
	}
	if !found || character.AccountID != accountID {
		t.Fatalf("character/account binding found=%t account=%q want=%q", found, character.AccountID, accountID)
	}

	for _, test := range []struct {
		name                         string
		accountCount, bag, warehouse int64
		wantAccount, wantRemainder   int64
	}{
		{name: "warehouse_only", warehouse: 7, wantAccount: 7},
		{name: "mixed_bag_and_warehouse", bag: 3, warehouse: 7, wantAccount: 10},
		{
			name:          "partial_at_stack_limit",
			accountCount:  definition.StackLimit - 8,
			bag:           5,
			warehouse:     7,
			wantAccount:   definition.StackLimit,
			wantRemainder: 4,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixedSlot, mapped := dnfrepo.AccountWarehouseSlotForItem(cubeItemID)
			if !mapped {
				t.Fatalf("cube item=%d has no account warehouse slot", cubeItemID)
			}
			accountSlots := make(map[string]dnfrepo.ItemStack)
			if test.accountCount != 0 {
				accountSlots[dnfrepo.AccountSharedInventorySlotKey(fixedSlot)] = dnfrepo.ItemStack{
					ItemID: cubeItemID,
					Count:  test.accountCount,
				}
			}
			if err := repositories.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
				AccountID: accountID,
				Slots:     accountSlots,
			}); err != nil {
				t.Fatal(err)
			}

			preserved := dnfrepo.ItemStack{
				ItemID: materialID,
				Count:  9,
				Extra:  map[string]string{"preserve": "yes"},
			}
			seed := dnfrepo.InventoryRecord{
				CharacterID: characterID,
				Slots:       map[string]dnfrepo.ItemStack{"0:121": preserved},
				Warehouse:   map[string]dnfrepo.ItemStack{"2:1": preserved},
			}
			if test.bag != 0 {
				seed.Slots["0:122"] = dnfrepo.ItemStack{ItemID: cubeItemID, Count: test.bag}
			}
			if test.warehouse != 0 {
				seed.Warehouse["2:0"] = dnfrepo.ItemStack{ItemID: cubeItemID, Count: test.warehouse}
			}
			if err := repositories.Inventory.Save(ctx, seed); err != nil {
				t.Fatal(err)
			}

			initialTotal := test.accountCount + test.bag + test.warehouse
			wantRows := 0
			if test.bag != 0 {
				wantRows++
			}
			if test.warehouse != 0 {
				wantRows++
			}
			for attempt := 0; attempt < 3; attempt++ {
				purged, err := service.purgeCurrentInventoryCubes(ctx, session, "test_cube_warehouse_purge")
				if err != nil {
					t.Fatalf("attempt=%d purge: %v", attempt, err)
				}
				wantPurged := 0
				if attempt == 0 {
					wantPurged = wantRows
				}
				if purged != wantPurged {
					t.Fatalf("attempt=%d purged=%d want=%d", attempt, purged, wantPurged)
				}

				inventory, found, err := repositories.Inventory.Load(ctx, characterID)
				if err != nil || !found {
					t.Fatalf("attempt=%d inventory found=%t error=%v", attempt, found, err)
				}
				account, found, err := repositories.AccountInventory.Load(ctx, accountID)
				if err != nil || !found {
					t.Fatalf("attempt=%d account inventory found=%t error=%v", attempt, found, err)
				}

				fixed, bag, warehouse := cubeWarehousePurgeCounts(account, inventory, cubeItemID, fixedSlot)
				if got := fixed + bag + warehouse; got != initialTotal {
					t.Fatalf("attempt=%d crystal total=%d want=%d (fixed=%d bag=%d warehouse=%d)", attempt, got, initialTotal, fixed, bag, warehouse)
				}
				if fixed != test.wantAccount {
					t.Fatalf("attempt=%d account fixed slot=%d want=%d", attempt, fixed, test.wantAccount)
				}
				if got := bag + warehouse; got != test.wantRemainder {
					t.Fatalf("attempt=%d character remainder=%d want=%d (bag=%d warehouse=%d)", attempt, got, test.wantRemainder, bag, warehouse)
				}
				if !reflect.DeepEqual(inventory.Slots["0:121"], preserved) {
					t.Fatalf("attempt=%d bag material changed: %+v", attempt, inventory.Slots["0:121"])
				}
				if !reflect.DeepEqual(inventory.Warehouse["2:1"], preserved) {
					t.Fatalf("attempt=%d warehouse material changed: %+v", attempt, inventory.Warehouse["2:1"])
				}
			}
		})
	}
}

func cubeWarehousePurgeCounts(
	account dnfrepo.AccountInventoryRecord,
	inventory dnfrepo.InventoryRecord,
	itemID int64,
	fixedSlot int16,
) (fixed, bag, warehouse int64) {
	if stack, exists := account.Slots[dnfrepo.AccountSharedInventorySlotKey(fixedSlot)]; exists && stack.ItemID == itemID {
		fixed = stack.Count
	}
	for _, stack := range inventory.Slots {
		if stack.ItemID == itemID {
			bag += stack.Count
		}
	}
	for _, stack := range inventory.Warehouse {
		if stack.ItemID == itemID {
			warehouse += stack.Count
		}
	}
	return fixed, bag, warehouse
}
