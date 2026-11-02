package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func crystalGoldShop(t *testing.T, itemID uint32, limit int64) (*currentNPCShopCatalog, *pvfDungeonDropCatalog) {
	t.Helper()
	source := bridgePVFSource{
		"monster/monster.lst": "", "equipment/equipment.lst": "",
		"stackable/stackable.lst":  fmt.Sprintf("%d `cube.stk`\n", itemID),
		"stackable/cube.stk":       fmt.Sprintf("[stackable type]\n`[material]` 2\n[price]\n100\n[stack limit]\n%d\n", limit),
		"itemshop/itemshop.lst":    "1 `shop.shp`\n",
		"itemshop/shop.shp":        fmt.Sprintf("[NPC]\n326\n[item list]\n%d\n[/item list]\n", itemID),
		"equipment/pricetable.tbl": "[rate]\n200 150 30\n",
	}
	shop, err := newCurrentNPCShopCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return shop, items
}

func fullCrystalMaterialBag() map[string]dnfrepo.ItemStack {
	bag := make(map[string]dnfrepo.ItemStack)
	for slot := 3; slot <= 176; slot++ {
		if slot > 8 && slot < 121 {
			continue
		}
		bag[fmt.Sprintf("0:%d", slot)] = dnfrepo.ItemStack{ItemID: 900, Count: 1}
	}
	return bag
}

// Exercise the real handler and response, including the purchase quantity in
// ACK and the full merged account balance in the authoritative inventory row.
func TestNPCShopGoldCrystalsUseAccountWarehouseWithFullBackpack(t *testing.T) {
	for _, id := range []uint32{3033, 3034, 3035, 3036, 3037, 3262} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			shop, items := crystalGoldShop(t, id, 0)
			bag := fullCrystalMaterialBag()
			repos := mustCurrentNPCShopRepositories(t, 1000, bag)
			slot := currentDisjointWarehouseFixedSlots[id]
			key := dnfrepo.AccountSharedInventorySlotKey(slot)
			ctx := context.Background()
			if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]dnfrepo.ItemStack{key: {ItemID: int64(id), Count: 7}}}); err != nil {
				t.Fatal(err)
			}
			s := currentNPCShopTestService(repos, shop, items)
			conn := &bufferConn{}
			if err := s.handleCurrentNPCShopBuy(&gameSession{conn: conn, selectedCharacterID: 19}, currentNPCShopBuyTestBody(id, 3, 1, 326)); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Body[0] != 1 || int16(binary.LittleEndian.Uint16(ack.Body[17:19])) != slot || binary.LittleEndian.Uint32(ack.Body[23:27]) != 3 {
				t.Fatalf("ACK=%x", ack.Body)
			}
			update, rest := splitGameServerUpperPacket(t, rest)
			if len(rest) != 0 {
				t.Fatal("unexpected extra response")
			}
			assertCurrentNPCShopIncrementalRows(t, update.Body, 700, slot, id, 10)
			account, _, err := repos.AccountInventory.Load(ctx, "account-1")
			if err != nil || account.Slots[key].Count != 10 {
				t.Fatalf("account=%+v err=%v", account, err)
			}
			inventory, _, _ := repos.Inventory.Load(ctx, "19")
			if !reflect.DeepEqual(inventory.Slots, bag) {
				t.Fatal("warehouse purchase changed full backpack")
			}
			character, _, _ := repos.Character.Load(ctx, "19")
			if character.Stats["gold"] != 700 {
				t.Fatal("gold not charged once")
			}
		})
	}
}

func TestNPCShopGoldCrystalOverflowAndRollback(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		storedID, stored, limit, gold int64
		full, failSave, wantFail      bool
		wantWarehouse, wantBag        int64
	}{
		{name: "new warehouse", gold: 1000, wantWarehouse: 3},
		{name: "partial overflow", storedID: 3037, stored: 8, limit: 10, gold: 1000, wantWarehouse: 10, wantBag: 1},
		{name: "wire limit", storedID: 3037, stored: math.MaxInt32 - 1, gold: 1000, wantWarehouse: math.MaxInt32, wantBag: 2},
		{name: "foreign slot preserved", storedID: 3033, stored: 8, gold: 1000, wantWarehouse: 8, wantBag: 3},
		{name: "full overflow rolls back", storedID: 3037, stored: 8, limit: 10, gold: 1000, full: true, wantFail: true},
		{name: "insufficient gold", storedID: 3037, stored: 8, gold: 299, wantFail: true},
		{name: "account save fails after gold save", storedID: 3037, stored: 8, gold: 1000, failSave: true, wantFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shop, items := crystalGoldShop(t, 3037, tc.limit)
			var bag map[string]dnfrepo.ItemStack
			if tc.full {
				bag = fullCrystalMaterialBag()
			}
			repos := mustCurrentNPCShopRepositories(t, tc.gold, bag)
			ctx := context.Background()
			account := dnfrepo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]dnfrepo.ItemStack{}}
			if tc.storedID != 0 {
				account.Slots["0:358"] = dnfrepo.ItemStack{ItemID: tc.storedID, Count: tc.stored}
			}
			if err := repos.AccountInventory.Save(ctx, account); err != nil {
				t.Fatal(err)
			}
			beforeA, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			beforeC, _, _ := repos.Character.Load(ctx, "19")
			beforeI, _, _ := repos.Inventory.Load(ctx, "19")
			called := false
			if tc.failSave {
				repos.AccountAssets = crystalFailAccountAssets{AccountCharacterAssetUnitOfWork: repos.AccountAssets, called: &called}
			}
			s := currentNPCShopTestService(repos, shop, items)
			_, err := s.commitCurrentNPCShopBuy(ctx, &gameSession{selectedCharacterID: 19}, shop, items, currentNPCShopBuyRequest{ItemID: 3037, Count: 3, ShopContext: 1, ActorContext: 326})
			if (err != nil) != tc.wantFail {
				t.Fatalf("err=%v wantFail=%v", err, tc.wantFail)
			}
			a, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			c, _, _ := repos.Character.Load(ctx, "19")
			i, _, _ := repos.Inventory.Load(ctx, "19")
			if tc.failSave && !called {
				t.Fatal("account save fault not reached")
			}
			if tc.wantFail {
				if !reflect.DeepEqual(a, beforeA) || !reflect.DeepEqual(c, beforeC) || !reflect.DeepEqual(i, beforeI) {
					t.Fatal("failed purchase changed assets")
				}
				return
			}
			if c.Stats["gold"] != tc.gold-300 || a.Slots["0:358"].Count != tc.wantWarehouse {
				t.Fatalf("gold=%d warehouse=%+v", c.Stats["gold"], a)
			}
			if tc.storedID == 3033 && a.Slots["0:358"].ItemID != 3033 {
				t.Fatal("foreign item evicted")
			}
			assertCurrentNPCShopInventoryAmount(t, i, 3037, tc.wantBag)
		})
	}
}

type crystalFailAccountInventory struct {
	dnfrepo.AccountInventoryRepository
	called *bool
}

func (r crystalFailAccountInventory) Save(context.Context, dnfrepo.AccountInventoryRecord) error {
	*r.called = true
	return errors.New("injected account inventory save failure")
}

type crystalFailAccountAssets struct {
	dnfrepo.AccountCharacterAssetUnitOfWork
	called *bool
}

func (u crystalFailAccountAssets) WithinAccountCharacterLedgerAssets(ctx context.Context, account, character string, apply func(dnfrepo.AccountRepository, dnfrepo.AccountInventoryRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
	return u.AccountCharacterAssetUnitOfWork.(dnfrepo.AccountCharacterLedgerAssetUnitOfWork).WithinAccountCharacterLedgerAssets(ctx, account, character, func(a dnfrepo.AccountRepository, ai dnfrepo.AccountInventoryRepository, c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, e dnfrepo.EquipmentRepository) error {
		return apply(a, crystalFailAccountInventory{ai, u.called}, c, i, e)
	})
}
