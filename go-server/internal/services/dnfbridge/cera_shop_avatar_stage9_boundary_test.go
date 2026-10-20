package dnfbridge

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// The reported stage-9 purchase is advertised by the real catalog, but the
// existing client/container contract caps expansion at 56. Until a current-EXE
// contract proves higher stages, rejection must preserve both money and assets.
func TestRealPVFCeraShopAvatarStage9Through15RejectWithoutMutation(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("requires real cera shop and coupon metadata")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFCeraShopCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	seedCeraShopAccountCera(t, ctx, repos, "account-1", 50000)
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "31", AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "31", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	before := dnfrepo.SettingsRecord{Scope: dnfrepo.CharacterContainerStateScope("31"), Values: map[string]string{"avatar_list_param16": "56"}}
	if err := repos.Settings.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	before, _, err = repos.Settings.Load(ctx, before.Scope)
	if err != nil {
		t.Fatal(err)
	}
	beforeAccount, _, err := repos.Account.Load(ctx, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	beforeInventory, _, err := repos.Inventory.Load(ctx, "31")
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{accountID: "account-1"}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	for commodity := uint32(102652); commodity <= 102658; commodity++ {
		product, found := catalog.products[commodity]
		if !found || product.ItemID != commodity-102652+2683909 || product.CeraPrice != 1980 {
			t.Fatalf("real unsupported-stage commodity %d=%+v found=%t", commodity, product, found)
		}
		_, err := s.commitCurrentCeraShopPurchase(ctx, &gameSession{selectedCharacterID: 31}, catalog, currentCeraShopPurchaseRequest{
			PaymentMode: currentCeraShopPaymentModeCera,
			Items:       []currentCeraShopPurchaseRequestItem{{CommodityID: commodity}},
		})
		if !errors.Is(err, errCurrentCeraShopProductUnavailable) {
			t.Fatalf("commodity=%d error=%v", commodity, err)
		}
		after, found, err := repos.Settings.Load(ctx, before.Scope)
		if err != nil || !found || !reflect.DeepEqual(after, before) {
			t.Fatalf("rejected commodity %d changed expansion: %+v err=%v", commodity, after, err)
		}
		afterAccount, found, err := repos.Account.Load(ctx, "account-1")
		if err != nil || !found || !reflect.DeepEqual(afterAccount, beforeAccount) {
			t.Fatalf("rejected commodity %d changed account: err=%v", commodity, err)
		}
		afterInventory, found, err := repos.Inventory.Load(ctx, "31")
		if err != nil || !found || !reflect.DeepEqual(afterInventory, beforeInventory) {
			t.Fatalf("rejected commodity %d changed inventory: %+v err=%v", commodity, afterInventory, err)
		}
	}
}
