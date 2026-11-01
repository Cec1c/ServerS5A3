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

func TestPurchaseLimitAccumulateAccountTransactionAndPersistence(t *testing.T) {
	for _, resetDaily := range []bool{false, true} {
		name := "without_reset_marker"
		if resetDaily {
			name = "with_reset_marker"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			seedNPCShopOwner(t, ctx, repos)
			character, found, err := repos.Character.Load(ctx, "77")
			if err != nil || !found {
				t.Fatalf("load character: found=%t err=%v", found, err)
			}
			character.CharacterID = "78"
			if err := repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"77", "78"} {
				if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: id, Slots: map[string]dnfrepo.ItemStack{
					"0:121": {ItemID: 900, Count: 6},
				}}); err != nil {
					t.Fatal(err)
				}
			}
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			// Just before Thursday 06:00 Beijing: crosses both daily and weekly reset.
			before := time.Date(2026, 10, 28, 21, 59, 0, 0, time.UTC)
			policy := PurchaseLimit{ItemID: 600, Scope: "account", Period: "accumulate", Maximum: 1, Count: 1, ResetDaily: resetDaily}
			projectionFailure := errors.New("grant rejected")
			insufficientMaterials := errors.New("insufficient materials")
			buy := func(id string, now time.Time, requested int64, failure string) error {
				p := policy
				p.Count = requested
				return owner.Mutate(ctx, Command{AccountID: "dnf:1", CharacterID: id, UpdatedAt: now, PurchaseLimit: &p,
					Project: func(a *Assets) (Changes, error) {
						material := a.Inventory.Slots["0:121"]
						if material.Count < 2*requested || failure == "payment" {
							return Changes{}, insufficientMaterials
						}
						material.Count -= 2 * requested
						a.Inventory.Slots["0:121"] = material
						if failure == "after_payment" {
							return Changes{}, projectionFailure
						}
						a.Inventory.Slots["0:65"] = dnfrepo.ItemStack{ItemID: 600, Count: a.Inventory.Slots["0:65"].Count + requested}
						if failure == "after_grant" {
							return Changes{}, projectionFailure
						}
						return Changes{Inventory: true}, nil
					}})
			}
			assertState := func(id string, now time.Time, materials, goods, quota int64) {
				t.Helper()
				account, found, err := repos.Account.Load(ctx, "dnf:1")
				if err != nil || !found {
					t.Fatalf("load account: found=%t err=%v", found, err)
				}
				c, found, err := repos.Character.Load(ctx, id)
				if err != nil || !found {
					t.Fatalf("load character: found=%t err=%v", found, err)
				}
				inventory, found, err := repos.Inventory.Load(ctx, id)
				if err != nil || !found {
					t.Fatalf("load inventory: found=%t err=%v", found, err)
				}
				if inventory.Slots["0:121"].Count != materials || inventory.Slots["0:65"].Count != goods || PurchaseCount(account, c, policy, now) != quota {
					t.Fatalf("character=%s materials=%d goods=%d quota=%d, want %d/%d/%d", id,
						inventory.Slots["0:121"].Count, inventory.Slots["0:65"].Count, PurchaseCount(account, c, policy, now), materials, goods, quota)
				}
				if quota == 0 {
					if _, exists := account.Metadata["npc_shop_purchase_600_count"]; exists {
						t.Fatal("failed purchase persisted quota")
					}
				} else if account.Metadata["npc_shop_purchase_600_day"] != "0" || account.Metadata["npc_shop_purchase_600_count"] != "1" {
					t.Fatalf("accumulate must persist account bucket 0/count 1: %+v", account.Metadata)
				}
			}
			if err := buy("77", before, 2, ""); !errors.Is(err, ErrPurchaseLimit) {
				t.Fatalf("bulk purchase exceeded quota: %v", err)
			}
			assertState("77", before, 6, 0, 0)
			for _, failure := range []string{"payment", "after_payment", "after_grant"} {
				want := projectionFailure
				if failure == "payment" {
					want = insufficientMaterials
				}
				if err := buy("77", before, 1, failure); !errors.Is(err, want) {
					t.Fatalf("%s: %v", failure, err)
				}
				assertState("77", before, 6, 0, 0)
			}
			if err := buy("77", before, 1, ""); err != nil {
				t.Fatal(err)
			}
			assertState("77", before, 4, 1, 1)
			// Reconstruct the owner to ensure counts come from persisted account data.
			owner, err = NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			for _, now := range []time.Time{before, before.Add(time.Minute), before.Add(24 * time.Hour), before.AddDate(0, 0, 7), before.AddDate(1, 0, 0)} {
				for _, id := range []string{"77", "78"} {
					if err := buy(id, now, 1, ""); !errors.Is(err, ErrPurchaseLimit) {
						t.Fatalf("shared permanent quota character=%s at=%s: %v", id, now, err)
					}
				}
				assertState("77", now, 4, 1, 1)
				assertState("78", now, 6, 0, 1)
			}
		})
	}
}
