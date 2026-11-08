package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	"longheng.io/server/internal/modules/dnf/npcshop"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestRealPVFAikenTitleExtractionRejected(t *testing.T) {
	jobs, items := nativeExtractionRealCatalogs(t)
	ctx := context.Background()
	for _, kind := range []byte{1, 2, 4} {
		tool := nativeExtractionChooseTool(t, jobs, kind)
		for _, id := range []int64{currentAikenSupportTitleOwnedID, currentAikenSupportTitleCombatID} {
			for _, batch := range []bool{false, true} {
				t.Run(fmt.Sprintf("job%d/item%d/batch%t", kind, id, batch), func(t *testing.T) {
					s, p, g, _, _ := nativeExtractionRealFixture(t, jobs, items, kind, 2, 100, tool)
					bag, _, _ := g.Inventory.Load(ctx, "19")
					bag.Slots["0:10"] = r.ItemStack{ItemID: id, Count: 1}
					bag.Slots["0:11"] = r.ItemStack{ItemID: id, Count: 1}
					if err := g.Inventory.Save(ctx, bag); err != nil {
						t.Fatal(err)
					}
					beforeCharacter, _, _ := g.Character.Load(ctx, "19")
					beforeWarehouse, _, _ := g.AccountInventory.Load(ctx, "account-1")
					request := currentExpertJobExtractionRequest{ExtractorType: kind, ExtractorSlot: 200, TargetSlot: 10}
					if batch {
						second := request
						second.TargetSlot = 11
						outcomes, err := s.commitNativeExtractionRequests(ctx, p, []currentExpertJobExtractionRequest{request, second})
						if err != nil || len(outcomes) != 2 {
							t.Fatalf("batch: %v", err)
						}
						for _, result := range outcomes {
							if !errors.Is(result.err, dnfexpertjob.ErrExtractionInvalid) {
								t.Fatalf("title extracted: %v", result.err)
							}
						}
					} else {
						if _, err := s.commitCurrentExpertJobExtraction(ctx, p, request); !errors.Is(err, dnfexpertjob.ErrExtractionInvalid) {
							t.Fatalf("title extracted: %v", err)
						}
					}
					after, _, _ := g.Inventory.Load(ctx, "19")
					character, _, _ := g.Character.Load(ctx, "19")
					warehouse, _, _ := g.AccountInventory.Load(ctx, "account-1")
					if !reflect.DeepEqual(bag, after) || !reflect.DeepEqual(beforeCharacter, character) || !reflect.DeepEqual(beforeWarehouse, warehouse) {
						t.Fatal("rejection consumed title/tool/gold or granted materials/experience")
					}
				})
			}
		}
	}
}

func TestRealPVFAikenTitleCharacterLifetimePurchase(t *testing.T) {
	items := loadRealAikenDisjointCatalog(t)
	shop, err := newCurrentNPCShopCatalog(items.source)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := uint32(currentAikenSupportTitleOwnedID)
	var shelf, actor uint32
	for key, products := range shop.shopItems {
		if _, ok := products[id]; ok {
			shelf, actor = key, shop.shopNPCs[key]
			break
		}
	}
	if shelf == 0 {
		t.Fatal("title not on real shelf")
	}
	request := currentNPCShopBuyRequest{ItemID: id, Count: 1, ShopContext: shelf, ActorContext: actor}
	pricing, err := resolveCurrentNPCShopActorPricing(shop, items, id, actor)
	if err != nil || pricing.BuyGold != 1 {
		t.Fatalf("title price: %+v %v", pricing, err)
	}
	policy := currentNPCShopPurchaseLimit(pricing, 1)
	if policy == nil || policy.Scope != "character" || policy.Period != "accumulate" || policy.Maximum != 1 {
		t.Fatalf("missing lifetime policy: %+v", policy)
	}
	// Historical holdings do not consume the new allowance, per user policy.
	g := mustCurrentNPCShopRepositories(t, 1000, map[string]r.ItemStack{"0:10": {ItemID: currentAikenSupportTitleOwnedID, Count: 1}})
	c, _, _ := g.Character.Load(ctx, "19")
	c.CharacterID = "20"
	if err := g.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	bag, _, _ := g.Inventory.Load(ctx, "19")
	bag.CharacterID = "20"
	if err := g.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	for _, characterID := range []uint16{19, 20} {
		p := &gameSession{selectedCharacterID: characterID, conn: &bufferConn{}}
		s := currentNPCShopTestService(g, shop, items)
		// A failed purchase must not consume the allowance.
		key := fmt.Sprint(characterID)
		c, _, _ := g.Character.Load(ctx, key)
		c.Stats["gold"] = 0
		if err := g.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
		if _, err := s.commitCurrentNPCShopBuy(ctx, p, shop, items, request); !errors.Is(err, errCurrentNPCShopGoldInsufficient) {
			t.Fatalf("insufficient: %v", err)
		}
		c, _, _ = g.Character.Load(ctx, key)
		c.Stats["gold"] = 1000
		if err := g.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		results := make(chan error, 12)
		for range 12 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := s.commitCurrentNPCShopBuy(ctx, p, shop, items, request)
				results <- err
			}()
		}
		wg.Wait()
		close(results)
		success := 0
		for err := range results {
			if err == nil {
				success++
			} else if !errors.Is(err, npcshop.ErrPurchaseLimit) {
				t.Fatal(err)
			}
		}
		if success != 1 {
			t.Fatalf("concurrent purchases succeeded=%d", success)
		}
		c, _, _ = g.Character.Load(ctx, key)
		account, _, _ := g.Account.Load(ctx, "account-1")
		if c.Stats["gold"] != 999 || npcshop.PurchaseCount(account, c, *policy, time.Now().AddDate(5, 0, 0)) != 1 {
			t.Fatal("cost or durable lifetime count wrong")
		}
		// Selling/removing all copies and rebuilding the connection cannot reset it.
		bag, _, _ := g.Inventory.Load(ctx, key)
		bag.Slots = map[string]r.ItemStack{}
		if err := g.Inventory.Save(ctx, bag); err != nil {
			t.Fatal(err)
		}
		bag, _, _ = g.Inventory.Load(ctx, key) // Compare persisted snapshots; empty maps normalize to nil.
		s = currentNPCShopTestService(g, shop, items)
		p = &gameSession{selectedCharacterID: characterID, conn: &bufferConn{}}
		if _, err := s.commitCurrentNPCShopBuy(ctx, p, shop, items, request); !errors.Is(err, npcshop.ErrPurchaseLimit) {
			t.Fatalf("reconnected repeat: %v", err)
		}
		after, _, _ := g.Inventory.Load(ctx, key)
		afterC, _, _ := g.Character.Load(ctx, key)
		if !reflect.DeepEqual(bag, after) || !reflect.DeepEqual(c, afterC) {
			t.Fatal("rejected repeat changed assets")
		}
	}
}
