package guild

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func teleportBenefitCatalog() ContentCatalog {
	c := benefitCatalog()
	for i, value := range []int32{10, 100, 1000} {
		id := uint32(30 + i)
		c.Contents[id] = Content{
			ID:     id,
			Type:   7,
			Value:  value,
			Days:   9999,
			Mode:   1,
			ItemID: []uint32{10153534, 10153535, 10153536}[i],
			Price:  []uint32{300, 2850, 27000}[i],
		}
	}
	return c
}

func TestGuildTeleportBenefitPurchaseAccumulatesAndReplayPreservesLatestCount(t *testing.T) {
	repos, q, now := memberExpansionFixture(t)
	ctx := context.Background()
	cat := teleportBenefitCatalog()
	var first BenefitPurchaseReceipt
	var firstRequestID string
	for i, want := range []uint32{10, 110, 1110} {
		q.ContentID = uint32(30 + i)
		q.RequestID = []string{
			"1000000000000000000000000000000000000000000000000000000000000000",
			"2000000000000000000000000000000000000000000000000000000000000000",
			"3000000000000000000000000000000000000000000000000000000000000000",
		}[i]
		got, err := PurchaseBenefit(ctx, repos.Guild, q, cat, now.Add(time.Duration(i)*time.Second))
		if err != nil || got.Benefit.Type != 7 || got.Benefit.Value != want || !got.Benefit.ExpiresAt.IsZero() || got.Benefit.Value == 0 {
			t.Fatalf("purchase %d=%+v %v", i, got, err)
		}
		if i == 0 {
			first = got
			firstRequestID = q.RequestID
		}
	}

	q.ContentID, q.RequestID = 30, firstRequestID
	replayed, err := PurchaseBenefit(ctx, repos.Guild, q, cat, now.Add(time.Hour))
	if err != nil || !equalBenefitPurchaseReceipt(replayed, first) {
		t.Fatalf("replay=%+v %v", replayed, err)
	}
	reg, err := Load(ctx, repos.Settings)
	if err != nil || reg.Guilds[1].Benefits[7].Value != 1110 || reg.Guilds[1].Mileage != 769850 || len(reg.BenefitPurchases) != 3 {
		t.Fatalf("wrong accumulated state: %+v %v", reg.Guilds[1].Benefits[7], err)
	}

	reg.Guilds[1].Benefits[7] = Benefit{Type: 7, Value: 7}
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	q.ContentID, q.RequestID = 30, firstRequestID
	if replayed, err = PurchaseBenefit(ctx, repos.Guild, q, cat, now.Add(2*time.Hour)); err != nil || !equalBenefitPurchaseReceipt(replayed, first) {
		t.Fatalf("latest-count replay=%+v %v", replayed, err)
	}
	reg, err = Load(ctx, repos.Settings)
	if err != nil || reg.Guilds[1].Benefits[7].Value != 7 || reg.Guilds[1].Mileage != 769850 {
		t.Fatal("replay overwrote the latest count or charged again", reg.Guilds[1].Benefits[7], reg.Guilds[1].Mileage, err)
	}

	reg.Guilds[1].Benefits[7] = Benefit{Type: 7, Value: 0}
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal("zero remaining count is not a valid durable state", err)
	}
	reg, err = Load(ctx, repos.Settings)
	if err != nil || reg.Guilds[1].Benefits[7].Value != 0 || reg.Guilds[1].Benefits[7].Active(now) {
		t.Fatal("zero remaining count was not preserved inactive", reg.Guilds[1].Benefits[7], err)
	}
	q.ContentID = 30
	q.RequestID = "4000000000000000000000000000000000000000000000000000000000000000"
	if got, err := PurchaseBenefit(ctx, repos.Guild, q, cat, now.Add(3*time.Hour)); err != nil || got.Benefit.Value != 10 {
		t.Fatal("purchase after depletion", got, err)
	}
}

func TestGuildTeleportBenefitActiveUsesPositiveCountAndZeroExpiry(t *testing.T) {
	now := time.Unix(1700000000, 0)
	for _, tc := range []struct {
		name    string
		benefit Benefit
		active  bool
	}{
		{name: "zero", benefit: Benefit{Type: 7}},
		{name: "positive", benefit: Benefit{Type: 7, Value: 1}, active: true},
		{name: "expiry_not_applicable", benefit: Benefit{Type: 7, Value: 1, ExpiresAt: now}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.benefit.Active(now); got != tc.active {
				t.Fatalf("Active=%v want=%v", got, tc.active)
			}
		})
	}
}

func TestGuildTeleportBenefitRejectsAndRollsBack(t *testing.T) {
	for _, mode := range []string{"nonmember", "permission", "balance", "type", "mode", "days", "value", "gold", "overflow", "save_failure", "late_failure"} {
		t.Run(mode, func(t *testing.T) {
			repos, q, now := memberExpansionFixture(t)
			ctx := context.Background()
			q.ContentID = 30
			cat := teleportBenefitCatalog()
			reg, err := Load(ctx, repos.Settings)
			if err != nil {
				t.Fatal(err)
			}
			uow := repos.Guild
			switch mode {
			case "nonmember":
				q.Owner = r.GuildOwner{CharacterID: "2", AccountID: "a2"}
			case "permission":
				reg.Guilds[1].GradePermissions = map[byte]uint32{1: CargoPermission}
			case "balance":
				reg.Guilds[1].Mileage = 299
			case "type":
				c := cat.Contents[30]
				c.Type = 8
				cat.Contents[30] = c
			case "mode":
				c := cat.Contents[30]
				c.Mode = 0
				cat.Contents[30] = c
			case "days":
				c := cat.Contents[30]
				c.Days = 1
				cat.Contents[30] = c
			case "value":
				c := cat.Contents[30]
				c.Value = 11
				cat.Contents[30] = c
			case "gold":
				c := cat.Contents[30]
				c.Gold = true
				cat.Contents[30] = c
			case "overflow":
				reg.Guilds[1].Benefits = map[byte]Benefit{7: {Type: 7, Value: math.MaxUint32}}
			case "save_failure":
				uow = cargoInitializationFailUOW{uow}
			case "late_failure":
				uow = lateFailGuildTx{base: uow, fail: errors.New("late failure")}
			}
			if err := Save(ctx, repos.Settings, reg, now); err != nil {
				t.Fatal(err)
			}
			before, _, err := repos.Settings.Load(ctx, RegistryScope)
			if err != nil {
				t.Fatal(err)
			}
			got, err := PurchaseBenefit(ctx, uow, q, cat, now)
			if err == nil || got.GuildID != 0 {
				t.Fatalf("invalid teleport purchase succeeded: %+v %v", got, err)
			}
			after, _, err := repos.Settings.Load(ctx, RegistryScope)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("failed teleport purchase persisted state", err)
			}
		})
	}
}

func TestGuildTeleportBenefitReceiptMustRemainPositive(t *testing.T) {
	repos, q, now := memberExpansionFixture(t)
	ctx := context.Background()
	q.ContentID = 30
	got, err := PurchaseBenefit(ctx, repos.Guild, q, teleportBenefitCatalog(), now)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	receipt := reg.BenefitPurchases[q.RequestID]
	receipt.Benefit.Value = 0
	reg.BenefitPurchases[q.RequestID] = receipt
	if err := validateBenefits(reg); err != ErrInvalidRegistry {
		t.Fatalf("zero-value purchase receipt accepted: %v", err)
	}
	if got.Benefit.Value != 10 {
		t.Fatal("purchase returned a non-positive receipt", got)
	}
}
