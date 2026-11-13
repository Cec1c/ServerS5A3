package dnfbridge

import (
	"context"
	"encoding/binary"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"reflect"
	"testing"
	"time"
)

func couponR6Catalog(t *testing.T) *pvfCeraShopCatalog {
	t.Helper()
	source := bridgePVFSource{
		"etc/cerashop.etc":    "[item]\n[/item]\n[premium]\n[/premium]\n[creature]\n[/creature]\n[coin]\n[/coin]\n[material]\n[/material]\n[recoveryitem]\n[/recoveryitem]\n[package]\n100001 9002 1 0 1000 `package` 1 0 0 0 0\n1500000 9002 1 0 900 `package` 90 0 0 0 0\n[/package]\n[private coupon discounts]\n49 9003 100001 1500000 90\n[/private coupon discounts]\n",
		"monster/monster.lst": "", "equipment/equipment.lst": "",
		"stackable/stackable.lst": "9002 `pack.stk`\n9003 `coupon.stk`\n",
		"stackable/pack.stk":      "[stackable type]\n`[etc]` 0\n[stack limit]\n1000\n",
		"stackable/coupon.stk":    "[stackable type]\n`[coupon]` 49\n[stack limit]\n1000\n[expiration date]\n`2028-12-31 23:59:59`\n",
	}
	c, e := newPVFCeraShopCatalog(source)
	if e != nil {
		t.Fatal(e)
	}
	return c
}

func TestCouponR6AtomicNativeCart(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		count   int64
		rows    int
		item    uint32
		slot    int16
		money   int64
		expired bool
		normal  bool
		want    bool
	}{
		{"one", 1, 1, 9003, 70, 2000, false, false, true},
		{"two", 2, 2, 9003, 70, 2000, false, false, true},
		{"two-with-one-coupon", 1, 2, 9003, 70, 2000, false, false, false},
		{"no-coupon", 0, 1, 9003, 70, 2000, false, false, false},
		{"missing-native-fields", 1, 1, 0, 70, 2000, false, false, false},
		{"wrong-item", 1, 1, 9002, 70, 2000, false, false, false},
		{"wrong-slot", 1, 1, 9003, 71, 2000, false, false, false},
		{"negative-slot", 1, 1, 9003, -1, 2000, false, false, false},
		{"insufficient-money", 1, 1, 9003, 70, 899, false, false, false},
		{"expired-instance", 1, 1, 9003, 70, 2000, true, false, false},
		{"coupon-for-normal-price", 1, 1, 9003, 70, 2000, false, true, false},
		{"ordinary-purchase", 1, 1, 0, 70, 2000, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			g := memory.NewMemoryGroup()
			c := couponR6Catalog(t)
			stack := repo.ItemStack{ItemID: 9003, Count: tc.count}
			if tc.expired {
				stack.ExpireAt = now
			}
			slots := map[string]repo.ItemStack{}
			if tc.count > 0 {
				slots["0:70"] = stack
			}
			seedShopCharacter(t, ctx, g, "77", tc.money, slots)
			before, _, _ := g.Inventory.Load(ctx, "77")
			s := shopTestService(g, c)
			s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return now }}
			conn := &bufferConn{}
			a := &gameSession{conn: conn, connID: "coupon-r6", selectedCharacterID: 77}
			ids := make([]uint32, tc.rows)
			for i := range ids {
				ids[i] = 1500000
				if tc.normal {
					ids[i] = 100001
				}
			}
			body := shopCartBody(0, 0xff, ids...)
			for i := range ids {
				off := 4 + i*15
				binary.LittleEndian.PutUint32(body[off+9:], tc.item)
				binary.LittleEndian.PutUint16(body[off+13:], uint16(tc.slot))
			}
			if e := s.handleCurrentCeraShopPurchase(a, body); e != nil {
				t.Fatal(e)
			}
			ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
			if len(ack.Body) == 0 || (ack.Body[0] == 1) != tc.want {
				t.Fatalf("ack=%x want=%v", ack.Body, tc.want)
			}
			inv, _, _ := g.Inventory.Load(ctx, "77")
			account, _, _ := g.Account.Load(ctx, "account-1")
			if !tc.want {
				if !reflect.DeepEqual(inv.Slots, before.Slots) || currentAccountCera(account) != tc.money {
					t.Fatal("failed cart changed assets")
				}
				return
			}
			price := int64(900)
			remaining := tc.count - int64(tc.rows)
			if tc.normal {
				price = 1000
				remaining = tc.count
			}
			if currentAccountCera(account) != tc.money-price*int64(tc.rows) {
				t.Fatal("incorrect charge")
			}
			var packs, coupons int64
			for _, st := range inv.Slots {
				if st.ItemID == 9002 {
					packs += st.Count
				}
				if st.ItemID == 9003 {
					coupons += st.Count
				}
			}
			if packs != int64(tc.rows) || coupons != remaining {
				t.Fatalf("packs=%d coupons=%d", packs, coupons)
			}
		})
	}
}

func TestCouponR6PVFDefinitionBoundaries(t *testing.T) {
	c := couponR6Catalog(t)
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	q := currentCeraShopPurchaseRequestItem{CommodityID: 1500000, DiscountCouponItemID: 9003, DiscountCouponSlot: 70}
	if _, e := c.resolveDiscountCoupon(q, 0, now); e != nil {
		t.Fatal(e)
	}
	if _, e := c.resolveDiscountCoupon(q, 1, now); e == nil {
		t.Fatal("alternate payment accepted")
	}
	if _, e := c.resolveDiscountCoupon(q, 0, now.AddDate(3, 0, 0)); e == nil {
		t.Fatal("expired definition accepted")
	}
	rule := c.discountCoupons[1500000]
	rule.Group = 48
	c.discountCoupons[1500000] = rule
	if _, e := c.resolveDiscountCoupon(q, 0, now); e == nil {
		t.Fatal("foreign period accepted")
	}
	if c.couponBaseCommodity(1500000) != 100001 || c.couponBaseCommodity(999) != 999 {
		t.Fatal("festival identity")
	}
}
