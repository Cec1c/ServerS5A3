package guild

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func contractCatalog() ContentCatalog {
	c := benefitCatalog()
	c.ContractRewardItemID, c.ContractRewardCount = 490001385, 1
	for i, n := range []int32{300, 500, 700, 1000} {
		id := uint32(23 + i)
		c.Contents[id] = Content{ID: id, Type: 21, Value: n, Days: 1, Price: uint32(n) * 10000, Gold: true}
	}
	return c
}

func contractPurchaseFixture(t *testing.T) (r.Group, CargoPurchase, time.Time) {
	t.Helper()
	repos, q, now := memberExpansionFixture(t)
	c, _, _ := repos.Character.Load(context.Background(), "1")
	c.Stats["gold"] = 100000000
	if err := repos.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	q.ContentID = 23
	return repos, q, now
}

func TestGuildContractPurchaseTiersRenewalAndThirtyDays(t *testing.T) {
	forEachPurchaseReceiptTimeZone(t, testGuildContractPurchaseTiersRenewalAndThirtyDays)
}

func testGuildContractPurchaseTiersRenewalAndThirtyDays(t *testing.T, loc *time.Location) {
	ctx := context.Background()
	for i, tier := range []int{300, 500, 700, 1000} {
		t.Run(fmt.Sprint(tier), func(t *testing.T) {
			repos, q, now := contractPurchaseFixture(t)
			now = now.In(loc)
			reg, _ := Load(ctx, repos.Settings)
			reg.Guilds[1].MemberLimit = tier
			if err := Save(ctx, repos.Settings, reg, now); err != nil {
				t.Fatal(err)
			}
			q.ContentID = uint32(23 + i)
			got, err := PurchaseBenefit(ctx, repos.Guild, q, contractCatalog(), now)
			if err != nil || !got.Gold || got.Benefit.Value != uint32(tier) || !got.Benefit.ExpiresAt.Equal(now.Add(24*time.Hour)) {
				t.Fatal(got, err)
			}
			reg, _ = Load(ctx, repos.Settings)
			actor, _, _ := repos.Character.Load(ctx, "1")
			if actor.Stats["gold"] != 100000000-int64(tier)*10000 || reg.Guilds[1].Mileage != 800000 || len(reg.Guilds[1].MileageHistory) != 0 {
				t.Fatal("contract charged the wrong currency")
			}
		})
	}
	repos, q, now := contractPurchaseFixture(t)
	now = now.In(loc)
	for i := 1; i <= 30; i++ {
		q.RequestID = fmt.Sprintf("%064x", i)
		got, err := PurchaseBenefit(ctx, repos.Guild, q, contractCatalog(), now)
		if err != nil || !got.Benefit.ExpiresAt.Equal(now.Add(time.Duration(i)*24*time.Hour)) {
			t.Fatal(i, got, err)
		}
		if replay, err := PurchaseBenefit(ctx, repos.Guild, q, contractCatalog(), now.Add(time.Hour)); err != nil || !equalBenefitPurchaseReceipt(replay, got) {
			t.Fatal("replay renewed", replay, err)
		}
	}
	q.RequestID = fmt.Sprintf("%064x", 31)
	if _, err := PurchaseBenefit(ctx, repos.Guild, q, contractCatalog(), now); err != ErrContractDuration {
		t.Fatal("31st day", err)
	}
	if got, err := PurchaseBenefit(ctx, repos.Guild, q, contractCatalog(), now.Add(24*time.Hour)); err != nil || !got.Benefit.ExpiresAt.Equal(now.Add(31*24*time.Hour)) {
		t.Fatal("remaining duration renewal", got, err)
	}
	q.RequestID = fmt.Sprintf("%064x", 32)
	if got, err := PurchaseBenefit(ctx, repos.Guild, q, contractCatalog(), now.Add(40*24*time.Hour)); err != nil || !got.Benefit.ExpiresAt.Equal(now.Add(41*24*time.Hour)) {
		t.Fatal("expired renewal", got, err)
	}
}

func TestGuildContractPurchaseFailuresAreAtomic(t *testing.T) {
	for _, mode := range []string{"gold", "tier", "permission", "definition", "currency", "save", "late", "stale", "foreign"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			repos, q, now := contractPurchaseFixture(t)
			cat, uow := contractCatalog(), repos.Guild
			reg, _ := Load(ctx, repos.Settings)
			actor, _, _ := repos.Character.Load(ctx, "1")
			switch mode {
			case "gold":
				actor.Stats["gold"] = 2999999
			case "tier":
				q.ContentID = 24
			case "permission":
				reg.Guilds[1].GradePermissions = map[byte]uint32{1: CargoPermission}
			case "definition":
				cat.ContractRewardCount = 2
			case "currency":
				c := cat.Contents[23]
				c.Gold = false
				cat.Contents[23] = c
			case "save":
				uow = cargoInitializationFailUOW{uow}
			case "late":
				uow = lateFailGuildTx{base: uow, fail: fmt.Errorf("after all saves")}
			case "stale":
				calls := 0
				q.Revalidate = func() bool { calls++; return calls < 3 }
			case "foreign":
				q.Owner.AccountID = "foreign"
			}
			if err := repos.Character.Save(ctx, actor); err != nil {
				t.Fatal(err)
			}
			if err := Save(ctx, repos.Settings, reg, now); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Settings.Load(ctx, RegistryScope)
			if got, err := PurchaseBenefit(ctx, uow, q, cat, now); err == nil || got.GuildID != 0 {
				t.Fatal("invalid purchase accepted", got, err)
			}
			after, _, _ := repos.Settings.Load(ctx, RegistryScope)
			current, _, _ := repos.Character.Load(ctx, "1")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(actor, current) {
				t.Fatal("failure changed balance or entitlement")
			}
		})
	}
}

func TestGuildContractPurchaseConcurrentAndUnknownCommit(t *testing.T) {
	repos, q, now := contractPurchaseFixture(t)
	ctx := context.Background()
	start, done := make(chan struct{}), make(chan error, 8)
	for range 8 {
		go func() { <-start; _, err := PurchaseBenefit(ctx, repos.Guild, q, contractCatalog(), now); done <- err }()
	}
	close(start)
	for range 8 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	q.RequestID = fmt.Sprintf("%064x", 200)
	if _, err := PurchaseBenefit(ctx, purchaseCommitUnknown{repos.Guild}, q, contractCatalog(), now); err != r.ErrGuildCommitUnknown {
		t.Fatal(err)
	}
	if _, err := PurchaseBenefit(ctx, repos.Guild, q, contractCatalog(), now); err != nil {
		t.Fatal(err)
	}
	reg, _ := Load(ctx, repos.Settings)
	actor, _, _ := repos.Character.Load(ctx, "1")
	if actor.Stats["gold"] != 94000000 || len(reg.BenefitPurchases) != 2 || !reg.Guilds[1].Benefits[21].ExpiresAt.Equal(now.Add(48*time.Hour)) {
		t.Fatal("duplicate charge or lost renewal")
	}
}
