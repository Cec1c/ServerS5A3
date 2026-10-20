package guild

import (
	"context"
	"fmt"
	r "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"reflect"
	"testing"
	"time"
)

func benefitCatalog() ContentCatalog {
	c := memberExpansionCatalog()
	c.Contents[1] = Content{ID: 1, Type: 5, Value: 169, Days: -1, Price: 36000}
	for i, days := range []int32{1, 7, 30} {
		id := uint32(2 + i*3)
		c.Contents[id] = Content{ID: id, Type: 3, Value: 167, Days: days, Price: uint32(days) * 200}
		c.Contents[id+1] = Content{ID: id + 1, Type: 1, Value: 10, Days: days, Price: uint32(days) * 200}
	}
	return c
}

func TestGuildBenefitPurchaseRenewalExpiryAndPermanent(t *testing.T) {
	forEachPurchaseReceiptTimeZone(t, testGuildBenefitPurchaseRenewalExpiryAndPermanent)
}

func testGuildBenefitPurchaseRenewalExpiryAndPermanent(t *testing.T, loc *time.Location) {
	repos, q, now := memberExpansionFixture(t)
	now = now.In(loc)
	ctx := context.Background()
	for _, kind := range []byte{1, 3} {
		firstID := uint32(3)
		if kind == 3 {
			firstID = 2
		}
		expires := now
		for i, days := range []int{1, 7, 30} {
			q.ContentID = firstID + uint32(i*3)
			q.RequestID = fmt.Sprintf("%064x", 100+int(kind)*10+i)
			got, err := PurchaseBenefit(ctx, repos.Guild, q, benefitCatalog(), now)
			expires = expires.Add(time.Duration(days) * 24 * time.Hour)
			if err != nil || !got.Benefit.ExpiresAt.Equal(expires) {
				t.Fatalf("renewal=%+v %v", got, err)
			}
			replayed, err := PurchaseBenefit(ctx, repos.Guild, q, benefitCatalog(), now.Add(time.Hour))
			if err != nil || !equalBenefitPurchaseReceipt(replayed, got) {
				t.Fatal("replay extended duration", err)
			}
		}
		q.RequestID = fmt.Sprintf("%064x", 200+int(kind))
		q.ContentID = firstID
		got, err := PurchaseBenefit(ctx, repos.Guild, q, benefitCatalog(), expires.Add(time.Hour))
		if err != nil || !got.Benefit.ExpiresAt.Equal(expires.Add(25*time.Hour)) {
			t.Fatal("expired benefit renewal", got, err)
		}
	}
	q.ContentID = 1
	q.RequestID = fmt.Sprintf("%064x", 300)
	got, err := PurchaseBenefit(ctx, repos.Guild, q, benefitCatalog(), now)
	if err != nil || !got.Benefit.ExpiresAt.IsZero() {
		t.Fatal("permanent purchase", got, err)
	}
	q.RequestID = fmt.Sprintf("%064x", 301)
	if _, err = PurchaseBenefit(ctx, repos.Guild, q, benefitCatalog(), now); err != ErrPermanentBenefitOwned {
		t.Fatal("permanent charged again", err)
	}
	reg, err := Load(ctx, repos.Settings)
	if err != nil || reg.Guilds[1].Mileage != 800000-2*39*200-36000 || len(reg.BenefitPurchases) != 9 {
		t.Fatal("wrong balance/receipts", err)
	}
}

func TestGuildBenefitPurchaseRejectedRollbackAndIdentity(t *testing.T) {
	for _, mode := range []string{"balance", "permission", "save_failure", "late_failure", "after_save", "invalid_type", "invalid_value", "gold", "overflow", "cargo_token", "member_token"} {
		t.Run(mode, func(t *testing.T) {
			repos, q, now := memberExpansionFixture(t)
			ctx := context.Background()
			q.ContentID = 3
			reg, _ := Load(ctx, repos.Settings)
			cat := benefitCatalog()
			uow := repos.Guild
			c := cat.Contents[3]
			switch mode {
			case "balance":
				reg.Guilds[1].Mileage = 199
			case "permission":
				reg.Guilds[1].GradePermissions = map[byte]uint32{1: CargoPermission}
			case "save_failure":
				uow = cargoInitializationFailUOW{uow}
			case "late_failure":
				uow = lateFailGuildTx{base: uow, fail: fmt.Errorf("late failure")}
			case "after_save":
				calls := 0
				q.Revalidate = func() bool { calls++; return calls < 3 }
			case "invalid_type":
				c.Type = 2
			case "invalid_value":
				c.Value = 167
			case "gold":
				c.Gold = true
			case "overflow":
				now = time.Unix(math.MaxInt32-60, 0)
			}
			cat.Contents[3] = c
			if err := Save(ctx, repos.Settings, reg, now); err != nil {
				t.Fatal(err)
			}
			if mode == "cargo_token" {
				other := q
				other.ContentID = 11
				if _, err := PurchaseCargo(ctx, repos.Guild, other, cat, now); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "member_token" {
				other := q
				other.ContentID = 16
				if _, err := PurchaseMemberExpansion(ctx, repos.Guild, other, cat, now); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := repos.Settings.Load(ctx, RegistryScope)
			got, err := PurchaseBenefit(ctx, uow, q, cat, now)
			if err == nil || got.GuildID != 0 {
				t.Fatal("invalid benefit purchase succeeded", got, err)
			}
			after, _, _ := repos.Settings.Load(ctx, RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed purchase persisted")
			}
		})
	}
}

func TestGuildBenefitPurchaseConcurrentDurationAndReverseTokenReuse(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprint(same), func(t *testing.T) {
			repos, q, now := memberExpansionFixture(t)
			ctx := context.Background()
			q.ContentID = 2
			start, results := make(chan struct{}), make(chan error, 8)
			for i := range 8 {
				go func(i int) {
					req := q
					if !same {
						req.RequestID = fmt.Sprintf("%064x", i+1)
					}
					<-start
					_, err := PurchaseBenefit(ctx, repos.Guild, req, benefitCatalog(), now)
					results <- err
				}(i)
			}
			close(start)
			for range 8 {
				if err := <-results; err != nil {
					t.Fatal(err)
				}
			}
			reg, _ := Load(ctx, repos.Settings)
			count := 8
			if same {
				count = 1
			}
			if reg.Guilds[1].Mileage != 800000-uint32(count)*200 || !reg.Guilds[1].Benefits[3].ExpiresAt.Equal(now.Add(time.Duration(count)*24*time.Hour)) || len(reg.BenefitPurchases) != count {
				t.Fatal("concurrent extension lost or double charged")
			}
			for id := range reg.BenefitPurchases {
				q.RequestID = id
				break
			}
			q.ContentID = 11
			if _, err := PurchaseCargo(ctx, repos.Guild, q, benefitCatalog(), now); err != ErrPurchaseIdentity {
				t.Fatal("benefit token reused for cargo", err)
			}
			q.ContentID = 16
			if _, err := PurchaseMemberExpansion(ctx, repos.Guild, q, benefitCatalog(), now); err != ErrPurchaseIdentity {
				t.Fatal("benefit token reused for members", err)
			}
		})
	}
}

func TestGuildBenefitMonsterExperienceExpiryRoundingAndOverflow(t *testing.T) {
	now := time.Unix(1700000000, 0)
	g := &Guild{Benefits: map[byte]Benefit{1: {Type: 1, Value: 10, ExpiresAt: now.Add(time.Second)}}}
	for _, v := range []struct{ base, want uint32 }{{0, 0}, {9, 0}, {10, 1}, {200, 20}, {math.MaxUint32, 0}, {math.MaxUint32 - 1, 1}} {
		if got := MonsterExperienceBonus(g, v.base, now); got != v.want {
			t.Fatal(v, got)
		}
	}
	if MonsterExperienceBonus(g, 200, now.Add(time.Second)) != 0 {
		t.Fatal("expired bonus applied")
	}
	if MonsterExperienceBonus(nil, 200, now) != 0 {
		t.Fatal("nonmember bonus applied")
	}
}

func TestGuildBenefitPurchasePermissionAndUnknownCommitRecovery(t *testing.T) {
	repos, q, now := memberExpansionFixture(t)
	ctx := context.Background()
	reg, _ := Load(ctx, repos.Settings)
	g := reg.Guilds[1]
	g.Members["2"] = Member{CharacterID: "2", AccountID: "a2", Name: "授权会员", Grade: 4}
	g.GradePermissions = map[byte]uint32{4: PurchasePermission}
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	q.Owner = r.GuildOwner{CharacterID: "2", AccountID: "a2"}
	q.ContentID = 3
	if got, err := PurchaseBenefit(ctx, purchaseCommitUnknown{repos.Guild}, q, benefitCatalog(), now); err != r.ErrGuildCommitUnknown || got.GuildID != 0 {
		t.Fatal("unknown commit reported success", got, err)
	}
	if got, err := PurchaseBenefit(ctx, repos.Guild, q, benefitCatalog(), now); err != nil || got.Benefit.Value != 10 {
		t.Fatal("failed reconciliation", got, err)
	}
	reg, _ = Load(ctx, repos.Settings)
	if reg.Guilds[1].Mileage != 799800 || len(reg.BenefitPurchases) != 1 {
		t.Fatal("reconciliation charged twice")
	}
	q.ContentID = 2
	if _, err := PurchaseBenefit(ctx, repos.Guild, q, benefitCatalog(), now); err != ErrPurchaseIdentity {
		t.Fatal("content token reuse", err)
	}
	q.ContentID = 3
	reg.Guilds[1].GradePermissions[4] = 0
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	if _, err := PurchaseBenefit(ctx, repos.Guild, q, benefitCatalog(), now); err != ErrPermissionDenied {
		t.Fatal("revoked permission reused receipt", err)
	}
}
