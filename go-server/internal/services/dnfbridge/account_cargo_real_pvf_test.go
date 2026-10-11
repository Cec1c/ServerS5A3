package dnfbridge

import (
	"context"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"strconv"
	"testing"
)

func TestRealPVFAccountCargoPurchasesFrom64To120(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFCeraShopCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	const initialCera int64 = 1000000
	seedCeraShopAccountCera(t, ctx, repositories, "cargo-real", initialCera)
	account, _, _ := repositories.Account.Load(ctx, "cargo-real")
	account.Metadata["account_cargo_created"] = "true"
	account.Metadata["account_cargo_level"] = "64"
	if err := repositories.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "29", AccountID: "cargo-real", Stats: map[string]int64{"gold": 99}}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "29", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	service := &Service{options: options{accountID: "cargo-real"}, repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true }}
	session := &gameSession{selectedCharacterID: 29}
	remaining := initialCera
	for index, commodity := range []uint32{103665, 103666, 103667, 103668, 103669, 103670, 103671} {
		product, found := catalog.Product(commodity)
		if !found || product.CeraPrice == 0 {
			t.Fatalf("PVF commodity %d missing priced row", commodity)
		}
		request, err := parseCurrentCeraShopPurchaseRequest(currentCeraShopTestRequestBody(commodity))
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.commitCurrentCeraShopPurchase(ctx, session, catalog, request)
		if err != nil {
			t.Fatalf("commodity %d: %v", commodity, err)
		}
		remaining -= int64(product.CeraPrice)
		capacity := int64(72 + index*8)
		if int64(result.AccountCargoSelectionKey) != capacity || result.CeraAfter != remaining || len(result.Updates) != 0 {
			t.Fatalf("commodity %d result=%+v", commodity, result)
		}
		account, _, _ := repositories.Account.Load(ctx, "cargo-real")
		if account.Metadata["account_cargo_level"] != strconv.FormatInt(capacity, 10) {
			t.Fatalf("capacity metadata=%v", account.Metadata)
		}
	}
	request, _ := parseCurrentCeraShopPurchaseRequest(currentCeraShopTestRequestBody(103671))
	if _, err := service.commitCurrentCeraShopPurchase(ctx, session, catalog, request); err == nil {
		t.Fatal("purchase past 120 accepted")
	}
	if balance := loadCeraShopAccountCera(t, ctx, repositories, "cargo-real"); balance != remaining {
		t.Fatalf("rejected max purchase deducted cera: %d != %d", balance, remaining)
	}
}
