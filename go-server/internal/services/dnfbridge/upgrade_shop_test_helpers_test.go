package dnfbridge

import (
	"context"
	"errors"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
)

const (
	privateAikenExchangeMaterialID  uint32 = 10161538
	privateAikenExchangeShop        uint32 = 247
	privateAikenExchangeActor       uint32 = 486
	privateAikenExchangeMaterialKey        = "0:121"
	privateAikenExchangeGold        int64  = 1000
)

func privateAikenExchangeAssertBuy(t *testing.T, shop *currentNPCShopCatalog, items *pvfDungeonDropCatalog, itemID uint32, cost int64, count uint32) {
	t.Helper()
	materialBefore := cost*int64(count) + 1
	repositories := mustCurrentNPCShopRepositories(t, privateAikenExchangeGold, map[string]dnfrepo.ItemStack{
		privateAikenExchangeMaterialKey: {ItemID: int64(privateAikenExchangeMaterialID), Count: materialBefore},
	})
	service := currentNPCShopTestService(repositories, shop, items)
	result, err := service.commitCurrentNPCShopBuy(context.Background(), &gameSession{selectedCharacterID: 19}, shop, items, currentNPCShopBuyRequest{
		ItemID:       itemID,
		Count:        count,
		ShopContext:  privateAikenExchangeShop,
		ActorContext: privateAikenExchangeActor,
	})
	if err != nil {
		t.Fatal(err)
	}
	character, characterFound, err := repositories.Character.Load(context.Background(), "19")
	if err != nil || !characterFound {
		t.Fatalf("load character: found=%t err=%v", characterFound, err)
	}
	bag, bagFound, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil || !bagFound {
		t.Fatalf("load inventory: found=%t err=%v", bagFound, err)
	}
	materialAfter := materialBefore - cost*int64(count)
	if result.GoldAfter != privateAikenExchangeGold || character.Stats["gold"] != privateAikenExchangeGold {
		t.Fatalf("material exchange charged gold: result=%d stored=%d", result.GoldAfter, character.Stats["gold"])
	}
	if result.CostItemID != privateAikenExchangeMaterialID || result.CostItemNewCount != materialAfter {
		t.Fatalf("material result=%d/%d want=%d/%d", result.CostItemID, result.CostItemNewCount, privateAikenExchangeMaterialID, materialAfter)
	}
	deliveredID := itemID
	if itemID == 990031104 {
		deliveredID = 2681593
	}
	if result.ItemID != deliveredID || result.Count != count {
		t.Fatalf("product result=%d x%d want=%d x%d", result.ItemID, result.Count, deliveredID, count)
	}
	if got := bag.Slots[privateAikenExchangeMaterialKey]; got.ItemID != int64(privateAikenExchangeMaterialID) || got.Count != materialAfter {
		t.Fatalf("material stack=%+v want=%d x%d", got, privateAikenExchangeMaterialID, materialAfter)
	}
	assertCurrentNPCShopInventoryAmount(t, bag, int64(deliveredID), int64(count))
	if itemID == 990031104 {
		assertCurrentNPCShopInventoryAmount(t, bag, int64(itemID), 0)
		// The item actually bought must be usable by the existing advanced,
		// permanent avatar checkout, whose client/server use the original ID.
		coupon := currentCeraShopAvatarCouponItemIDs[2][3]
		if coupon != int64(deliveredID) {
			t.Fatalf("checkout coupon=%d delivered=%d", coupon, deliveredID)
		}
		if _, err := consumeCurrentCeraShopAvatarCoupon(&bag, coupon); err != nil {
			t.Fatalf("bought voucher cannot pay for an avatar: %v", err)
		}
		assertCurrentNPCShopInventoryAmount(t, bag, coupon, int64(count)-1)
	}
}

func privateAikenExchangeAssertOneShort(t *testing.T, shop *currentNPCShopCatalog, items *pvfDungeonDropCatalog, itemID uint32, cost int64, count uint32) {
	t.Helper()
	materialBefore := cost*int64(count) - 1
	repositories := mustCurrentNPCShopRepositories(t, privateAikenExchangeGold, map[string]dnfrepo.ItemStack{
		privateAikenExchangeMaterialKey: {ItemID: int64(privateAikenExchangeMaterialID), Count: materialBefore},
	})
	beforeCharacter, beforeCharacterFound, err := repositories.Character.Load(context.Background(), "19")
	if err != nil || !beforeCharacterFound {
		t.Fatalf("load character before rejection: found=%t err=%v", beforeCharacterFound, err)
	}
	beforeBag, beforeBagFound, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil || !beforeBagFound {
		t.Fatalf("load inventory before rejection: found=%t err=%v", beforeBagFound, err)
	}
	service := currentNPCShopTestService(repositories, shop, items)
	_, err = service.commitCurrentNPCShopBuy(context.Background(), &gameSession{selectedCharacterID: 19}, shop, items, currentNPCShopBuyRequest{
		ItemID:       itemID,
		Count:        count,
		ShopContext:  privateAikenExchangeShop,
		ActorContext: privateAikenExchangeActor,
	})
	if !errors.Is(err, errCurrentNPCShopMaterialInsufficient) {
		t.Fatalf("one-short exchange error=%v want material insufficient", err)
	}
	privateAikenExchangeAssertAssetsEqual(t, repositories, beforeCharacter, beforeBag)
}

func privateAikenExchangeAssertCommitFailureRollsBack(t *testing.T, shop *currentNPCShopCatalog, items *pvfDungeonDropCatalog, itemID uint32, cost int64) {
	t.Helper()
	const count uint32 = 3
	repositories := mustCurrentNPCShopRepositories(t, privateAikenExchangeGold, map[string]dnfrepo.ItemStack{
		privateAikenExchangeMaterialKey: {ItemID: int64(privateAikenExchangeMaterialID), Count: cost * int64(count)},
	})
	beforeCharacter, beforeCharacterFound, err := repositories.Character.Load(context.Background(), "19")
	if err != nil || !beforeCharacterFound {
		t.Fatalf("load character before rollback: found=%t err=%v", beforeCharacterFound, err)
	}
	beforeBag, beforeBagFound, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil || !beforeBagFound {
		t.Fatalf("load inventory before rollback: found=%t err=%v", beforeBagFound, err)
	}
	commitErr := errors.New("private Aiken exchange commit fault")
	repositories.AccountAssets = rejectingCurrentNPCShopAccountAssets{inner: repositories.AccountAssets, err: commitErr}
	service := currentNPCShopTestService(repositories, shop, items)
	_, err = service.commitCurrentNPCShopBuy(context.Background(), &gameSession{selectedCharacterID: 19}, shop, items, currentNPCShopBuyRequest{
		ItemID:       itemID,
		Count:        count,
		ShopContext:  privateAikenExchangeShop,
		ActorContext: privateAikenExchangeActor,
	})
	if !errors.Is(err, commitErr) {
		t.Fatalf("commit failure error=%v want injected fault", err)
	}
	privateAikenExchangeAssertAssetsEqual(t, repositories, beforeCharacter, beforeBag)
}

func privateAikenExchangeAssertAssetsEqual(t *testing.T, repositories dnfrepo.Group, beforeCharacter dnfrepo.CharacterRecord, beforeBag dnfrepo.InventoryRecord) {
	t.Helper()
	afterCharacter, characterFound, err := repositories.Character.Load(context.Background(), "19")
	if err != nil || !characterFound {
		t.Fatalf("load character after failure: found=%t err=%v", characterFound, err)
	}
	afterBag, bagFound, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil || !bagFound {
		t.Fatalf("load inventory after failure: found=%t err=%v", bagFound, err)
	}
	if !reflect.DeepEqual(beforeCharacter, afterCharacter) || !reflect.DeepEqual(beforeBag, afterBag) {
		t.Fatalf("failed exchange changed character or inventory: character=%t inventory=%t", reflect.DeepEqual(beforeCharacter, afterCharacter), reflect.DeepEqual(beforeBag, afterBag))
	}
}
