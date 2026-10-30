package dnfbridge

import (
	"context"
	"errors"
	"testing"
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
