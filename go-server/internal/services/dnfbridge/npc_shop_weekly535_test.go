package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"testing"

	dnfnpcshop "longheng.io/server/internal/modules/dnf/npcshop"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func realPVF535(t *testing.T) (*currentNPCShopCatalog, *pvfDungeonDropCatalog) {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the fixed Script.pvf")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	shop, err := newCurrentNPCShopCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	return shop, items
}

// The real Epic Road shop sells the talisman 490010214 to NPC 3136 and the item
// document declares [purchase limit] account weekly 70. The commerce fields are
// document-derived: price 200000, value 1 and no [need material] section, so it
// is a gold purchase and the material fields are zero because the document has
// no such section (not because of a guess).
func TestGreen535RealPVFEpicRoadTalismanAccountWeekly70(t *testing.T) {
	shop, items := realPVF535(t)
	if !currentNPCShopContainsItem(shop, 490010214) {
		t.Fatal("epic road talisman 490010214 is not listed in any loaded shop")
	}
	if _, ok := shop.npcItems[3136][490010214]; !ok {
		t.Fatal("talisman is not registered to the Epic Road NPC 3136")
	}
	pricing, err := resolveCurrentNPCShopPricing(shop, items, 490010214)
	if err != nil {
		t.Fatal(err)
	}
	if pricing.PurchaseLimitScope != "account" || pricing.PurchaseLimitPeriod != "weekly" || pricing.PurchaseLimitValue != 70 {
		t.Fatalf("talisman limit=%s/%s/%d want account/weekly/70",
			pricing.PurchaseLimitScope, pricing.PurchaseLimitPeriod, pricing.PurchaseLimitValue)
	}
	if pricing.BuyGold != 200000 || pricing.MaterialExchange || pricing.NeedMaterialItem != 0 || pricing.NeedMaterialCount != 0 {
		t.Fatalf("talisman commerce=%+v want gold=200000 and no material exchange", pricing)
	}
	if !pricing.Buyable || pricing.Definition.PVFPath != "stackable/490010001/chn_490010214.stk" {
		t.Fatalf("talisman definition=%+v", pricing.Definition)
	}
	policy := currentNPCShopPurchaseLimit(pricing, 1)
	if policy == nil || policy.Scope != "account" || policy.Period != "weekly" || policy.Maximum != 70 || policy.ItemID != 490010214 {
		t.Fatalf("policy=%+v want item=490010214 account/weekly/70", policy)
	}
}

// The native NOTI purchase-count query must read the weekly window, so an
// already bought 69/70 quota shows up for the client instead of 0, and a 71st
// purchase is rejected. The grant path is the real owner transaction.
func TestGreen535RealPVFNOTIPurchaseCountUsesWeeklyWindow(t *testing.T) {
	shop, items := realPVF535(t)
	repos := mustCurrentNPCShopRepositories(t, 100000000, nil)
	service := currentNPCShopTestService(repos, shop, items)
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "npc-shop-weekly-noti", selectedCharacterID: 19}
	ctx := context.Background()
	buy := func(count uint32) error {
		_, err := service.commitCurrentNPCShopBuy(ctx, session, shop, items,
			currentNPCShopBuyRequest{ItemID: 490010214, Count: count, ActorContext: 1})
		return err
	}
	expectCount := func(want uint32) {
		conn.write.Reset()
		request := make([]byte, 4)
		binary.LittleEndian.PutUint32(request, 3136)
		if err := service.handleCurrentNPCShopPurchaseCount(session, request); err != nil {
			t.Fatal(err)
		}
		ack, trailing := splitGameServerUpperPacket(t, conn.write.Bytes())
		if len(trailing) != 0 {
			t.Fatalf("NOTI trailing bytes=%d", len(trailing))
		}
		body := buildCurrentNPCShopPurchaseCountBody(map[uint32]uint32{490010214: want})
		if !bytes.Equal(ack.Body, body) {
			t.Fatalf("NOTI count body=%x want=%x", ack.Body, body)
		}
	}
	if err := buy(69); err != nil {
		t.Fatal(err)
	}
	expectCount(69)
	if err := buy(1); err != nil {
		t.Fatalf("69+1 must be allowed: %v", err)
	}
	expectCount(70)
	if err := buy(1); !errors.Is(err, dnfnpcshop.ErrPurchaseLimit) {
		t.Fatalf("71st purchase must be rejected: %v", err)
	}
	expectCount(70)
}

// Synthetic coverage of the three parser shapes and the fail-closed period.
func TestGreen535BridgePurchaseLimitShapes(t *testing.T) {
	source := bridgePVFSource{
		"monster/monster.lst":               "",
		"stackable/stackable.lst":           "601 `weekly_talisman.stk`\n602 `legacy_daily.stk`\n603 `bad_period.stk`\n604 `no_limit.stk`\n",
		"equipment/equipment.lst":           "",
		"itemshop/itemshop.lst":             "400000044 `2018_epicroad_kairyu.shp`\n",
		"itemshop/2018_epicroad_kairyu.shp": "[NPC]\n3136\n[item list]\n601 602 603 604\n[/item list]\n",
		"equipment/pricetable.tbl":          "[rate]\n200 150 30\n",
		"stackable/weekly_talisman.stk":     "[price]\n200000\n[value]\n1\n[purchase limit]\n`account` `weekly` 70\n",
		"stackable/legacy_daily.stk":        "[price]\n100\n[daily purchase limit]\n`character` 5\n[reset daily purchase item]\n",
		"stackable/bad_period.stk":          "[price]\n100\n[purchase limit]\n`account` `monthly` 70\n",
		"stackable/no_limit.stk":            "[price]\n100\n",
	}
	shop, err := newCurrentNPCShopCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	weekly, err := resolveCurrentNPCShopPricing(shop, items, 601)
	if err != nil || weekly.PurchaseLimitScope != "account" || weekly.PurchaseLimitPeriod != "weekly" || weekly.PurchaseLimitValue != 70 {
		t.Fatalf("weekly shape=%+v err=%v", weekly, err)
	}
	if p := currentNPCShopPurchaseLimit(weekly, 1); p == nil || p.Period != "weekly" || p.Maximum != 70 {
		t.Fatalf("weekly policy=%+v", p)
	}
	legacy, err := resolveCurrentNPCShopPricing(shop, items, 602)
	if err != nil || legacy.PurchaseLimitScope != "character" || legacy.PurchaseLimitPeriod != "" || legacy.PurchaseLimitValue != 5 || !legacy.PurchaseLimitReset {
		t.Fatalf("legacy daily shape=%+v err=%v", legacy, err)
	}
	if p := currentNPCShopPurchaseLimit(legacy, 1); p == nil || p.Period != "" || !p.ResetDaily || p.Maximum != 5 {
		t.Fatalf("legacy policy=%+v", p)
	}
	if _, err := resolveCurrentNPCShopPricing(shop, items, 603); err == nil {
		t.Fatal("unknown period must fail closed")
	}
	none, err := resolveCurrentNPCShopPricing(shop, items, 604)
	if err != nil || none.PurchaseLimitValue != 0 {
		t.Fatalf("no-limit shape=%+v err=%v", none, err)
	}
	if p := currentNPCShopPurchaseLimit(none, 1); p != nil {
		t.Fatalf("no-limit product must keep no policy: %+v", p)
	}
	if !none.Buyable {
		t.Fatal("no-limit product must remain buyable")
	}
}

// The bridge must pass an explicit daily period through to the domain without
// requiring the optional reset marker, so the domain daily window applies.
func TestGreen535BridgeExplicitDailyPolicyCarriesPeriod(t *testing.T) {
	pricing := currentNPCShopPricing{
		PurchaseLimitScope:  "account",
		PurchaseLimitPeriod: "daily",
		PurchaseLimitValue:  5,
	}
	pricing.Definition.ItemID = 605
	policy := currentNPCShopPurchaseLimit(pricing, 1)
	if policy == nil || policy.Period != "daily" || policy.Maximum != 5 || policy.ResetDaily || policy.Scope != "account" {
		t.Fatalf("explicit daily policy=%+v", policy)
	}
}
