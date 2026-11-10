package dnfbridge

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raidcurrency"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func pr55NPCShopMaterialCatalogs(t *testing.T, material uint32) (*currentNPCShopCatalog, *pvfDungeonDropCatalog) {
	t.Helper()
	shop, _ := mustCurrentNPCShopTestCatalogs(t)
	source := shop.source.(bridgePVFSource)
	source["stackable/material_exchange.stk"] = strings.ReplaceAll(
		strings.Replace(source["stackable/material_exchange.stk"], "900 2", fmt.Sprintf("%d 2", material), 1),
		"[price]\n100\n",
		"[price]\n0\n",
	)
	updatedShop, err := newCurrentNPCShopCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	updatedItems, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return updatedShop, updatedItems
}

func pr55BattlePassMaterialCounter(t *testing.T, character dnfrepo.CharacterRecord, wantParam int64) int64 {
	t.Helper()
	for key, value := range character.Stats {
		_, kind, param, ok := dnfrepo.ReadBattlePassCounterKey(key)
		if ok && kind == 28 && param == wantParam {
			return value
		}
	}
	return 0
}

func TestPR55NPCShopFateMaterialPrivateType28Counters(t *testing.T) {
	for _, material := range []uint32{10158964, uint32(dnfrepo.BattlePassFateItemID)} {
		t.Run(fmt.Sprint(material), func(t *testing.T) {
			ctx := context.Background()
			shop, items := pr55NPCShopMaterialCatalogs(t, material)
			slots := map[string]dnfrepo.ItemStack{}
			if material == 10158964 {
				slots["0:50"] = dnfrepo.ItemStack{ItemID: 10158964, Count: 6}
			}
			repos := mustCurrentNPCShopRepositories(t, 1000, slots)
			if material == uint32(dnfrepo.BattlePassFateItemID) {
				account, found, err := repos.Account.Load(ctx, "account-1")
				if err != nil || !found {
					t.Fatalf("account found=%t err=%v", found, err)
				}
				if _, _, err := raidcurrency.Credit(&account, 6, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				if err := repos.Account.Save(ctx, account); err != nil {
					t.Fatal(err)
				}
			}
			service := currentNPCShopTestService(repos, shop, items)
			pricing, err := resolveCurrentNPCShopPricing(shop, items, 602)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.commitCurrentNPCShopMaterialExchange(ctx, &gameSession{selectedCharacterID: 19}, pricing, currentNPCShopBuyRequest{ItemID: 602, Count: 1}); err != nil {
				t.Fatal(err)
			}
			character, _, _ := repos.Character.Load(ctx, "19")
			if got := pr55BattlePassMaterialCounter(t, character, int64(material)); got != 1 {
				t.Fatalf("type28 param=%d count=%d want=1 stats=%v", material, got, character.Stats)
			}
			bag, _, _ := repos.Inventory.Load(ctx, "19")
			assertCurrentNPCShopInventoryAmount(t, bag, 602, 1)
			if material == 10158964 {
				assertCurrentNPCShopInventoryAmount(t, bag, 10158964, 4)
			} else {
				account, _, _ := repos.Account.Load(ctx, "account-1")
				state, err := raidcurrency.Read(account, time.Now().UTC())
				if err != nil || state.Balance != 4 {
					t.Fatalf("fate balance=%d err=%v want=4", state.Balance, err)
				}
			}
		})
	}
}

func TestPR55NPCShopColorlessMaterialRecordsNarrowSpend(t *testing.T) {
	ctx := context.Background()
	shop, items := pr55NPCShopMaterialCatalogs(t, uint32(dnfrepo.ColorlessCubeItemID))
	repos := mustCurrentNPCShopRepositories(t, 1000, nil)
	if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
		AccountID: "account-1",
		Slots:     map[string]dnfrepo.ItemStack{"0:358": {ItemID: dnfrepo.ColorlessCubeItemID, Count: 5}},
	}); err != nil {
		t.Fatal(err)
	}
	service := currentNPCShopTestService(repos, shop, items)
	pricing, err := resolveCurrentNPCShopPricing(shop, items, 602)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.commitCurrentNPCShopMaterialExchange(ctx, &gameSession{selectedCharacterID: 19}, pricing, currentNPCShopBuyRequest{ItemID: 602, Count: 1}); err != nil {
		t.Fatal(err)
	}
	summary, err := repos.Activity.Summary(ctx, dnfrepo.ActivityQuery{AccountID: "account-1"})
	if err != nil || summary.Metrics[dnfrepo.ActivityColorless] != 2 {
		t.Fatalf("colorless summary=%+v err=%v", summary, err)
	}
	account, _, _ := repos.AccountInventory.Load(ctx, "account-1")
	if account.Slots["0:358"].Count != 3 {
		t.Fatalf("colorless stack=%+v want count=3", account.Slots["0:358"])
	}
	var bought int64
	bag, _, _ := repos.Inventory.Load(ctx, "19")
	for _, stack := range bag.Slots {
		if stack.ItemID == 602 {
			bought += stack.Count
		}
	}
	if bought != 1 {
		t.Fatalf("purchased item 602=%d want=1", bought)
	}
}
