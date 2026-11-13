package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"

	dnfnpcshop "longheng.io/server/internal/modules/dnf/npcshop"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestRealPVFEmpireShopAllProductsChargeCompletePrice(t *testing.T) {
	shop, items := realPVF535(t)
	ids := make([]int, 0, len(shop.npcItems[604]))
	for id := range shop.npcItems[604] {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	if len(ids) != 38 {
		t.Fatalf("unexpected empire shelf: %v", ids)
	}
	multi := 0
	ctx := context.Background()
	for _, id := range ids {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			def, err := items.ResolveItem(uint32(id))
			if err != nil {
				t.Fatal(err)
			}
			costs := def.NeedMaterials
			if len(costs) == 0 || len(costs)%2 != 0 {
				t.Fatalf("unexpected price: %v", costs)
			}
			if len(costs) > 2 {
				multi++
			}
			for missing := -1; missing < len(costs)/2; missing++ {
				name := "success"
				if missing >= 0 {
					name = fmt.Sprintf("insufficient_%d", costs[missing*2])
				}
				t.Run(name, func(t *testing.T) {
					slots := map[string]dnfrepo.ItemStack{}
					for i := 0; i < len(costs); i += 2 {
						amount := costs[i+1] + 7
						if i/2 == missing {
							amount = costs[i+1] - 1
						}
						slots[fmt.Sprintf("0:%d", 121+i/2)] = dnfrepo.ItemStack{ItemID: costs[i], Count: amount}
					}
					repos := mustCurrentNPCShopRepositories(t, 10000000, slots)
					s := currentNPCShopTestService(repos, shop, items)
					beforeA, _, _ := repos.Account.Load(ctx, "account-1")
					beforeC, _, _ := repos.Character.Load(ctx, "19")
					beforeI, _, _ := repos.Inventory.Load(ctx, "19")
					r, err := s.commitCurrentNPCShopBuy(ctx, &gameSession{selectedCharacterID: 19}, shop, items, currentNPCShopBuyRequest{ItemID: uint32(id), Count: 1, ActorContext: 604})
					a, _, _ := repos.Account.Load(ctx, "account-1")
					c, _, _ := repos.Character.Load(ctx, "19")
					inv, _, _ := repos.Inventory.Load(ctx, "19")
					if missing >= 0 {
						if !errors.Is(err, errCurrentNPCShopMaterialInsufficient) || !reflect.DeepEqual(a, beforeA) || !reflect.DeepEqual(c, beforeC) || !reflect.DeepEqual(inv, beforeI) {
							t.Fatalf("partial purchase persisted: %v", err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					for i := 0; i < len(costs); i += 2 {
						if got := inv.Slots[fmt.Sprintf("0:%d", 121+i/2)].Count; got != 7 {
							t.Fatalf("material %d left=%d want 7", costs[i], got)
						}
					}
					if r.Count != 1 || c.Stats["gold"] != 10000000 {
						t.Fatalf("unexpected grant/gold: %+v", r)
					}
					assertCurrentNPCShopInventoryAmount(t, inv, int64(id), 1)
					if id == 490009969 {
						// Reconnect preserves the account quota and returns the native limit error.
						conn := &bufferConn{}
						s = currentNPCShopTestService(repos, shop, items)
						var shopID uint32
						for k, npc := range shop.shopNPCs {
							if npc == 604 {
								shopID = k
								break
							}
						}
						if err := s.handleCurrentNPCShopBuy(&gameSession{selectedCharacterID: 19, conn: conn}, currentNPCShopBuyTestBody(uint32(id), 1, shopID, 604)); err != nil {
							t.Fatal(err)
						}
						ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
						if !bytes.Equal(ack.Body, []byte{0, 0x85}) || len(rest) != 0 {
							t.Fatalf("monthly rejection body=%x", ack.Body)
						}
						pricing, err := resolveCurrentNPCShopPricing(shop, items, uint32(id))
						if err != nil || dnfnpcshop.PurchaseCount(a, c, *currentNPCShopPurchaseLimit(pricing, 1), s.gameplayNow()) != 1 {
							t.Fatalf("monthly count lost: %v", err)
						}
					}
				})
			}
		})
	}
	if multi != 10 {
		t.Fatalf("multi-material empire goods=%d want 10", multi)
	}
}

func TestNPCShopMultipleMaterialBatchAndAtomicFailure(t *testing.T) {
	for _, mode := range []string{"success", "second_missing", "commit_failure", "bag_full"} {
		t.Run(mode, func(t *testing.T) {
			shop, _ := mustCurrentNPCShopTestCatalogs(t)
			source := shop.source.(bridgePVFSource)
			source["stackable/material_exchange.stk"] = strings.Replace(source["stackable/material_exchange.stk"], "900 2", "900 2 901 3", 1)
			items, err := newPVFDungeonDropCatalog(source)
			if err != nil {
				t.Fatal(err)
			}
			slots := map[string]dnfrepo.ItemStack{"0:121": {ItemID: 900, Count: 20}, "0:122": {ItemID: 901, Count: 30}}
			if mode == "second_missing" {
				slots["0:122"] = dnfrepo.ItemStack{ItemID: 901, Count: 8}
			}
			if mode == "bag_full" {
				for i := 3; i <= 8; i++ {
					slots[fmt.Sprintf("0:%d", i)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
				}
				def, err := items.ResolveItem(602)
				if err != nil {
					t.Fatal(err)
				}
				for i := def.SlotStart; i <= def.SlotEnd; i++ {
					key := fmt.Sprintf("0:%d", i)
					if _, occupied := slots[key]; !occupied {
						slots[key] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
					}
				}
			}
			repos := mustCurrentNPCShopRepositories(t, 1000, slots)
			ctx := context.Background()
			beforeC, _, _ := repos.Character.Load(ctx, "19")
			beforeI, _, _ := repos.Inventory.Load(ctx, "19")
			if mode == "commit_failure" {
				repos.AccountAssets = rejectingCurrentNPCShopAccountAssets{inner: repos.AccountAssets, err: errors.New("multiple material commit failure")}
			}
			s := currentNPCShopTestService(repos, shop, items)
			_, err = s.commitCurrentNPCShopBuy(ctx, &gameSession{selectedCharacterID: 19}, shop, items, currentNPCShopBuyRequest{ItemID: 602, Count: 3})
			c, _, _ := repos.Character.Load(ctx, "19")
			inv, _, _ := repos.Inventory.Load(ctx, "19")
			if mode != "success" {
				if err == nil || !reflect.DeepEqual(beforeC, c) || !reflect.DeepEqual(beforeI, inv) {
					t.Fatalf("failed batch changed assets: %v", err)
				}
				return
			}
			if err != nil || c.Stats["gold"] != 700 || inv.Slots["0:121"].Count != 14 || inv.Slots["0:122"].Count != 21 {
				t.Fatalf("incorrect batch debit: err=%v gold=%d slots=%v", err, c.Stats["gold"], inv.Slots)
			}
			assertCurrentNPCShopInventoryAmount(t, inv, 602, 3)
		})
	}
}

func TestNPCShopMaterialPriceRejectsMalformedTailAndOverflow(t *testing.T) {
	for _, value := range []string{"900 2 901", "900 2 901 0", "900 2 901 -1", "900 2 `901` 3", "900 2 4294967296 3", "900 9223372036854775807 900 1"} {
		t.Run(value, func(t *testing.T) {
			shop, _ := mustCurrentNPCShopTestCatalogs(t)
			source := shop.source.(bridgePVFSource)
			source["stackable/material_exchange.stk"] = strings.Replace(source["stackable/material_exchange.stk"], "900 2", value, 1)
			items, err := newPVFDungeonDropCatalog(source)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := resolveCurrentNPCShopPricing(shop, items, 602); !errors.Is(err, errCurrentNPCShopProductUnavailable) {
				t.Fatalf("malformed tail accepted: %v", err)
			}
		})
	}
	shop, _ := mustCurrentNPCShopTestCatalogs(t)
	source := shop.source.(bridgePVFSource)
	source["stackable/material_exchange.stk"] = strings.Replace(source["stackable/material_exchange.stk"], "900 2", fmt.Sprintf("900 2 901 %d", int64(math.MaxInt64)), 1)
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s := currentNPCShopTestService(mustCurrentNPCShopRepositories(t, 1000, nil), shop, items)
	if _, err := s.commitCurrentNPCShopBuy(context.Background(), &gameSession{selectedCharacterID: 19}, shop, items, currentNPCShopBuyRequest{ItemID: 602, Count: 2}); err == nil {
		t.Fatal("batch material multiplication overflow accepted")
	}
}

func TestRealPVFDreamChoiceBoxTradeAndAuction(t *testing.T) {
	_, items := realPVF535(t)
	def, err := items.ResolveItem(990032001)
	if err != nil {
		t.Fatal(err)
	}
	if def.AttachType != "[free]" || !currentAuctionTradeable(def) || !currentDungeonDiscardTradeable(def) {
		t.Fatalf("dream choice box not freely tradable: %+v", def)
	}
	stack := dnfrepo.ItemStack{ItemID: 990032001, Count: 1, RawEntry: make([]byte, currentItemListEntryWireSize)}
	if _, _, ok := currentAuctionRegistrationPolicyForDefinition(def, stack); !ok {
		t.Fatal("auction policy rejected box")
	}
	repos := mustCurrentNPCShopRepositories(t, 100000, map[string]dnfrepo.ItemStack{"0:65": stack})
	s := currentNPCShopTestService(repos, nil, items)
	seedAuctionSequenceForBridge(t, s, 1000)
	conn := &bufferConn{}
	if err := s.registerCurrentAuctionItemLocked(&gameSession{conn: conn, selectedCharacterID: 19, accountID: "account-1"}, buildCurrentAuctionRegisterRequestForTest(65, 990032001, 1, -1, 1000, 1000, 24)); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if !bytes.Equal(ack.Body, []byte{1, 0}) {
		t.Fatalf("dream auction registration failed: %x", ack.Body)
	}
	inv, _, _ := repos.Inventory.Load(context.Background(), "19")
	if inv.Slots["0:65"].ItemID != 0 {
		t.Fatal("auction escrow left source box in bag")
	}
}
