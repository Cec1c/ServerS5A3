package dnfbridge

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	shoplimit "longheng.io/server/internal/modules/dnf/npcshop"
	repo "longheng.io/server/internal/modules/dnf/repository"
	pvf "longheng.io/server/internal/platform/pvf"
)

func seriaPVF(t *testing.T) (*currentNPCShopCatalog, *pvfDungeonDropCatalog) {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_SERIA_PVF")
	if path == "" {
		t.Skip("private Seria candidate required")
	}
	a, e := pvf.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	s, e := newCurrentNPCShopCatalog(a)
	if e != nil {
		t.Fatal(e)
	}
	c, e := newPVFDungeonDropCatalog(a)
	if e != nil {
		t.Fatal(e)
	}
	return s, c
}

func TestPrivateSeriaLifetimePurchaseAndOpen(t *testing.T) {
	shop, items := seriaPVF(t)
	ctx := context.Background()
	repos := mustCurrentNPCShopRepositories(t, 100, nil)
	service := currentNPCShopTestService(repos, shop, items)
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
	pricing, e := resolveCurrentNPCShopPricing(shop, items, 990027001)
	if e != nil {
		t.Fatal(e)
	}
	if pricing.BuyGold != 1 || pricing.PurchaseLimitScope != "character" || pricing.PurchaseLimitPeriod != "" || pricing.PurchaseLimitValue != 1 || pricing.PurchaseLimitReset {
		t.Fatalf("invalid pricing/limit %+v", pricing)
	}
	if _, ok := shop.npcItems[2][990027001]; !ok {
		t.Fatal("welcome box not in Seria shop")
	}
	for _, id := range []uint32{3299, 3300} {
		if _, ok := shop.npcItems[2][id]; ok {
			t.Fatalf("removed material still in Seria: %d", id)
		}
	}
	buy := func(count uint32) error {
		_, e := service.commitCurrentNPCShopBuy(ctx, session, shop, items, currentNPCShopBuyRequest{ItemID: 990027001, Count: count, ActorContext: 2})
		return e
	}
	if e := buy(2); e == nil {
		t.Fatal("bulk buy bypassed once limit")
	}
	if e := buy(1); e != nil {
		t.Fatal(e)
	}
	service = currentNPCShopTestService(repos, shop, items)
	if e := buy(1); !errors.Is(e, shoplimit.ErrPurchaseLimit) {
		t.Fatalf("repeat after service recreation: %v", e)
	}
	ch, _, _ := repos.Character.Load(ctx, "19")
	acc, _, _ := repos.Account.Load(ctx, "account-1")
	if ch.Stats["gold"] != 99 {
		t.Fatalf("gold=%d", ch.Stats["gold"])
	}
	if n := shoplimit.PurchaseCount(acc, ch, *currentNPCShopPurchaseLimit(pricing, 1), time.Now().AddDate(10, 0, 0)); n != 1 {
		t.Fatal("lifetime quota reset")
	}
	inv, _, e := repos.Inventory.Load(ctx, "19")
	if e != nil {
		t.Fatal(e)
	}
	slot := int16(-1)
	for k, v := range inv.Slots {
		if v.ItemID == 990027001 {
			n, e := strconv.Atoi(k[2:])
			if e != nil {
				t.Fatal(e)
			}
			slot = int16(n)
		}
	}
	if slot < 0 {
		t.Fatal("purchased item missing")
	}
	definition, e := resolveCurrentBoosterDefinition(items, 990027001, currentBoosterRequestRandom)
	if e != nil {
		t.Fatal(e)
	}
	opener := currentBoosterTestService(items, repos)
	result, e := opener.commitCurrentBooster(ctx, session, items, definition, currentBoosterOpenRequest{Kind: currentBoosterRequestRandom, SourceSlot: slot})
	if e != nil {
		t.Fatal(e)
	}
	want := map[uint32]uint32{990027002: 1, 990027003: 3, 990027004: 1, 10163829: 10, 400330047: 1, 3330: 2000}
	got := map[uint32]uint32{}
	for _, r := range result.Rewards {
		got[r.ItemID] += r.Count
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rewards %v want %v", got, want)
	}
	if e := buy(1); !errors.Is(e, shoplimit.ErrPurchaseLimit) {
		t.Fatalf("opening reset purchase count: %v", e)
	}
	inv, _, _ = repos.Inventory.Load(ctx, "19")
	actual := map[uint32]uint32{}
	for _, v := range inv.Slots {
		if v.ItemID == 990027001 {
			t.Fatal("box not consumed")
		}
		if v.ItemID > 0 {
			actual[uint32(v.ItemID)] += uint32(v.Count)
		}
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("persisted rewards %v", actual)
	}
	ch.CharacterID = "20"
	ch.Stats = map[string]int64{"gold": 100}
	if e := repos.Character.Save(ctx, ch); e != nil {
		t.Fatal(e)
	}
	if e := repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "20", Slots: map[string]repo.ItemStack{}}); e != nil {
		t.Fatal(e)
	}
	session.selectedCharacterID = 20
	if e := buy(1); e != nil {
		t.Fatalf("second character in same account rejected: %v", e)
	}
}

func TestPrivateSeriaPermanentSelectionsAndPotion(t *testing.T) {
	_, items := seriaPVF(t)
	for _, id := range []uint32{990027001, 990027002, 990027003, 990027004} {
		d, e := items.ResolveItem(id)
		if e != nil {
			t.Fatal(e)
		}
		if d.UsablePeriodDays != 0 || !d.ExpirationDate.IsZero() || d.DailyDeleteItem {
			t.Fatalf("temporary welcome item %+v", d)
		}
	}
	potion, e := items.ResolveItem(990027003)
	if e != nil {
		t.Fatal(e)
	}
	if normalizeMagicBoxPVFType(potion.AttachType) != "free" {
		t.Fatal("potion not tradeable")
	}
	pd, e := parseDungeonCardPVFDocument(items.source, potion.PVFPath)
	if e != nil {
		t.Fatal(e)
	}
	if duration := pd.Ints("stat change duration"); len(duration) != 1 || duration[0] != 7200000 {
		t.Fatalf("duration=%v", duration)
	}
	weapon, e := resolveCurrentBoosterDefinition(items, 990027002, currentBoosterRequestSelection)
	if e != nil {
		t.Fatal(e)
	}
	if weapon.SelectionRequired != 1 || len(weapon.SelectionCategory) != 11 {
		t.Fatalf("weapon categories=%d required=%d", len(weapon.SelectionCategory), weapon.SelectionRequired)
	}
	seen := map[uint32]bool{}
	for category, rows := range weapon.SelectionCategory {
		for _, r := range rows {
			d, e := items.ResolveItem(r.ItemID)
			if e != nil {
				t.Fatal(e)
			}
			if d.UsablePeriodDays != 0 || !d.ExpirationDate.IsZero() || d.DailyDeleteItem {
				t.Fatalf("temporary weapon %d", r.ItemID)
			}
			req := currentBoosterOpenRequest{Kind: currentBoosterRequestSelection, SelectionContext: category, Selections: []currentBoosterSelectionRequest{{ItemID: r.ItemID}}}
			amounts, options, e := resolveCurrentBoosterRewardAmounts(weapon, req, func(int64) (int64, error) { return 0, nil })
			if e != nil || len(amounts) != 1 || amounts[r.ItemID] != 1 {
				t.Fatalf("weapon selection %d: %v %v", r.ItemID, amounts, e)
			}
			rewards, e := resolveCurrentBoosterRewards(items, amounts, options, repo.ItemStack{}, time.Now())
			if e != nil || len(rewards) != 1 || !rewards[0].Definition.ExpirationDate.IsZero() {
				t.Fatalf("weapon grant %d: %v", r.ItemID, e)
			}
			seen[r.ItemID] = true
		}
	}
	if len(seen) != 37 {
		t.Fatalf("weapons=%d want 37", len(seen))
	}
	avatar, e := resolveCurrentBoosterDefinition(items, 990027004, currentBoosterRequestSelection)
	if e != nil {
		t.Fatal(e)
	}
	if len(avatar.SelectionCategory) != 16 {
		t.Fatalf("avatar categories=%d", len(avatar.SelectionCategory))
	}
	for category, rows := range avatar.SelectionCategory {
		if len(rows) != 8 {
			t.Fatalf("avatar category %d rows=%d", category, len(rows))
		}
		req := currentBoosterOpenRequest{Kind: currentBoosterRequestSelection, SelectionContext: category}
		for _, r := range rows {
			req.Selections = append(req.Selections, currentBoosterSelectionRequest{ItemID: r.ItemID, Option: r.Option})
		}
		amounts, options, e := resolveCurrentBoosterRewardAmounts(avatar, req, func(int64) (int64, error) { return 0, nil })
		if e != nil {
			t.Fatal(e)
		}
		rewards, e := resolveCurrentBoosterRewards(items, amounts, options, repo.ItemStack{}, time.Now())
		if e != nil || len(rewards) != 8 {
			t.Fatalf("avatar grant category %d: rows=%d err=%v", category, len(rewards), e)
		}
		for _, r := range rewards {
			if r.Placement != currentBoosterRewardAvatar || r.Count != 1 {
				t.Fatalf("avatar reward=%+v", r)
			}
		}
	}
}
