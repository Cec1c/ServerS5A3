package npcshop

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestPurchaseLimitTransactionScopeResetAndRollback(t *testing.T) {
	for _, scope := range []string{"character", "account"} {
		t.Run(scope, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			seedNPCShopOwner(t, ctx, repos)
			c, _, _ := repos.Character.Load(ctx, "77")
			c.CharacterID = "78"
			if err := repos.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "78", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
				t.Fatal(err)
			}
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			before := time.Date(2026, 10, 1, 21, 59, 0, 0, time.UTC) // 05:59 Beijing
			policy := PurchaseLimit{ItemID: 600, Scope: scope, Maximum: 1, ResetDaily: true, Count: 1}
			buy := func(id string, now time.Time, reject bool) error {
				return owner.Mutate(ctx, Command{AccountID: "dnf:1", CharacterID: id, UpdatedAt: now, PurchaseLimit: &policy, Project: func(a *Assets) (Changes, error) {
					a.Character.Stats["gold"] -= 100
					a.Inventory.Slots["0:65"] = dnfrepo.ItemStack{ItemID: 600, Count: a.Inventory.Slots["0:65"].Count + 1}
					if reject {
						return Changes{}, errors.New("grant failed after quota reservation")
					}
					return Changes{Character: true, Inventory: true}, nil
				}})
			}
			if err := buy("77", before, true); err == nil {
				t.Fatal("expected rollback")
			}
			if err := buy("77", before, false); err != nil {
				t.Fatal(err)
			}
			if err := buy("77", before, false); !errors.Is(err, ErrPurchaseLimit) {
				t.Fatalf("duplicate: %v", err)
			}
			err = buy("78", before, false)
			if scope == "account" && !errors.Is(err, ErrPurchaseLimit) {
				t.Fatalf("shared account: %v", err)
			}
			if scope == "character" && err != nil {
				t.Fatalf("independent character: %v", err)
			}
			account, _, _ := repos.Account.Load(ctx, "dnf:1")
			c, _, _ = repos.Character.Load(ctx, "77")
			if c.Stats["gold"] != 900 || PurchaseCount(account, c, policy, before) != 1 {
				t.Fatalf("failed purchase changed wallet/quota: %+v", c.Stats)
			}
			after := before.Add(time.Minute)
			if PurchaseCount(account, c, policy, after) != 0 {
				t.Fatal("06:00 reset")
			}
			if err := buy("77", after, false); err != nil {
				t.Fatal(err)
			}
			c, _, _ = repos.Character.Load(ctx, "77")
			inventory, _, _ := repos.Inventory.Load(ctx, "77")
			if c.Stats["gold"] != 800 || inventory.Slots["0:65"].Count != 2 {
				t.Fatal("quota rollback/reset did not preserve assets")
			}
		})
	}
}

func TestPurchaseLimitConcurrentRequestsAndBulkCount(t *testing.T) {
	repos := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	seedNPCShopOwner(t, ctx, repos)
	owner, _ := NewOwner(repos)
	policy := PurchaseLimit{ItemID: 600, Scope: "character", Maximum: 1, Count: 2}
	command := Command{AccountID: "dnf:1", CharacterID: "77", PurchaseLimit: &policy, Project: func(a *Assets) (Changes, error) {
		a.Character.Stats["gold"] -= 100
		return Changes{Character: true}, nil
	}}
	if err := owner.Mutate(ctx, command); !errors.Is(err, ErrPurchaseLimit) {
		t.Fatalf("bulk bypass: %v", err)
	}
	policy.Count = 1
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := owner.Mutate(ctx, command)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrPurchaseLimit) {
				t.Errorf("purchase: %v", err)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("commits=%d", successes.Load())
	}
	c, _, _ := repos.Character.Load(ctx, "77")
	if c.Stats["gold"] != 900 {
		t.Fatalf("wallet=%d", c.Stats["gold"])
	}
	account, _, _ := repos.Account.Load(ctx, "dnf:1")
	if PurchaseCount(account, c, policy, time.Now().AddDate(0, 0, 3)) != 1 {
		t.Fatal("nonreset limit must survive day changes")
	}
}
