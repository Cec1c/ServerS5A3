package dnfbridge

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/cerashop"
	"longheng.io/server/internal/modules/dnf/mileage"
	r "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
)

type mileageLateInventoryFailure struct{ r.InventoryRepository }

func (m mileageLateInventoryFailure) Save(context.Context, r.InventoryRecord) error {
	return errors.New("injected inventory SQL boundary failure")
}

type mileageFailureAssets struct{ base r.CeraShopAssetUnitOfWork }

func (m mileageFailureAssets) WithinCeraShopAssets(ctx context.Context, a, c, s string, apply func(r.AccountRepository, r.CharacterRepository, r.InventoryRepository, r.EquipmentRepository, r.SettingsRepository) error) error {
	return m.base.WithinCeraShopAssets(ctx, a, c, s, func(ar r.AccountRepository, cr r.CharacterRepository, ir r.InventoryRepository, er r.EquipmentRepository, sr r.SettingsRepository) error {
		return apply(ar, cr, mileageLateInventoryFailure{ir}, er, sr)
	})
}
func TestMileageMySQLAtomicCashbackAndRedemption(t *testing.T) {
	ctx := context.Background()
	g := clearSettlementMySQLFixture(t)
	c := mustCurrentCeraShopTestCatalog(t)
	c.source.(bridgePVFSource)["etc/mileageshop_chn.etc"] = "[item]\n1001 37 3 0 5 0 `one` 0 0\n[/item]\n[limit mileage by cash]\n20000\n[/limit mileage by cash]"
	s, p, seed := mileageBridgeFixture(t, c)
	a, _, _ := seed.Account.Load(ctx, "account-1")
	a.Metadata["account_cera"] = "100"
	a.Metadata["account_token_cera"] = "100"
	a.Metadata["account_happy_token_cera"] = "100"
	mileage.Save(&a, mileage.State{Version: 1, Balance: 100})
	if err := g.Account.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	ch, _, _ := seed.Character.Load(ctx, "19")
	if err := g.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	inv, _, _ := seed.Inventory.Load(ctx, "19")
	if err := g.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	s.repositoryProvider = func() (r.Group, bool) { return g, true }
	owner, _ := cerashop.NewOwner(g)
	cmd := cerashop.CheckoutCommand{AccountID: "account-1", CharacterID: "19", SettingsScope: r.CharacterContainerStateScope("19"), Cost: 300, PayPolicy: cerashop.PayPolicyWaterfall, MileageCashLimit: 20000, Project: func(a *cerashop.CheckoutAssets) (cerashop.CheckoutChanges, error) {
		return cerashop.CheckoutChanges{}, nil
	}}
	if _, err := owner.Checkout(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	if got := mileageBalance(t, g); got.Balance != 109 || got.CashEarned != 9 {
		t.Fatal(got)
	}
	before, _, _ := g.Account.Load(ctx, "account-1")
	broken := g
	broken.CeraShopAssets = mileageFailureAssets{g.CeraShopAssets}
	badOwner, _ := cerashop.NewOwner(broken)
	_, err := badOwner.Checkout(ctx, cerashop.CheckoutCommand{AccountID: "account-1", CharacterID: "19", SettingsScope: r.CharacterContainerStateScope("19"), Preflight: func(a *r.AccountRecord) error {
		return mileage.Redeem(a, "19", "failure", 1, []mileage.Product{{Commodity: 1001, Price: 5, Count: 3}})
	}, Project: func(a *cerashop.CheckoutAssets) (cerashop.CheckoutChanges, error) {
		a.Inventory.Slots = map[string]r.ItemStack{"0:9": {ItemID: 37, Count: 3}}
		return cerashop.CheckoutChanges{Inventory: true}, nil
	}})
	if err == nil {
		t.Fatal("late inventory failure committed")
	}
	a, _, _ = g.Account.Load(ctx, "account-1")
	if !reflect.DeepEqual(a.Metadata, before.Metadata) {
		t.Fatal("SQL account write survived inventory rollback")
	}
	seq := p.guild.purchaseSequence.observe(100)
	for i := 0; i < 2; i++ {
		if err := s.handleCurrentCeraShopPurchase(p, mileageRequest(1001), &seq); err != nil {
			t.Fatal(err)
		}
	}
	if got := mileageBalance(t, g); got.Balance != 104 || got.CashEarned != 9 {
		t.Fatal(got)
	}
	inv, _, _ = g.Inventory.Load(ctx, "19")
	var count int64
	for _, v := range inv.Slots {
		if v.ItemID == 37 {
			count += v.Count
		}
	}
	if count != 3 {
		t.Fatal("duplicate/missing SQL grant", count)
	}
	a, _, _ = g.Account.Load(ctx, "account-1")
	if w := cerashop.WalletBalance(a); w != (cerashop.Wallet{}) {
		t.Fatal(w)
	}
}
