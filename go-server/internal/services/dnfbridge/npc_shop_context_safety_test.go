package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type npcShopBeforeProjectAssets struct {
	dnfrepo.AccountCharacterAssetUnitOfWork
	before func()
}

func (u npcShopBeforeProjectAssets) WithinAccountCharacterLedgerAssets(ctx context.Context, account, character string,
	apply func(dnfrepo.AccountRepository, dnfrepo.AccountInventoryRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error,
) error {
	ledger := u.AccountCharacterAssetUnitOfWork.(dnfrepo.AccountCharacterLedgerAssetUnitOfWork)
	return ledger.WithinAccountCharacterLedgerAssets(ctx, account, character, func(a dnfrepo.AccountRepository, ai dnfrepo.AccountInventoryRepository, c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, e dnfrepo.EquipmentRepository) error {
		u.before()
		return apply(a, ai, c, i, e)
	})
}

func TestNPCShopRepairedContextRecheckedInsideAssetOwner(t *testing.T) {
	f := mustNPCShopRepairFixture(t)
	for _, item := range []uint32{10161536, uint32(currentAikenSupportTitleOwnedID)} {
		for _, change := range []string{"unready", "area", "generation", "session_replaced"} {
			t.Run(fmt.Sprintf("item_%d_%s", item, change), func(t *testing.T) {
				p, err := resolveCurrentNPCShopActorPricing(f.shop, f.items, item, 486)
				if err != nil {
					t.Fatal(err)
				}
				slots := map[string]dnfrepo.ItemStack{}
				if p.MaterialExchange {
					slots["0:121"] = dnfrepo.ItemStack{ItemID: int64(p.NeedMaterialItem), Count: p.NeedMaterialCount + 1}
				}
				repos := mustCurrentNPCShopRepositories(t, 1000, slots)
				s, g := f.service(t, repos, 486)
				q, err := s.resolveCurrentNPCShopBuyContext(g, f.shop, currentNPCShopBuyRequest{ItemID: item, Count: 1, ShopContext: 247, ActorContext: 1000})
				if err != nil || q.contextProof == nil {
					t.Fatalf("repair=%+v err=%v", q, err)
				}
				repos.AccountAssets = npcShopBeforeProjectAssets{AccountCharacterAssetUnitOfWork: repos.AccountAssets, before: func() {
					switch change {
					case "unready":
						g.townSceneReadyCharacterID = 0
					case "area":
						g.townPositionSnapshot = f.scenes[2]
					case "generation":
						g.characterGeneration++
					case "session_replaced":
						s.gameSessions[19] = &gameSession{selectedCharacterID: 19}
					}
				}}
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
				beforeCharacter, _, _ := repos.Character.Load(context.Background(), "19")
				beforeBag, _, _ := repos.Inventory.Load(context.Background(), "19")
				_, err = s.commitCurrentNPCShopBuy(context.Background(), g, f.shop, f.items, q)
				if !errors.Is(err, errCurrentNPCShopContextUnavailable) {
					t.Fatalf("stale transaction error=%v", err)
				}
				privateAikenExchangeAssertAssetsEqual(t, repos, beforeCharacter, beforeBag)
			})
		}
	}
}

func TestNPCShopContextRepairAikenRollbackAndClearedSlot(t *testing.T) {
	f := mustNPCShopRepairFixture(t)
	p, err := resolveCurrentNPCShopActorPricing(f.shop, f.items, 10161536, 486)
	if err != nil || !p.MaterialExchange {
		t.Fatalf("magic stone pricing=%+v err=%v", p, err)
	}
	for _, state := range []string{"one_short", "bag_full", "late_commit_failure", "consume_exact_stack"} {
		t.Run(state, func(t *testing.T) {
			const count = 18
			cost := p.NeedMaterialCount * count
			slots := map[string]dnfrepo.ItemStack{}
			if state == "bag_full" {
				slots = fullCurrentNPCShopWasteInventory()
				for slot := 121; slot <= 176; slot++ {
					slots[fmt.Sprintf("0:%d", slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
				}
				cost++ // Consuming material must not free a slot in this case.
			}
			if state == "one_short" {
				cost--
			}
			slots["0:121"] = dnfrepo.ItemStack{ItemID: int64(p.NeedMaterialItem), Count: cost}
			repos := mustCurrentNPCShopRepositories(t, 1000, slots)
			if state == "late_commit_failure" {
				repos.AccountAssets = rejectingCurrentNPCShopAccountAssets{inner: repos.AccountAssets, err: errors.New("injected shop commit failure")}
			}
			s, g := f.service(t, repos, 486)
			beforeCharacter, _, _ := repos.Character.Load(context.Background(), "19")
			beforeBag, _, _ := repos.Inventory.Load(context.Background(), "19")
			if err := s.handleCurrentNPCShopBuy(g, currentNPCShopBuyTestBody(10161536, count, 247, 1000)); err != nil {
				t.Fatal(err)
			}
			ack, tail := splitGameServerUpperPacket(t, g.conn.(*bufferConn).write.Bytes())
			if state != "consume_exact_stack" {
				if len(tail) != 0 || !bytes.Equal(ack.Body, []byte{0, 4}) {
					t.Fatalf("failed exchange ACK=%x tail=%d", ack.Body, len(tail))
				}
				privateAikenExchangeAssertAssetsEqual(t, repos, beforeCharacter, beforeBag)
				return
			}
			if len(ack.Body) == 0 || ack.Body[0] != 1 {
				t.Fatalf("exact exchange rejected: %x", ack.Body)
			}
			bag, _, _ := repos.Inventory.Load(context.Background(), "19")
			assertCurrentNPCShopInventoryAmount(t, bag, 10161536, count)
			assertCurrentNPCShopInventoryAmount(t, bag, int64(p.NeedMaterialItem), 0)
			for _, stack := range bag.Slots {
				if stack.ItemID == int64(p.NeedMaterialItem) {
					t.Fatalf("consumed material left a ghost slot: %+v", stack)
				}
			}
		})
	}
}

func TestNPCShopDiagnosticsBoundedAndAvailableWithoutPacketLogger(t *testing.T) {
	var log bytes.Buffer
	now := time.Unix(1800000000, 0)
	s := &Service{logger: slog.New(slog.NewJSONHandler(&log, nil)), gameplayTimers: &processGameplayTimeQueue{now: func() time.Time { return now }}}
	g := &gameSession{connID: "bounded-shop", selectedCharacterID: 19}
	for i := 0; i < 100; i++ {
		s.logCurrentNPCShopDiagnostic(g, 21, "shop_context", currentNPCShopBuyRequest{ItemID: 10161536, Count: 18, ShopContext: 247, ActorContext: 1000}, 0, "context_or_shelf_unavailable")
	}
	if got := strings.Count(log.String(), "\n"); got != 4 {
		t.Fatalf("records=%d want4", got)
	}
	now = now.Add(2 * time.Second)
	s.logCurrentNPCShopDiagnostic(g, 22, "transaction", currentNPCShopBuyRequest{ActorContext: 1000}, 1000, "sale_npc_or_price_unavailable")
	if got := strings.Count(log.String(), "\n"); got != 5 || !strings.Contains(log.String(), `"suppressed":96`) || !strings.Contains(log.String(), `"char_id":19`) {
		t.Fatalf("bounded recovery log=%s", log.String())
	}
}

func TestNPCShopDynamicMerchantStillRequiresLiveRaidAfterContextRepair(t *testing.T) {
	shop, items := mustCurrentNPCShopTestCatalogs(t)
	shop.shopItems[currentFiendWarMerchantShop] = map[uint32]struct{}{600: {}}
	shop.shopNPCs[currentFiendWarMerchantShop] = currentFiendWarMerchantNPC
	shop.npcItems[currentFiendWarMerchantNPC] = map[uint32]struct{}{600: {}}
	repos := mustCurrentNPCShopRepositories(t, 1000, nil)
	s := currentNPCShopTestService(repos, shop, items)
	g := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
	q, err := s.resolveCurrentNPCShopBuyContext(g, shop, currentNPCShopBuyRequest{ItemID: 600, Count: 1, ShopContext: currentFiendWarMerchantShop, ActorContext: 1000})
	if err != nil || q.ActorContext != currentFiendWarMerchantNPC {
		t.Fatalf("dynamic context=%+v err=%v", q, err)
	}
	beforeCharacter, _, _ := repos.Character.Load(context.Background(), "19")
	beforeBag, _, _ := repos.Inventory.Load(context.Background(), "19")
	if _, err := s.commitCurrentNPCShopBuy(context.Background(), g, shop, items, q); err == nil {
		t.Fatal("dynamic merchant repaired context bypassed actual raid gate")
	}
	privateAikenExchangeAssertAssetsEqual(t, repos, beforeCharacter, beforeBag)
}
