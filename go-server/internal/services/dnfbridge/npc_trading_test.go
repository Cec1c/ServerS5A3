package dnfbridge

import (
	"context"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

func TestTradingCatalogUsesNPCMembershipAndMaterialTransaction(t *testing.T) {
	oldShop, _ := mustCurrentNPCShopTestCatalogs(t)
	source := oldShop.source.(bridgePVFSource)
	source["etc/tradingsystem.etc"] = "[trade item list]\n476\n[item list]\n601 100 1.25 1\n[/item list]\n[shop item]\n602\n[/shop item]\n[/trade item list]\n"
	shop, err := newCurrentNPCShopCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	price, err := resolveCurrentNPCShopActorPricing(shop, items, 601, 476)
	if err != nil || !price.Buyable || price.BuyGold != 125 || price.SellGold != 100 {
		t.Fatalf("trading price=%+v err=%v", price, err)
	}
	wrong, err := resolveCurrentNPCShopActorPricing(shop, items, 601, 2)
	if err != nil || wrong.Buyable {
		t.Fatalf("wrong NPC price=%+v err=%v", wrong, err)
	}
	repos := mustCurrentNPCShopRepositories(t, 1000, map[string]dnfrepo.ItemStack{"0:121": {ItemID: 900, Count: 10}})
	service := currentNPCShopTestService(repos, shop, items)
	session := &gameSession{selectedCharacterID: 19}
	result, err := service.commitCurrentNPCShopBuy(context.Background(), session, shop, items, currentNPCShopBuyRequest{ItemID: 601, Count: 3, ActorContext: 476})
	if err != nil || result.GoldAfter != 625 {
		t.Fatalf("buy=%+v err=%v", result, err)
	}
	result, err = service.commitCurrentNPCShopBuy(context.Background(), session, shop, items, currentNPCShopBuyRequest{ItemID: 602, Count: 3, ActorContext: 476})
	// The shop-item definition also declares an explicit price of 100 each.
	if err != nil || result.CostItemNewCount != 4 || result.GoldAfter != 325 {
		t.Fatalf("exchange=%+v err=%v", result, err)
	}
	inventory, _, _ := repos.Inventory.Load(context.Background(), "19")
	assertCurrentNPCShopInventoryAmount(t, inventory, 601, 3)
	assertCurrentNPCShopInventoryAmount(t, inventory, 602, 3)
}

func TestRealPVFJonathanBuysAndRefinesTimeStone(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	source, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	shop, err := newCurrentNPCShopCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	repos := mustCurrentNPCShopRepositories(t, 1000000, map[string]dnfrepo.ItemStack{"0:121": {ItemID: 10158700, Count: 30}})
	service := currentNPCShopTestService(repos, shop, items)
	session := &gameSession{selectedCharacterID: 19}
	result, err := service.commitCurrentNPCShopBuy(context.Background(), session, shop, items, currentNPCShopBuyRequest{ItemID: 10158700, Count: 4, ActorContext: 476})
	if err != nil || result.GoldAfter != 966668 {
		t.Fatalf("time stone=%+v err=%v", result, err)
	}
	result, err = service.commitCurrentNPCShopBuy(context.Background(), session, shop, items, currentNPCShopBuyRequest{ItemID: 10158964, Count: 3, ActorContext: 476})
	if err != nil || result.CostItemNewCount != 4 || result.GoldAfter != 966668 {
		t.Fatalf("refine=%+v err=%v", result, err)
	}
	inventory, _, _ := repos.Inventory.Load(context.Background(), "19")
	assertCurrentNPCShopInventoryAmount(t, inventory, 10158700, 4)
	assertCurrentNPCShopInventoryAmount(t, inventory, 10158964, 3)
}
