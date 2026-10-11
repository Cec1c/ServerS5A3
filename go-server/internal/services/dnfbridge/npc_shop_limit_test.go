package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestNPCShopMaterialLimitRejectsRepeatAndReturnsPersistedCount(t *testing.T) {
	shop, _ := mustCurrentNPCShopTestCatalogs(t)
	source := shop.source.(bridgePVFSource)
	source["stackable/material_exchange.stk"] += "[daily purchase limit]\n`character` 1\n[reset daily purchase item]\n"
	var err error
	shop, err = newCurrentNPCShopCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	repos := mustCurrentNPCShopRepositories(t, 1000, map[string]dnfrepo.ItemStack{"0:121": {ItemID: 900, Count: 4}})
	service := currentNPCShopTestService(repos, shop, items)
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
	req := currentNPCShopBuyRequest{ItemID: 602, Count: 1, ActorContext: 326}
	if _, err := service.commitCurrentNPCShopBuy(context.Background(), session, shop, items, req); err != nil {
		t.Fatal(err)
	}
	// Rebuild the Service/session, simulating reconnect: quota belongs to records.
	service = currentNPCShopTestService(repos, shop, items)
	if _, err := service.commitCurrentNPCShopBuy(context.Background(), session, shop, items, req); err == nil {
		t.Fatal("repeat purchase bypassed limit")
	}
	inventory, _, _ := repos.Inventory.Load(context.Background(), "19")
	if inventory.Slots["0:121"].Count != 2 {
		t.Fatal("failed repeat debited materials")
	}
	conn := &bufferConn{}
	session.conn = conn
	// The real handler must answer the wrapped quota error using the native
	// purchase-limit message, without an inventory update or another debit.
	if err := service.handleCurrentNPCShopBuy(session, currentNPCShopBuyTestBody(602, 1, 0, 326)); err != nil {
		t.Fatal(err)
	}
	failure, trailing := splitGameServerUpperPacket(t, conn.write.Bytes())
	if len(trailing) != 0 || failure.Header.MsgID != uint16(dnfenum.CmdPacketBuyItem) ||
		failure.Header.Classification != dnfproto.DefaultChannelClassification ||
		!bytes.Equal(failure.Body, []byte{0, 0x85}) {
		t.Fatalf("quota rejection must show native purchase limit, got header=%+v body=%x trailing=%d", failure.Header, failure.Body, len(trailing))
	}
	after, _, err := repos.Inventory.Load(context.Background(), "19")
	if err != nil || !equalCurrentNPCShopSlots(inventory.Slots, after.Slots) {
		t.Fatalf("handler rejection changed inventory: err=%v before=%+v after=%+v", err, inventory.Slots, after.Slots)
	}
	conn.write.Reset()
	request := make([]byte, 4)
	binary.LittleEndian.PutUint32(request, 326)
	if err := service.handleCurrentNPCShopPurchaseCount(session, request); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	want := buildCurrentNPCShopPurchaseCountBody(map[uint32]uint32{602: 1})
	if len(rest) != 0 || !bytes.Equal(ack.Body, want) {
		t.Fatalf("persisted native count body=%x want=%x", ack.Body, want)
	}
}

func TestRealPVFCurrentRaidBoxPurchaseLimits(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current PVF")
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
	for _, row := range []struct {
		id      uint32
		scope   string
		maximum int64
		reset   bool
	}{{10162064, "", 0, false}, {10161942, "account", 10, true}} {
		pricing, err := resolveCurrentNPCShopPricing(shop, items, row.id)
		if err != nil || pricing.DailyPurchaseScope != row.scope || pricing.DailyPurchaseLimit != row.maximum || pricing.DailyPurchaseReset != row.reset {
			t.Fatalf("item=%d pricing=%+v err=%v", row.id, pricing, err)
		}
	}
	// The released entry box has use-count fields, not a purchase quota.
	// Its bare reset flag cannot supply a missing purchase scope/maximum.
	entry, err := items.ResolveItem(10162064)
	if err != nil {
		t.Fatal(err)
	}
	document, err := parseDungeonCardPVFDocument(archive, entry.PVFPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := document.Section("daily purchase limit"); found {
		t.Fatal("entry box unexpectedly gained a daily purchase quota; re-audit actual PVF")
	}
	if _, found := document.Section("reset daily purchase item"); !found {
		t.Fatal("entry box bare purchase-reset flag missing")
	}
	for section, want := range map[string]int64{"total usable count": 1, "total weekly usable count": 2, "total weekly usable count reset day": 4} {
		if got, found := document.Int(section); !found || got != want {
			t.Fatalf("entry box use-limit field %s=%d found=%v, want=%d", section, got, found, want)
		}
	}
	// The actual raid shop is an [etc shop] with no [NPC] field.
	// Do not invent an NPC326 association from the globally listed product.
	raidShop, err := parseDungeonCardPVFDocument(archive, "itemshop/raidshop_anton.shp")
	if err != nil {
		t.Fatal(err)
	}
	if _, found := raidShop.Int("NPC"); found {
		t.Fatal("raid shop gained an NPC association; re-audit actual PVF")
	}
	listed := false
	for _, id := range raidShop.Ints("item list") {
		listed = listed || id == 10162064
	}
	entryPricing, err := resolveCurrentNPCShopPricing(shop, items, 10162064)
	if !listed || !currentNPCShopContainsItem(shop, 10162064) || err != nil || !entryPricing.Buyable ||
		entryPricing.NeedMaterialItem != 3341 || entryPricing.NeedMaterialCount != 50 {
		t.Fatalf("actual raid shop/material pricing: listed=%v pricing=%+v err=%v", listed, entryPricing, err)
	}
}
