package cerashop

import (
	"context"
	r "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestCheckoutReservesPVFRestrictedAmountsBeforeGeneralCart(t *testing.T) {
	ctx := context.Background()
	g := seededCheckoutRepositories(t, 50)
	a, _, _ := g.Account.Load(ctx, "account-1")
	SetTokenCeraBalance(&a, 80)
	SetHappyTokenCeraBalance(&a, 20)
	g.Account.Save(ctx, a)
	o, _ := NewOwner(g)
	result, e := o.Checkout(ctx, CheckoutCommand{AccountID: "account-1", CharacterID: "22", SettingsScope: r.CharacterContainerStateScope("22"), Cost: 130, CeraOnlyCost: 50, TokenOnlyCost: 20, PayPolicy: PayPolicyWaterfall, Project: func(*CheckoutAssets) (CheckoutChanges, error) { return CheckoutChanges{}, nil }})
	if e != nil || result.WalletAfter.Cera != 0 || result.WalletAfter.HappyTokenCera != 0 || result.WalletAfter.TokenCera != 20 || result.Payment.Total() != 130 {
		t.Fatal(result, e)
	}
	before, _, _ := g.Account.Load(ctx, "account-1")
	_, e = o.Checkout(ctx, CheckoutCommand{AccountID: "account-1", CharacterID: "22", SettingsScope: r.CharacterContainerStateScope("22"), Cost: 1, CeraOnlyCost: 1, PayPolicy: PayPolicyWaterfall, Project: func(*CheckoutAssets) (CheckoutChanges, error) {
		t.Fatal("点券不足仍发货")
		return CheckoutChanges{}, nil
	}})
	if e == nil {
		t.Fatal("纯点券商品借用代币券")
	}
	after, _, _ := g.Account.Load(ctx, "account-1")
	if WalletBalance(before) != WalletBalance(after) {
		t.Fatal("失败扣款")
	}
}
