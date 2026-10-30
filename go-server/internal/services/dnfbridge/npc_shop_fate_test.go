package dnfbridge

import (
	"context"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raidcurrency"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestNPCShopFatePurchaseUsesSharedLedgerAndPreservesWeeklyEarned(t *testing.T) {
	ctx := context.Background()
	shop, items := mustCurrentNPCShopTestCatalogs(t)
	g := mustCurrentNPCShopRepositories(t, 1000, nil)
	a, _, _ := g.Account.Load(ctx, "account-1")
	if _, _, err := raidcurrency.Credit(&a, 100, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := g.Account.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	s := currentNPCShopTestService(g, shop, items)
	pricing, err := resolveCurrentNPCShopPricing(shop, items, 602)
	if err != nil {
		t.Fatal(err)
	}
	pricing.NeedMaterialItem, pricing.NeedMaterialCount = raidcurrency.FateItemID, 40
	pricing.ExchangeGold = 0
	se := &gameSession{selectedCharacterID: 19}
	result, err := s.commitCurrentNPCShopMaterialExchange(ctx, se, pricing, currentNPCShopBuyRequest{ItemID: 602, Count: 1})
	if err != nil || result.CostItemID != raidcurrency.FateItemID || result.CostItemNewCount != 60 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	a, _, _ = g.Account.Load(ctx, "account-1")
	state, err := raidcurrency.Read(a, time.Now())
	if err != nil || state.Balance != 60 || state.Earned != 100 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	inv, _, _ := g.Inventory.Load(ctx, "19")
	assertCurrentNPCShopInventoryAmount(t, inv, 602, 1)
	for _, stack := range inv.Slots {
		if stack.ItemID == int64(raidcurrency.FateItemID) {
			t.Fatal("currency returned to bag")
		}
	}
	// Another character uses the same committed account balance.
	if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "20", AccountID: "account-1", Slot: 1, Stats: map[string]int64{"gold": 1000}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "20", Slots: map[string]repo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	se.selectedCharacterID = 20
	result, err = s.commitCurrentNPCShopMaterialExchange(ctx, se, pricing, currentNPCShopBuyRequest{ItemID: 602, Count: 1})
	if err != nil || result.CostItemNewCount != 20 {
		t.Fatalf("second character=%+v err=%v", result, err)
	}
	before, _, _ := g.Account.Load(ctx, "account-1")
	beforeInv, _, _ := g.Inventory.Load(ctx, "20")
	if _, err = s.commitCurrentNPCShopMaterialExchange(ctx, se, pricing, currentNPCShopBuyRequest{ItemID: 602, Count: 1}); err == nil {
		t.Fatal("insufficient wallet accepted")
	}
	a, _, _ = g.Account.Load(ctx, "account-1")
	inv, _, _ = g.Inventory.Load(ctx, "20")
	if !reflect.DeepEqual(a, before) || !reflect.DeepEqual(inv, beforeInv) {
		t.Fatal("failed debit changed assets")
	}
}

func TestNPCShopFateFullBagRollsBackDebitAndPurchaseLimit(t *testing.T) {
	ctx := context.Background()
	shop, items := mustCurrentNPCShopTestCatalogs(t)
	// Product 602 belongs to the material tab, not the consumable tab.
	full := make(map[string]repo.ItemStack)
	// Materials can also use the six shared quick slots when their tab is full.
	for slot := 3; slot <= 8; slot++ {
		full["0:"+strconv.Itoa(slot)] = repo.ItemStack{ItemID: 999, Count: 1}
	}
	for slot := 121; slot <= 176; slot++ {
		full["0:"+strconv.Itoa(slot)] = repo.ItemStack{ItemID: 999, Count: 1}
	}
	g := mustCurrentNPCShopRepositories(t, 1000, full)
	a, _, _ := g.Account.Load(ctx, "account-1")
	if _, _, err := raidcurrency.Credit(&a, 100, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := g.Account.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	s := currentNPCShopTestService(g, shop, items)
	pricing, err := resolveCurrentNPCShopPricing(shop, items, 602)
	if err != nil {
		t.Fatal(err)
	}
	pricing.NeedMaterialItem, pricing.NeedMaterialCount = raidcurrency.FateItemID, 40
	pricing.PurchaseLimitScope, pricing.PurchaseLimitValue, pricing.PurchaseLimitReset = "character", 1, true
	beforeInv, _, _ := g.Inventory.Load(ctx, "19")
	beforeChar, _, _ := g.Character.Load(ctx, "19")
	_, err = s.commitCurrentNPCShopMaterialExchange(ctx, &gameSession{selectedCharacterID: 19}, pricing, currentNPCShopBuyRequest{ItemID: 602, Count: 1})
	if err == nil {
		t.Fatal("full bag accepted purchase")
	}
	after, _, _ := g.Account.Load(ctx, "account-1")
	inv, _, _ := g.Inventory.Load(ctx, "19")
	char, _, _ := g.Character.Load(ctx, "19")
	if !reflect.DeepEqual(a, after) || !reflect.DeepEqual(inv, beforeInv) || !reflect.DeepEqual(char, beforeChar) {
		t.Fatal("failed grant consumed wallet, quota or gold")
	}
}
