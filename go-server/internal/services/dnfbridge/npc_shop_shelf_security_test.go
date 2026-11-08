package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
)

func TestNPCShopShelfRejectsForgedShopAndActorWithoutDebiting(t *testing.T) {
	shop, items := mustCurrentNPCShopTestCatalogs(t)
	repos := mustCurrentNPCShopRepositories(t, 1000, nil)
	service := currentNPCShopTestService(repos, shop, items)
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
	for _, request := range []currentNPCShopBuyRequest{
		{ItemID: 600, Count: 3, ShopContext: 2, ActorContext: 326}, // missing shop file
		{ItemID: 601, Count: 3, ShopContext: 1, ActorContext: 326}, // positive price, not on this shelf
		{ItemID: 600, Count: 3, ShopContext: 1, ActorContext: 999}, // NPC mismatch
		{ItemID: 600, Count: 3, ShopContext: 0, ActorContext: 0},   // old global union bypass
	} {
		if err := validateCurrentNPCShopBuyShelf(shop, request); !errors.Is(err, errCurrentNPCShopProductUnavailable) {
			t.Fatalf("forged shelf accepted: %+v err=%v", request, err)
		}
		if err := service.handleCurrentNPCShopBuy(session, currentNPCShopBuyTestBody(request.ItemID, request.Count, request.ShopContext, request.ActorContext)); err != nil {
			t.Fatal(err)
		}
		character, _, _ := repos.Character.Load(context.Background(), "19")
		inventory, _, _ := repos.Inventory.Load(context.Background(), "19")
		if character.Stats["gold"] != 1000 || len(inventory.Slots) != 0 {
			t.Fatalf("forged purchase mutated assets: request=%+v gold=%d slots=%d", request, character.Stats["gold"], len(inventory.Slots))
		}
	}
	if err := validateCurrentNPCShopBuyShelf(shop, currentNPCShopBuyRequest{ItemID: 600, ShopContext: 1, ActorContext: 326}); err != nil {
		t.Fatalf("valid shelf rejected: %v", err)
	}
}

func TestAikenSupportTitleRealPVFShopCostsOneGold(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("matching candidate PVF required")
	}
	source, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	shop, err := newCurrentNPCShopCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	request := currentNPCShopBuyRequest{ItemID: uint32(currentAikenSupportTitleOwnedID), ShopContext: 247, ActorContext: 0}
	if err := validateCurrentNPCShopBuyShelf(shop, request); err != nil {
		t.Fatal(err)
	}
	if err := validateCurrentNPCShopBuyShelf(shop, currentNPCShopBuyRequest{ItemID: 10161536, ShopContext: 247, ActorContext: 0}); err != nil {
		t.Fatalf("normal Aiken material shop blocked: %v", err)
	}
	if err := validateCurrentNPCShopBuyShelf(shop, currentNPCShopBuyRequest{ItemID: request.ItemID, ShopContext: 247, ActorContext: 486}); err != nil {
		t.Fatalf("Shua actor-scoped title shelf blocked: %v", err)
	}
	if err := validateCurrentNPCShopBuyShelf(shop, currentNPCShopBuyRequest{ItemID: request.ItemID, ShopContext: 0, ActorContext: 476}); err == nil {
		t.Fatal("title purchased through Jonathan trading shop")
	}
	if err := validateCurrentNPCShopBuyShelf(shop, currentNPCShopBuyRequest{ItemID: 10161536, ShopContext: 247, ActorContext: 476}); err == nil {
		t.Fatal("itemshop NPC mismatch accepted")
	}
	pricing, err := resolveCurrentNPCShopActorPricing(shop, items, request.ItemID, request.ActorContext)
	if err != nil || !pricing.Buyable || pricing.BuyGold != 1 {
		t.Fatalf("title pricing=%+v err=%v", pricing, err)
	}
	if err := validateCurrentNPCShopBuyShelf(shop, currentNPCShopBuyRequest{ItemID: uint32(currentAikenSupportTitleCombatID), ShopContext: 247, ActorContext: 0}); err == nil {
		t.Fatal("internal combat title became purchasable")
	}
	repos := mustCurrentNPCShopRepositories(t, 100, nil)
	service := currentNPCShopTestService(repos, shop, items)
	result, err := service.commitCurrentNPCShopBuy(context.Background(), &gameSession{selectedCharacterID: 19}, shop, items, request)
	if err != nil || result.GoldAfter != 99 || result.ItemID != uint32(currentAikenSupportTitleOwnedID) {
		t.Fatalf("title buy result=%+v err=%v", result, err)
	}
	character, _, _ := repos.Character.Load(context.Background(), "19")
	if character.Stats["gold"] != 99 {
		t.Fatalf("gold=%d want99", character.Stats["gold"])
	}
	var got bool
	inventory, _, _ := repos.Inventory.Load(context.Background(), "19")
	for _, item := range inventory.Slots {
		if item.ItemID == currentAikenSupportTitleOwnedID && item.Count == 1 {
			got = true
		}
	}
	if !got {
		t.Fatal("purchased title missing from inventory")
	}
	towns, err := town.Load(context.Background(), source, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var townID, areaID int64
	for _, entry := range towns.Towns() {
		for _, area := range entry.Areas {
			if strings.Contains(strings.ToLower(area.MapPath), "dvildom_c.map") {
				townID, areaID = entry.ID, area.ID
			}
		}
	}
	if townID == 0 {
		t.Fatal("Aiken entrance town unavailable")
	}
	service.townCatalog = towns
	service.initialEquipmentArchive = source
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
	service.gameSessions = map[uint16]*gameSession{19: session}
	body := currentNPCShopBuyTestBody(request.ItemID, 0, 247, 0)
	if err := service.handleCurrentNPCShopBuy(session, body); err != nil {
		t.Fatal(err)
	}
	character, _, _ = repos.Character.Load(context.Background(), "19")
	if character.Stats["gold"] != 99 {
		t.Fatal("title bought without ready NPC scene")
	}
	session.conn.(*bufferConn).write.Reset()
	session.townSceneReadyCharacterID = 19
	session.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 19, TownID: byte(townID), AreaID: byte(areaID), PositionValid: true}
	if err := service.handleCurrentNPCShopBuy(session, body); err != nil {
		t.Fatal(err)
	}
	character, _, _ = repos.Character.Load(context.Background(), "19")
	if character.Stats["gold"] != 99 {
		t.Fatalf("limited repeat charged gold=%d want99", character.Stats["gold"])
	}
	ack, tail := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
	if len(tail) != 0 || !bytes.Equal(ack.Body, []byte{0, 0x85}) {
		t.Fatalf("ready NPC repeat limit response=%x tail=%d", ack.Body, len(tail))
	}
}
