package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/inventory"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"strings"
	"testing"
)

func TestPrivateGrowthPairedRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires paired candidate PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	shop, err := newCurrentNPCShopCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{initialEquipmentArchive: a, pvfItemCatalog: c}
	for _, tc := range []struct {
		id       uint32
		material uint32
		cost     int64
	}{{990033001, 10158700, 50}, {990033002, 10158700, 50}, {990033101, 10158124, 3}} {
		q := currentNPCShopBuyRequest{ItemID: tc.id, Count: 1, ShopContext: 15, ActorContext: 2}
		if err := validateCurrentNPCShopBuyShelf(shop, q); err != nil {
			t.Fatal(tc.id, err)
		}
		price, err := resolveCurrentNPCShopActorPricing(shop, c, tc.id, 2)
		if err != nil || !price.MaterialExchange || price.NeedMaterialItem != tc.material || price.NeedMaterialCount != tc.cost || price.ExchangeGold != 0 {
			t.Fatalf("price %d: %+v %v", tc.id, price, err)
		}
		q.ShopContext = 247
		if validateCurrentNPCShopBuyShelf(shop, q) == nil {
			t.Fatal("wrong shop accepted", tc.id)
		}
		if tc.id >= 990033101 {
			def, err := c.ResolveItem(tc.id)
			if err != nil {
				t.Fatal(err)
			}
			if normalizeMagicBoxPVFType(def.AttachType) != "account" || currentAuctionTradeable(def) || currentDungeonDiscardTradeable(def) {
				t.Fatal("ticket transferable", tc.id)
			}
			stack := repo.ItemStack{ItemID: int64(tc.id), Count: 1, RawEntry: make([]byte, 119), Extra: map[string]string{"item_kind": "equipment"}}
			if err := s.initializePrivateGrowthGrant(&stack); err != nil {
				t.Fatal(err)
			}
			if !inventory.GrowthTicketUsable(stack, s.gameplayNow()) || inventory.GrowthTicketLevel(stack) != 0 {
				t.Fatalf("initial ticket %d %+v", tc.id, stack)
			}
			mode, _, _ := inventory.GrowthTicketType(int64(tc.id))
			for level := 0; level < 31; level++ {
				got, e := s.alignedUpgradePolicyResolver()(mode, int64(tc.id), level)
				want, e2 := s.alignedUpgradePolicyResolver()(mode, realForgingWeaponItemID, level)
				if e != nil || e2 != nil || got.SuccessWeight != want.SuccessWeight || got.PenaltyType != want.PenaltyType || got.PenaltyValue != want.PenaltyValue {
					t.Fatalf("weapon policy %d %d got=%+v want=%+v e=%v/%v", tc.id, level, got, want, e, e2)
				}
			}
		} else {
			def, _ := c.ResolveItem(tc.id)
			text, err := a.ReadText(def.PVFPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(text, "100000 50") {
				t.Fatal("box is not deterministic x50", tc.id)
			}
		}
	}
	for _, id := range []uint32{990033102, 990033103, 990033104, 990033105} {
		if validateCurrentNPCShopBuyShelf(shop, currentNPCShopBuyRequest{ItemID: id, Count: 1, ShopContext: 15, ActorContext: 2}) == nil {
			t.Fatal("retired typed coupon still sold", id)
		}
	}
	// Original one-for-one products and the latest Shua content remain present.
	for _, id := range []uint32{990030101, 990030102, 490008445, 990031102, 990031103} {
		if _, err := c.ResolveItem(id); err != nil {
			t.Fatal(id, err)
		}
	}
}
