package cerashop

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/mileage"
	r "longheng.io/server/internal/modules/dnf/repository"
	"sync"
	"testing"
)

func TestMileageCheckoutAllWalletsAndAtomicFailure(t *testing.T) {
	ctx := context.Background()
	repos := seededCheckoutRepositories(t, 1000)
	a, _, _ := repos.Account.Load(ctx, "account-1")
	a.Metadata["account_token_cera"] = "100"
	a.Metadata["account_happy_token_cera"] = "100"
	repos.Account.Save(ctx, a)
	owner, _ := NewOwner(repos)
	cmd := CheckoutCommand{AccountID: "account-1", CharacterID: "22", SettingsScope: r.CharacterContainerStateScope("22"), Cost: 300, PayPolicy: PayPolicyWaterfall, MileageCashLimit: 20000, Project: func(a *CheckoutAssets) (CheckoutChanges, error) { return CheckoutChanges{}, nil }}
	result, err := owner.Checkout(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if result.Payment.FromCera != 100 || result.Payment.FromTokenCera != 100 || result.Payment.FromHappyTokenCera != 100 {
		t.Fatal(result)
	}
	a, _, _ = repos.Account.Load(ctx, "account-1")
	s, _ := mileage.Load(a)
	if s.Balance != 9 || s.CashEarned != 9 {
		t.Fatal(s)
	}
	before := r.CloneAccount(a)
	cmd.Project = func(a *CheckoutAssets) (CheckoutChanges, error) {
		return CheckoutChanges{}, errors.New("late grant failure")
	}
	if _, err := owner.Checkout(ctx, cmd); err == nil {
		t.Fatal("failure committed")
	}
	a, _, _ = repos.Account.Load(ctx, "account-1")
	if a.Metadata[mileage.MetadataKey] != before.Metadata[mileage.MetadataKey] || Balance(a) != Balance(before) {
		t.Fatal("failed grant charged wallet/awarded points")
	}
}

func TestMileageConcurrentRedemptionCommitsOnce(t *testing.T) {
	ctx := context.Background()
	repos := seededCheckoutRepositories(t, 1000)
	owner, _ := NewOwner(repos)
	a, _, _ := repos.Account.Load(ctx, "account-1")
	mileage.AccrueSpend(&a, 1000, 20000)
	repos.Account.Save(ctx, a)
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := owner.Checkout(ctx, CheckoutCommand{AccountID: "account-1", CharacterID: "22", SettingsScope: r.CharacterContainerStateScope("22"), Preflight: func(a *r.AccountRecord) error {
				return mileage.Redeem(a, "22", "nonce", 123, []mileage.Product{{Commodity: 7, Price: 10, Count: 1}})
			}, Project: func(a *CheckoutAssets) (CheckoutChanges, error) {
				if a.Inventory.Slots == nil {
					a.Inventory.Slots = map[string]r.ItemStack{}
				}
				a.Inventory.Slots["0:9"] = r.ItemStack{ItemID: 7, Count: 1}
				return CheckoutChanges{Inventory: true}, nil
			}})
			mu.Lock()
			if err == nil {
				successes++
			} else if !errors.Is(err, mileage.ErrReplay) {
				t.Errorf("error=%v", err)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	a, _, _ = repos.Account.Load(ctx, "account-1")
	s, _ := mileage.Load(a)
	if successes != 1 || s.Balance != 20 || Balance(a) != 1000 {
		t.Fatal(successes, s, Balance(a))
	}
}
