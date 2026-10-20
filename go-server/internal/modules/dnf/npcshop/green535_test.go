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

func beijing535(y int, m time.Month, d, hh, mm, ss int) time.Time {
	return time.Date(y, m, d, hh, mm, ss, 0, time.FixedZone("CST", 8*3600)).UTC()
}

func weekly535(count int64) PurchaseLimit {
	return PurchaseLimit{ItemID: 490010214, Scope: "account", Period: "weekly", Maximum: 70, Count: count}
}

func seed535Second(t *testing.T, ctx context.Context, repos dnfrepo.Group) {
	t.Helper()
	c, found, err := repos.Character.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("load 77: found=%v err=%v", found, err)
	}
	c.CharacterID = "78"
	if err := repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "78", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
}

func buy535(owner *Owner, ctx context.Context, id string, now time.Time, policy PurchaseLimit, fail bool) error {
	p := policy
	return owner.Mutate(ctx, Command{AccountID: "dnf:1", CharacterID: id, UpdatedAt: now, PurchaseLimit: &p,
		Project: func(a *Assets) (Changes, error) {
			if a.Character.Stats == nil {
				a.Character.Stats = map[string]int64{}
			}
			a.Character.Stats["gold"] -= p.Count
			if fail {
				return Changes{}, errors.New("grant failed after quota reservation")
			}
			return Changes{Character: true}, nil
		}})
}

func count535(t *testing.T, repos dnfrepo.Group, ctx context.Context, policy PurchaseLimit, now time.Time) int64 {
	t.Helper()
	account, _, _ := repos.Account.Load(ctx, "dnf:1")
	character, _, _ := repos.Character.Load(ctx, "77")
	return PurchaseCount(account, character, policy, now)
}

// The weekly window is the Chinese service week that opens Thursday 06:00.
func TestGreen535WeeklyBoundaryThursday0600Beijing(t *testing.T) {
	bucket := func(y int, m time.Month, d, hh, mm, ss int) int64 {
		return purchaseLimitBucket(weekly535(1), beijing535(y, m, d, hh, mm, ss))
	}
	if bucket(2026, time.October, 15, 5, 59, 59) == bucket(2026, time.October, 15, 6, 0, 0) {
		t.Fatal("Thursday 06:00 Beijing must open a new service week")
	}
	if bucket(2026, time.October, 14, 12, 0, 0) != bucket(2026, time.October, 15, 5, 0, 0) {
		t.Fatal("Wednesday and pre-06:00 Thursday must share one service week")
	}
	if bucket(2026, time.October, 15, 6, 0, 0) != bucket(2026, time.October, 21, 23, 0, 0) {
		t.Fatal("the whole Thursday-06:00 window must share one service week")
	}
	if bucket(2026, time.October, 15, 6, 0, 0) == bucket(2026, time.October, 22, 6, 0, 0) {
		t.Fatal("the next Thursday 06:00 Beijing must open the following week")
	}
	// Same instant must produce the same bucket in any timezone argument.
	if purchaseLimitBucket(weekly535(1), beijing535(2026, time.October, 16, 12, 0, 0)) !=
		purchaseLimitBucket(weekly535(1), beijing535(2026, time.October, 16, 12, 0, 0).In(time.FixedZone("PST", -8*3600))) {
		t.Fatal("bucket must not depend on the argument timezone")
	}
}

func TestGreen535WeeklyAccountQuotaSharedAcrossCharacters(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	seedNPCShopOwner(t, ctx, repos)
	seed535Second(t, ctx, repos)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	now := beijing535(2026, time.October, 16, 12, 0, 0) // Friday, inside the week
	p := weekly535(40)
	if err := buy535(owner, ctx, "77", now, p, false); err != nil {
		t.Fatal(err)
	}
	p = weekly535(30)
	if err := buy535(owner, ctx, "78", now, p, false); err != nil {
		t.Fatal(err)
	}
	if got := count535(t, repos, ctx, weekly535(1), now); got != 70 {
		t.Fatalf("shared account count=%d want 70", got)
	}
	if err := buy535(owner, ctx, "78", now, weekly535(1), false); !errors.Is(err, ErrPurchaseLimit) {
		t.Fatalf("account quota must be shared: %v", err)
	}
}

func TestGreen535WeeklyBulk69Plus1AllowedThenRejected(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	seedNPCShopOwner(t, ctx, repos)
	owner, _ := NewOwner(repos)
	now := beijing535(2026, time.October, 16, 12, 0, 0)
	if err := buy535(owner, ctx, "77", now, weekly535(69), false); err != nil {
		t.Fatal(err)
	}
	if err := buy535(owner, ctx, "77", now, weekly535(1), false); err != nil {
		t.Fatalf("69+1 must be allowed: %v", err)
	}
	if got := count535(t, repos, ctx, weekly535(1), now); got != 70 {
		t.Fatalf("count=%d want 70", got)
	}
	if err := buy535(owner, ctx, "77", now, weekly535(1), false); !errors.Is(err, ErrPurchaseLimit) {
		t.Fatalf("71 must be rejected: %v", err)
	}
	// A single bulk request above the maximum is rejected outright.
	repos2 := dnfrepomemory.NewMemoryGroup()
	seedNPCShopOwner(t, ctx, repos2)
	owner2, _ := NewOwner(repos2)
	if err := buy535(owner2, ctx, "77", now, weekly535(71), false); !errors.Is(err, ErrPurchaseLimit) {
		t.Fatalf("bulk 71 must be rejected: %v", err)
	}
	if got := count535(t, repos2, ctx, weekly535(1), now); got != 0 {
		t.Fatalf("rejected bulk changed the counter: %d", got)
	}
}

func TestGreen535WeeklyConcurrentSingleRemainder(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	seedNPCShopOwner(t, ctx, repos)
	owner, _ := NewOwner(repos)
	now := beijing535(2026, time.October, 16, 12, 0, 0)
	if err := buy535(owner, ctx, "77", now, weekly535(69), false); err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := buy535(owner, ctx, "77", now, weekly535(1), false)
			if err == nil {
				successes.Add(1)
				return
			}
			if !errors.Is(err, ErrPurchaseLimit) {
				t.Errorf("unexpected purchase error: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := successes.Load(); got != 1 {
		t.Fatalf("concurrent successes=%d want 1", got)
	}
	if got := count535(t, repos, ctx, weekly535(1), now); got != 70 {
		t.Fatalf("count=%d want 70", got)
	}
}

func TestGreen535WeeklyRollbackOnProjectFailure(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	seedNPCShopOwner(t, ctx, repos)
	owner, _ := NewOwner(repos)
	now := beijing535(2026, time.October, 16, 12, 0, 0)
	if err := buy535(owner, ctx, "77", now, weekly535(70), true); err == nil {
		t.Fatal("expected the failing projection to roll back")
	}
	if got := count535(t, repos, ctx, weekly535(1), now); got != 0 {
		t.Fatalf("failed purchase consumed quota: %d", got)
	}
	if err := buy535(owner, ctx, "77", now, weekly535(70), false); err != nil {
		t.Fatalf("a later full purchase must still work: %v", err)
	}
	if got := count535(t, repos, ctx, weekly535(1), now); got != 70 {
		t.Fatalf("count=%d want 70", got)
	}
}

func TestGreen535WeeklyNoResetInWeekResetAcrossBoundaryColdReadback(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	seedNPCShopOwner(t, ctx, repos)
	owner, _ := NewOwner(repos)
	start := beijing535(2026, time.October, 16, 12, 0, 0) // Friday
	if err := buy535(owner, ctx, "77", start, weekly535(70), false); err != nil {
		t.Fatal(err)
	}
	// Same service week, four days later, must not reset.
	if got := count535(t, repos, ctx, weekly535(1), beijing535(2026, time.October, 21, 23, 0, 0)); got != 70 {
		t.Fatalf("same-week count=%d want 70", got)
	}
	// A cold owner (fresh Owner over the same records) reads the same quota.
	cold, _ := NewOwner(repos)
	if err := buy535(cold, ctx, "77", beijing535(2026, time.October, 21, 23, 0, 0), weekly535(1), false); !errors.Is(err, ErrPurchaseLimit) {
		t.Fatalf("cold readback must see the same week quota: %v", err)
	}
	// The next Thursday 06:00 opens a new week and resets the quota.
	next := beijing535(2026, time.October, 22, 6, 0, 0)
	if got := count535(t, repos, ctx, weekly535(1), next); got != 0 {
		t.Fatalf("week-boundary count=%d want 0", got)
	}
	if err := buy535(owner, ctx, "77", next, weekly535(1), false); err != nil {
		t.Fatalf("purchase after the weekly reset: %v", err)
	}
}

func TestGreen535LegacyDailyKeepsMarkerSemantics(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	seedNPCShopOwner(t, ctx, repos)
	owner, _ := NewOwner(repos)
	now := beijing535(2026, time.October, 15, 12, 0, 0)
	// Legacy daily without reset: cumulative forever.
	persistent := PurchaseLimit{ItemID: 601, Scope: "account", Maximum: 100, Count: 5}
	if err := buy535(owner, ctx, "77", now, persistent, false); err != nil {
		t.Fatal(err)
	}
	for _, later := range []time.Time{now.Add(25 * time.Hour), now.AddDate(0, 0, 40)} {
		if got := count535(t, repos, ctx, persistent, later); got != 5 {
			t.Fatalf("legacy no-reset count=%d want 5 at %s", got, later)
		}
	}
	// Daily with reset: resets on the next 06:00 Beijing service day.
	resetting := PurchaseLimit{ItemID: 602, Scope: "account", Maximum: 10, ResetDaily: true, Count: 3}
	if err := buy535(owner, ctx, "77", now, resetting, false); err != nil {
		t.Fatal(err)
	}
	if got := count535(t, repos, ctx, resetting, now.Add(time.Hour)); got != 3 {
		t.Fatalf("same-day count=%d want 3", got)
	}
	if got := count535(t, repos, ctx, resetting, beijing535(2026, time.October, 16, 6, 0, 0)); got != 0 {
		t.Fatalf("daily reset count=%d want 0", got)
	}
}

func TestGreen535UnknownPeriodFailsClosedAndNilLimitUnchanged(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	seedNPCShopOwner(t, ctx, repos)
	owner, _ := NewOwner(repos)
	now := beijing535(2026, time.October, 16, 12, 0, 0)
	unknown := PurchaseLimit{ItemID: 603, Scope: "account", Period: "monthly", Maximum: 10, Count: 1}
	if err := buy535(owner, ctx, "77", now, unknown, false); !errors.Is(err, ErrPurchaseLimit) {
		t.Fatalf("unknown period must fail closed: %v", err)
	}
	if got := count535(t, repos, ctx, unknown, now); got != 0 {
		t.Fatalf("unknown period count=%d want 0", got)
	}
	// No limit at all: the purchase proceeds and writes no counter.
	err := owner.Mutate(ctx, Command{AccountID: "dnf:1", CharacterID: "77", UpdatedAt: now, PurchaseLimit: nil,
		Project: func(a *Assets) (Changes, error) { return Changes{Character: true}, nil }})
	if err != nil {
		t.Fatalf("nil limit must not block: %v", err)
	}
	if _, _, err := repos.Character.Load(ctx, "77"); err != nil {
		t.Fatal(err)
	}
}

// The explicit three-token form [purchase limit] <scope> daily <count> declares
// a daily window on its own and must reset at the 06:00 Beijing service day even
// when the optional reset marker is absent. Before the 535 revision the bucket
// ignored an explicit daily period without that marker and never reset.
func TestGreen535ExplicitDailyWithoutMarkerResetsAcrossServiceDay(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	seedNPCShopOwner(t, ctx, repos)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	before := beijing535(2026, time.October, 16, 5, 59, 0)
	after := beijing535(2026, time.October, 16, 6, 0, 0)
	policy := PurchaseLimit{ItemID: 605, Scope: "account", Period: "daily", Maximum: 10, Count: 10}
	if err := buy535(owner, ctx, "77", before, policy, false); err != nil {
		t.Fatal(err)
	}
	if got := count535(t, repos, ctx, policy, before); got != 10 {
		t.Fatalf("same-service-day count=%d want 10", got)
	}
	if got := count535(t, repos, ctx, policy, after); got != 0 {
		t.Fatalf("explicit daily without marker must reset at 06:00 Beijing: count=%d want 0", got)
	}
	if err := buy535(owner, ctx, "77", after, policy, false); err != nil {
		t.Fatalf("purchase after the explicit daily reset: %v", err)
	}
	if got := count535(t, repos, ctx, policy, after); got != 10 {
		t.Fatalf("post-reset count=%d want 10", got)
	}
}
