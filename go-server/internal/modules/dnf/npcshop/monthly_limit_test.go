package npcshop

import (
	"context"
	"errors"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestMonthlyPurchaseLimitCalendarBoundaryAndAccountScope(t *testing.T) {
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
	owner, _ := NewOwner(repos)
	policy := PurchaseLimit{ItemID: 490009969, Scope: "account", Period: "monthly", Maximum: 1, Count: 1}
	zone := time.FixedZone("UTC+8", 8*3600)
	buy := func(id string, now time.Time, fail bool) error {
		return owner.Mutate(ctx, Command{AccountID: "dnf:1", CharacterID: id, UpdatedAt: now, PurchaseLimit: &policy, Project: func(a *Assets) (Changes, error) {
			a.Character.Stats["gold"] -= 10
			if fail {
				return Changes{}, errors.New("grant failure")
			}
			return Changes{Character: true}, nil
		}})
	}
	start := time.Date(2026, 12, 31, 12, 0, 0, 0, zone)
	if err := buy("77", start, true); err == nil {
		t.Fatal("expected rollback")
	}
	if err := buy("77", start, false); err != nil {
		t.Fatal(err)
	}
	for _, when := range []time.Time{start.Add(time.Hour), time.Date(2027, 1, 1, 5, 59, 59, 0, zone)} {
		if err := buy("78", when, false); !errors.Is(err, ErrPurchaseLimit) {
			t.Fatalf("shared monthly limit bypassed: %v", err)
		}
	}
	reset := time.Date(2027, 1, 1, 6, 0, 0, 0, zone)
	if err := buy("78", reset, false); err != nil {
		t.Fatal(err)
	}
	if err := buy("77", reset.Add(24*time.Hour), false); !errors.Is(err, ErrPurchaseLimit) {
		t.Fatalf("daily boundary reset monthly quota: %v", err)
	}
	// Calendar months include February; elapsed 30-day windows are incorrect.
	for _, date := range []time.Time{time.Date(2027, 2, 1, 6, 0, 0, 0, zone), time.Date(2027, 3, 1, 6, 0, 0, 0, zone), time.Date(2028, 2, 29, 6, 0, 0, 0, zone), time.Date(2028, 3, 1, 6, 0, 0, 0, zone)} {
		if err := buy("77", date, false); err != nil {
			t.Fatalf("month=%s: %v", date, err)
		}
	}
}
