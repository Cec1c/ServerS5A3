package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestNPCShopMaterialOnlyPriceDoesNotChargeSellValue(t *testing.T) {
	for _, price := range []string{"", "[price]\n0\n"} {
		t.Run(price, func(t *testing.T) {
			shop, items := mustCurrentNPCShopTestCatalogs(t)
			source := shop.source.(bridgePVFSource)
			source["stackable/material_exchange.stk"] = strings.Replace(source["stackable/material_exchange.stk"], "[price]\n100\n", price, 1)
			repos := mustCurrentNPCShopRepositories(t, 1000, map[string]dnfrepo.ItemStack{"0:121": {ItemID: 900, Count: 10}})
			s := currentNPCShopTestService(repos, shop, items)
			r, err := s.commitCurrentNPCShopBuy(context.Background(), &gameSession{selectedCharacterID: 19}, shop, items, currentNPCShopBuyRequest{ItemID: 602, Count: 3})
			if err != nil || r.GoldAfter != 1000 {
				t.Fatalf("material-only exchange charged sell value: result=%+v err=%v", r, err)
			}
			character, _, _ := repos.Character.Load(context.Background(), "19")
			if character.Stats["gold"] != 1000 {
				t.Fatal("material-only wallet changed")
			}
		})
	}
}

func TestNPCShopMixedCostSharedMaterialCommitsOrRollsBackTogether(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			shop, items := mustCurrentNPCShopTestCatalogs(t)
			source := shop.source.(bridgePVFSource)
			source["stackable/material_exchange.stk"] = strings.Replace(source["stackable/material_exchange.stk"], "900 2", "3033 2", 1)
			repos := mustCurrentNPCShopRepositories(t, 1000, nil)
			slot := currentDisjointWarehouseFixedSlots[3033]
			key := dnfrepo.AccountSharedInventorySlotKey(slot)
			account := dnfrepo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]dnfrepo.ItemStack{key: {ItemID: 3033, Count: 10}}}
			if err := repos.AccountInventory.Save(context.Background(), account); err != nil {
				t.Fatal(err)
			}
			if fail {
				repos.AccountAssets = rejectingCurrentNPCShopAccountAssets{inner: repos.AccountAssets, err: errors.New("shared mixed-cost commit fault")}
			}
			s := currentNPCShopTestService(repos, shop, items)
			_, err := s.commitCurrentNPCShopBuy(context.Background(), &gameSession{selectedCharacterID: 19}, shop, items, currentNPCShopBuyRequest{ItemID: 602, Count: 3})
			if (err != nil) != fail {
				t.Fatalf("failure=%t err=%v", fail, err)
			}
			character, _, _ := repos.Character.Load(context.Background(), "19")
			after, _, _ := repos.AccountInventory.Load(context.Background(), "account-1")
			bag, _, _ := repos.Inventory.Load(context.Background(), "19")
			gold, material, granted := int64(700), int64(4), int64(3)
			if fail {
				gold, material, granted = 1000, 10, 0
			}
			if character.Stats["gold"] != gold || after.Slots[key].Count != material {
				t.Fatalf("gold/material did not commit together: gold=%d material=%d", character.Stats["gold"], after.Slots[key].Count)
			}
			assertCurrentNPCShopInventoryAmount(t, bag, 602, granted)
		})
	}
}

func TestNPCShopMixedCostChargesBatchAndProjectsWallet(t *testing.T) {
	shop, items := mustCurrentNPCShopTestCatalogs(t)
	repos := mustCurrentNPCShopRepositories(t, 1000, map[string]dnfrepo.ItemStack{"0:121": {ItemID: 900, Count: 10}})
	s := currentNPCShopTestService(repos, shop, items)
	c := &bufferConn{}
	if err := s.handleCurrentNPCShopBuy(&gameSession{conn: c, selectedCharacterID: 19}, currentNPCShopBuyTestBody(602, 3, 1, 326)); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, c.write.Bytes())
	if len(ack.Body) < 5 || ack.Body[0] != 1 || binary.LittleEndian.Uint32(ack.Body[1:5]) != 700 {
		t.Fatalf("purchase ACK must include charged wallet: %x", ack.Body)
	}
	update, trailing := splitGameServerUpperPacket(t, rest)
	if update.Header.MsgID != 14 || len(trailing) != 0 {
		t.Fatalf("missing inventory update: %+v", update.Header)
	}
	character, _, _ := repos.Character.Load(context.Background(), "19")
	bag, _, _ := repos.Inventory.Load(context.Background(), "19")
	if character.Stats["gold"] != 700 || bag.Slots["0:121"].Count != 4 {
		t.Fatalf("gold=%d material=%+v", character.Stats["gold"], bag.Slots["0:121"])
	}
	assertCurrentNPCShopInventoryAmount(t, bag, 602, 3)
}

func TestNPCShopMixedCostFailurePreservesAllAssets(t *testing.T) {
	for _, tc := range []struct {
		name           string
		gold, material int64
		failCommit     bool
	}{
		{"insufficient_gold", 299, 10, false}, {"insufficient_material", 1000, 5, false}, {"commit_failure", 1000, 10, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shop, items := mustCurrentNPCShopTestCatalogs(t)
			repos := mustCurrentNPCShopRepositories(t, tc.gold, map[string]dnfrepo.ItemStack{"0:121": {ItemID: 900, Count: tc.material}})
			beforeChar, _, _ := repos.Character.Load(context.Background(), "19")
			beforeBag, _, _ := repos.Inventory.Load(context.Background(), "19")
			if tc.failCommit {
				repos.AccountAssets = rejectingCurrentNPCShopAccountAssets{inner: repos.AccountAssets, err: errors.New("mixed cost commit fault")}
			}
			s := currentNPCShopTestService(repos, shop, items)
			_, err := s.commitCurrentNPCShopBuy(context.Background(), &gameSession{selectedCharacterID: 19}, shop, items, currentNPCShopBuyRequest{ItemID: 602, Count: 3, ActorContext: 2})
			if err == nil {
				t.Fatal("purchase unexpectedly succeeded")
			}
			afterChar, _, _ := repos.Character.Load(context.Background(), "19")
			afterBag, _, _ := repos.Inventory.Load(context.Background(), "19")
			if !reflect.DeepEqual(beforeChar, afterChar) || !reflect.DeepEqual(beforeBag, afterBag) {
				t.Fatal("failed purchase changed persistent assets")
			}
		})
	}
}

func TestRealPVFNPCShopLukeMixedPriceDebitsGoldAndMonolium(t *testing.T) {
	shop, items := realPVF535(t)
	// All three finished Luke items named in the user's DOC-100 reproduction.
	for _, tc := range []struct {
		name   string
		itemID uint32
	}{
		{"王冠非冠", 100344527},
		{"黑暗祭礼", 100390011},
		{"王座本源", 100352839},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pricing, err := resolveCurrentNPCShopPricing(shop, items, tc.itemID)
			if err != nil || pricing.BuyGold != 2500000 || pricing.NeedMaterialItem != 10159623 || pricing.NeedMaterialCount != 100 {
				t.Fatalf("real Luke price changed: %+v err=%v", pricing, err)
			}
			for _, gold := range []int64{3000000, 2499999} {
				repos := mustCurrentNPCShopRepositories(t, gold, map[string]dnfrepo.ItemStack{"0:121": {ItemID: 10159623, Count: 150}})
				beforeChar, _, _ := repos.Character.Load(context.Background(), "19")
				beforeBag, _, _ := repos.Inventory.Load(context.Background(), "19")
				s := currentNPCShopTestService(repos, shop, items)
				result, err := s.commitCurrentNPCShopBuy(context.Background(), &gameSession{selectedCharacterID: 19}, shop, items, currentNPCShopBuyRequest{ItemID: tc.itemID, Count: 1})
				character, _, _ := repos.Character.Load(context.Background(), "19")
				bag, _, _ := repos.Inventory.Load(context.Background(), "19")
				if gold < 2500000 {
					if err == nil || !reflect.DeepEqual(beforeChar, character) || !reflect.DeepEqual(beforeBag, bag) {
						t.Fatalf("insufficient-gold purchase must reject without changing assets: err=%v", err)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				if result.GoldAfter != 500000 || character.Stats["gold"] != 500000 || bag.Slots["0:121"].Count != 50 {
					t.Fatalf("gold result=%d stored=%d material=%d", result.GoldAfter, character.Stats["gold"], bag.Slots["0:121"].Count)
				}
				assertCurrentNPCShopInventoryAmount(t, bag, int64(tc.itemID), 1)
			}
		})
	}
}
