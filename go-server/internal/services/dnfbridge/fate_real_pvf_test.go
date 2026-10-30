package dnfbridge

import (
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/raidcurrency"
)

// The current Script.pvf chains shop 259 -> itemshop/raidshop_trophy.shp ->
// product 10304528. The product is bought with 40 account-bound Fate material
// (10304529) and has no gold [price]/[value]. Its document declares a character
// weekly limit of 12. Keep every value document-derived so a PVF drift fails the
// test instead of being hidden by the synthetic NPC-shop fixture.
func TestRealPVFFateAccountMaterialTrophyShopAndPurchaseContract(t *testing.T) {
	const (
		trophyShopID int64  = 259
		productID    uint32 = 10304528
		fateItemID   uint32 = 10304529
		materialCost int64  = 40
		weeklyLimit  int64  = 12
	)
	if raidcurrency.FateItemID != fateItemID {
		t.Fatalf("raidcurrency.FateItemID=%d, want actual PVF item %d", raidcurrency.FateItemID, fateItemID)
	}

	shop, items := realPVF535(t)
	if shop == nil || shop.source == nil || items == nil {
		t.Fatal("real NPC-shop catalogs are unavailable")
	}

	fate, err := items.ResolveItem(fateItemID)
	if err != nil {
		t.Fatalf("resolve Fate material %d: %v", fateItemID, err)
	}
	if fate.Kind != dungeonDropItemStackable ||
		!strings.EqualFold(strings.TrimSpace(fate.AttachType), "[account]") ||
		!strings.EqualFold(strings.TrimSpace(fate.StackableType), "[material]") {
		t.Fatalf("Fate definition kind/attach/stackable=%q/%q/%q, want stackable/[account]/[material]",
			fate.Kind, fate.AttachType, fate.StackableType)
	}
	if fate.PVFPath != "stackable/10304001/10304529.stk" {
		t.Fatalf("Fate PVF path=%q", fate.PVFPath)
	}

	shopPaths, _, err := loadDungeonDropList(shop.source, currentNPCShopItemListPath)
	if err != nil {
		t.Fatalf("load itemshop list: %v", err)
	}
	listedShopPath := strings.TrimSpace(shopPaths[trophyShopID])
	if !strings.EqualFold(listedShopPath, "raidshop_trophy.shp") {
		t.Fatalf("shop %d path=%q, want raidshop_trophy.shp", trophyShopID, listedShopPath)
	}
	documentPath, _, err := readDungeonDropText(shop.source, "itemshop", listedShopPath)
	if err != nil {
		t.Fatalf("read shop %d: %v", trophyShopID, err)
	}
	trophyShop, err := parseDungeonCardPVFDocument(shop.source, documentPath)
	if err != nil {
		t.Fatalf("parse shop %d at %s: %v", trophyShopID, documentPath, err)
	}
	listedProduct := false
	for _, itemID := range trophyShop.Ints("item list") {
		if itemID == int64(productID) {
			listedProduct = true
			break
		}
	}
	if !listedProduct || !currentNPCShopContainsItem(shop, productID) {
		t.Fatalf("product %d listed_in_shop_259=%t loaded=%t", productID, listedProduct, currentNPCShopContainsItem(shop, productID))
	}

	pricing, err := resolveCurrentNPCShopPricing(shop, items, productID)
	if err != nil {
		t.Fatalf("resolve actual product %d pricing: %v", productID, err)
	}
	if pricing.Definition.PVFPath != "stackable/10304001/10304528.stk" {
		t.Fatalf("product PVF path=%q", pricing.Definition.PVFPath)
	}
	if !pricing.Buyable || !pricing.MaterialExchange ||
		pricing.NeedMaterialItem != fateItemID || pricing.NeedMaterialCount != materialCost {
		t.Fatalf("product material contract=%+v, want item=%d count=%d", pricing, fateItemID, materialCost)
	}
	if len(pricing.Definition.NeedMaterials) != 2 ||
		pricing.Definition.NeedMaterials[0] != int64(fateItemID) ||
		pricing.Definition.NeedMaterials[1] != materialCost {
		t.Fatalf("raw [need material]=%v, want [%d %d]", pricing.Definition.NeedMaterials, fateItemID, materialCost)
	}
	if pricing.BuyGold != 0 || pricing.ExchangeGold != 0 {
		t.Fatalf("product gold price=%d exchange_gold=%d, want material-only zero gold", pricing.BuyGold, pricing.ExchangeGold)
	}
	productDocument, err := parseDungeonCardPVFDocument(shop.source, pricing.Definition.PVFPath)
	if err != nil {
		t.Fatalf("parse product %d at %s: %v", productID, pricing.Definition.PVFPath, err)
	}
	if _, found := productDocument.Int("price"); found {
		t.Fatal("product gained a [price] section; re-audit the material price")
	}
	if _, found := productDocument.Int("value"); found {
		t.Fatal("product gained a [value] section; re-audit the zero-gold price")
	}

	if pricing.PurchaseLimitScope != "character" ||
		pricing.PurchaseLimitPeriod != "weekly" ||
		pricing.PurchaseLimitValue != weeklyLimit || pricing.PurchaseLimitReset {
		t.Fatalf("purchase limit=%s/%s/%d reset=%t, want character/weekly/%d reset=false",
			pricing.PurchaseLimitScope, pricing.PurchaseLimitPeriod, pricing.PurchaseLimitValue,
			pricing.PurchaseLimitReset, weeklyLimit)
	}
	policy := currentNPCShopPurchaseLimit(pricing, 1)
	if policy == nil || policy.ItemID != productID || policy.Scope != "character" ||
		policy.Period != "weekly" || policy.Maximum != weeklyLimit || policy.ResetDaily || policy.Count != 1 {
		t.Fatalf("purchase policy=%+v, want product=%d character/weekly/%d count=1", policy, productID, weeklyLimit)
	}
}
