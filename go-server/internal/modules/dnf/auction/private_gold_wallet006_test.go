package auction

import (
	shop "longheng.io/server/internal/modules/dnf/cerashop"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
)

func TestPrivateGoldConsignWallet006(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after shop.Wallet
		pay           shop.WalletPayment
	}{
		{"happy only", shop.Wallet{HappyTokenCera: 2000}, shop.Wallet{HappyTokenCera: 890}, shop.WalletPayment{FromHappyTokenCera: 1110}},
		{"token only", shop.Wallet{TokenCera: 2000}, shop.Wallet{TokenCera: 890}, shop.WalletPayment{FromTokenCera: 1110}},
		{"cera only", shop.Wallet{Cera: 2000}, shop.Wallet{Cera: 890}, shop.WalletPayment{FromCera: 1110}},
		{"three wallets", shop.Wallet{Cera: 2000, TokenCera: 400, HappyTokenCera: 500}, shop.Wallet{Cera: 1790}, shop.WalletPayment{FromHappyTokenCera: 500, FromTokenCera: 400, FromCera: 210}},
		{"tokens spare cera", shop.Wallet{Cera: 2000, TokenCera: 2000, HappyTokenCera: 500}, shop.Wallet{Cera: 2000, TokenCera: 1390}, shop.WalletPayment{FromHappyTokenCera: 500, FromTokenCera: 610}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, r, o, _ := goldFixture(t)
			a, _, _ := r.Account.Load(ctx, "a-buyer")
			shop.SetBalance(&a, tc.before.Cera)
			shop.SetTokenCeraBalance(&a, tc.before.TokenCera)
			shop.SetHappyTokenCeraBalance(&a, tc.before.HappyTokenCera)
			if err := r.Account.Save(ctx, a); err != nil {
				t.Fatal(err)
			}
			got, err := o.Buy(ctx, goldBuy("buyer"))
			if err != nil {
				t.Fatal(err)
			}
			if got.WalletAfter != tc.after || got.Payment != tc.pay {
				t.Fatalf("wrong debit %+v", got)
			}
			a, _, _ = r.Account.Load(ctx, "a-buyer")
			if shop.WalletBalance(a) != tc.after {
				t.Fatal("debit not saved")
			}
			if goldMailCount(t, ctx, r, "buyer") != 1 {
				t.Fatal("missing gold delivery")
			}
			again, err := o.Buy(ctx, goldBuy("buyer"))
			if err != nil || !again.Repeated {
				t.Fatal(again, err)
			}
			a, _, _ = r.Account.Load(ctx, "a-buyer")
			if shop.WalletBalance(a) != tc.after || goldMailCount(t, ctx, r, "buyer") != 1 {
				t.Fatal("replay debited/delivered twice")
			}
		})
	}
}

func TestPrivateGoldConsignWalletRollback006(t *testing.T) {
	for _, stage := range []string{"mail", "inventory"} {
		t.Run(stage, func(t *testing.T) {
			ctx, r, o, _ := goldFixture(t)
			a, _, _ := r.Account.Load(ctx, "a-buyer")
			shop.SetBalance(&a, 500)
			shop.SetTokenCeraBalance(&a, 500)
			shop.SetHappyTokenCeraBalance(&a, 500)
			if err := r.Account.Save(ctx, a); err != nil {
				t.Fatal(err)
			}
			before := shop.WalletBalance(a)
			inv, _, _ := r.Inventory.Load(ctx, EscrowCharacterID)
			o.repositories.MailboxAssets = goldFailUOW{r.MailboxAssets, r.MailboxAssets.(repo.AccountTradeMailboxUnitOfWork), stage}
			if _, err := o.Buy(ctx, goldBuy("buyer")); err == nil {
				t.Fatal("fault not injected")
			}
			a, _, _ = r.Account.Load(ctx, "a-buyer")
			after, _, _ := r.Inventory.Load(ctx, EscrowCharacterID)
			escrow, _, _ := r.Character.Load(ctx, EscrowCharacterID)
			if shop.WalletBalance(a) != before || !reflect.DeepEqual(inv, after) || escrow.Stats["gold"] != 1000000 || goldMailCount(t, ctx, r, "buyer") != 0 {
				t.Fatal("partial commit")
			}
		})
	}
}
