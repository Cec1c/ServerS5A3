package dnfbridge

import (
	"context"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
)

func TestR195FiendWarMerchantRequiresValidInstanceAndPreservesOrdinarySharedGoods(t *testing.T) {
	for _, tc := range []struct {
		name             string
		merchant, shared bool
		actor, shop      uint32
		allowed          bool
	}{
		{"explicit_npc_shared", true, true, currentFiendWarMerchantNPC, 0, false},
		{"explicit_shop_shared", true, true, 326, currentFiendWarMerchantShop, false},
		{"exclusive_without_context", true, false, 0, 0, false},
		{"exclusive_forged_context", true, false, 326, 1, false},
		{"ordinary_shared", true, true, 326, 1, true},
		{"shared_without_context", true, true, 0, 0, true},
		{"ordinary_only", false, true, 326, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shop, items := mustCurrentNPCShopTestCatalogs(t)
			if tc.merchant {
				shop.npcItems[currentFiendWarMerchantNPC] = map[uint32]struct{}{600: {}}
			}
			if !tc.shared {
				delete(shop.npcItems[326], 600)
			}
			repos := mustCurrentNPCShopRepositories(t, 1000, nil)
			s := currentNPCShopTestService(repos, shop, items)
			p := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
			ctx := context.Background()
			beforeActor, _, err := repos.Character.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			beforeBag, _, err := repos.Inventory.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			q := currentNPCShopBuyRequest{ItemID: 600, Count: 3, ActorContext: tc.actor, ShopContext: tc.shop}
			unlock, gateErr := s.lockCurrentFiendWarMerchantPurchase(p, shop, q)
			if gateErr == nil {
				unlock()
			}
			if (gateErr == nil) != tc.allowed {
				t.Fatalf("merchant gate allowed=%t err=%v", tc.allowed, gateErr)
			}
			_, err = s.commitCurrentNPCShopBuy(ctx, p, shop, items, q)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%t err=%v", tc.allowed, err)
			}
			afterActor, _, loadErr := repos.Character.Load(ctx, "19")
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			afterBag, _, loadErr := repos.Inventory.Load(ctx, "19")
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if !tc.allowed {
				if !reflect.DeepEqual(beforeActor, afterActor) || !reflect.DeepEqual(beforeBag, afterBag) {
					t.Fatal("rejected merchant purchase mutated assets")
				}
				return
			}
			var granted int64
			for _, stack := range afterBag.Slots {
				if stack.ItemID == 600 {
					granted += stack.Count
				}
			}
			if afterActor.Stats["gold"] != 700 || granted != 3 {
				t.Fatalf("ordinary shop did not commit: gold=%d count=%d", afterActor.Stats["gold"], granted)
			}
		})
	}
}

func TestR195FiendWarMerchantBoardShowsActiveAreaAndClearsInactive(t *testing.T) {
	now := time.Unix(1800000000, 0)
	for _, state := range []string{"active", "expired", "complete", "failed"} {
		t.Run(state, func(t *testing.T) {
			b := &fiendwar.FiendWarBattle{
				Config:       &fiendwar.FiendWarBattleConfig{Areas: map[int]fiendwar.FiendWarArea{2: {Index: 2, Dungeon: -1}}},
				MerchantArea: 2, MerchantExpiresAt: now.Add(time.Minute), MemberAreas: map[uint16]int{19: 2},
			}
			switch state {
			case "expired":
				b.MerchantExpiresAt = now.Add(-time.Second)
			case "complete":
				b.Complete = true
			case "failed":
				b.Failed = true
			}
			v := currentFiendWarProjection{RaidKey: 1, Battle: b, Members: []runtimeRaidMemberState{{CharID: 19, GroupIndex: 1}}, Deadline: now.Add(time.Hour)}
			packets, err := buildCurrentFiendWarBoard(v, currentFiendWarProjection{}, 19, now)
			if err != nil {
				t.Fatal(err)
			}
			wantArea := byte(0)
			if state == "active" {
				wantArea = byte(b.MerchantArea)
			}
			count := 0
			for _, packet := range packets {
				if packet.opcode == currentFiendWarMerchant {
					count++
					if !reflect.DeepEqual(packet.body, []byte{wantArea}) {
						t.Fatalf("merchant marker=%x want area=%d state=%s", packet.body, wantArea, state)
					}
				}
			}
			if count != 1 {
				t.Fatalf("merchant notification count=%d", count)
			}
		})
	}
}

func TestRealPVFCurrentFiendWarMerchantRejectsUnstartedInstanceWithActualProductOwnership(t *testing.T) {
	f, peers := actualCurrentFiendWar(t, 1)
	if f.state.Started {
		t.Fatal("merchant rejection fixture must not start the raid")
	}
	s, p := f.service, peers[0]
	shop, err := s.currentNPCShopCatalog()
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{490007845, 490007846} {
		if _, ok := shop.npcItems[currentFiendWarMerchantNPC][id]; !ok {
			t.Fatalf("merchant catalog missing %d", id)
		}
	}
	if _, ok := shop.npcItems[20316][490007845]; !ok {
		t.Fatal("shared supply absent from ordinary NPC")
	}
	unlock, err := s.lockCurrentFiendWarMerchantPurchase(p, shop, currentNPCShopBuyRequest{ItemID: 490007845, Count: 1, ActorContext: 20316})
	if err != nil {
		t.Fatal("real shared goods incorrectly blocked", err)
	}
	unlock()
	ctx := context.Background()
	beforeBag, _, err := f.repos.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	beforeActor, _, err := f.repos.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []currentNPCShopBuyRequest{
		{ItemID: 490007846, Count: 1, ActorContext: currentFiendWarMerchantNPC},
		{ItemID: 490007845, Count: 1, ActorContext: 20316, ShopContext: currentFiendWarMerchantShop},
		{ItemID: 490007846, Count: 1},
	} {
		unlock, gateErr := s.lockCurrentFiendWarMerchantPurchase(p, shop, q)
		if gateErr == nil {
			unlock()
			t.Fatal("real catalog guard accepted merchant purchase without a started raid", q)
		}
		if _, err := s.commitCurrentNPCShopBuy(ctx, p, shop, items, q); err == nil {
			t.Fatal("merchant purchase without a started raid committed", q)
		}
		afterBag, _, err := f.repos.Inventory.Load(ctx, "19")
		if err != nil {
			t.Fatal(err)
		}
		afterActor, _, err := f.repos.Character.Load(ctx, "19")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(beforeBag, afterBag) || !reflect.DeepEqual(beforeActor, afterActor) {
			t.Fatal("real rejected merchant purchase mutated assets")
		}
	}
}
