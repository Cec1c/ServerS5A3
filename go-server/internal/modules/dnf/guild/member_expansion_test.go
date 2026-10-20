package guild

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func memberExpansionCatalog() ContentCatalog {
	c := cargoPurchaseCatalog()
	for i, n := range []int32{500, 700, 1000} {
		id := uint32(16 + i)
		c.Contents[id] = Content{ID: id, Type: 6, Value: n, Days: -1, Price: []uint32{100000, 200000, 500000}[i]}
	}
	return c
}

func memberExpansionFixture(t *testing.T) (r.Group, CargoPurchase, time.Time) {
	t.Helper()
	repos, q, now := cargoPurchaseFixture(t)
	reg, err := Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Guilds[1].MemberLimit != 300 {
		t.Fatal("new guild did not start with 300 members")
	}
	reg.Guilds[1].Mileage = 800000
	if err := Save(context.Background(), repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	q.ContentID = 16
	return repos, q, now
}

func TestGuildMemberExpansionTiersAndHistoricalLimits(t *testing.T) {
	forEachPurchaseReceiptTimeZone(t, testGuildMemberExpansionTiersAndHistoricalLimits)
}

func testGuildMemberExpansionTiersAndHistoricalLimits(t *testing.T, loc *time.Location) {
	repos, q, now := memberExpansionFixture(t)
	now = now.In(loc)
	ctx := context.Background()
	for i, want := range []uint32{500, 700, 1000} {
		q.ContentID, q.RequestID = uint32(16+i), fmt.Sprintf("%064x", i+1)
		got, err := PurchaseMemberExpansion(ctx, repos.Guild, q, memberExpansionCatalog(), now)
		if err != nil || got.After != want {
			t.Fatalf("expansion=%+v %v", got, err)
		}
		replayed, err := PurchaseMemberExpansion(ctx, repos.Guild, q, memberExpansionCatalog(), now.Add(time.Hour))
		if err != nil || !equalCargoPurchaseReceipt(replayed, got) {
			t.Fatal("replay", replayed, err)
		}
	}
	reg, err := Load(ctx, repos.Settings)
	if err != nil || reg.Guilds[1].MemberLimit != 1000 || reg.Guilds[1].Mileage != 0 || len(reg.MemberExpansions) != 3 || reg.Guilds[1].Cargo != nil {
		t.Fatal("wrong member expansion state", err)
	}
	if history := reg.Guilds[1].MileageHistory; len(history) != 3 || history[2].Kind != MileageHistoryPurchase || history[2].ContentID != 18 || history[2].Amount != 500000 {
		t.Fatalf("expansion history/replay=%+v", history)
	}
	for _, limit := range []int{0, 500, 700, 1000, 1200} {
		reg.Guilds[1].MemberLimit = limit
		if err := Save(ctx, repos.Settings, reg, now); err != nil {
			t.Fatal(err)
		}
		read, err := Load(ctx, repos.Settings)
		want := limit
		if want == 0 {
			want = 1000
		}
		if err != nil || read.Guilds[1].MemberLimit != want {
			t.Fatal("historical guild shrank", limit, err)
		}
	}
}

func TestGuildMemberExpansionRejectsAndRollsBack(t *testing.T) {
	for _, mode := range []string{"skip_tier", "already", "insufficient", "no_permission", "wrong_owner", "save_failure", "late_failure", "after_save", "cargo_token", "unknown_commit"} {
		t.Run(mode, func(t *testing.T) {
			repos, q, now := memberExpansionFixture(t)
			ctx := context.Background()
			reg, _ := Load(ctx, repos.Settings)
			uow := repos.Guild
			want := error(nil)
			switch mode {
			case "skip_tier":
				q.ContentID = 17
				want = ErrMemberExpansionPrerequisite
			case "already":
				reg.Guilds[1].MemberLimit = 1000
				want = ErrMemberExpansionUsed
			case "insufficient":
				reg.Guilds[1].Mileage = 99999
				want = ErrInsufficientMileage
			case "no_permission":
				reg.Guilds[1].GradePermissions = map[byte]uint32{1: CargoPermission}
				want = ErrPermissionDenied
			case "wrong_owner":
				q.Owner.AccountID = "wrong"
				want = r.ErrGuildTransaction
			case "save_failure":
				uow = cargoInitializationFailUOW{uow}
			case "late_failure":
				uow = lateFailGuildTx{base: uow, fail: fmt.Errorf("late failure")}
			case "after_save":
				calls := 0
				q.Revalidate = func() bool { calls++; return calls < 3 }
				want = ErrCharacterNotFound
			case "unknown_commit":
				uow = purchaseCommitUnknown{uow}
				want = r.ErrGuildCommitUnknown
			}
			if err := Save(ctx, repos.Settings, reg, now); err != nil {
				t.Fatal(err)
			}
			if mode == "cargo_token" {
				cargo := q
				cargo.ContentID = 11
				if _, err := PurchaseCargo(ctx, repos.Guild, cargo, memberExpansionCatalog(), now); err != nil {
					t.Fatal(err)
				}
				want = ErrPurchaseIdentity
			}
			before, _, _ := repos.Settings.Load(ctx, RegistryScope)
			got, err := PurchaseMemberExpansion(ctx, uow, q, memberExpansionCatalog(), now)
			if err == nil || (want != nil && err != want) || got.GuildID != 0 {
				t.Fatalf("result=%+v %v want %v", got, err, want)
			}
			after, _, _ := repos.Settings.Load(ctx, RegistryScope)
			if mode == "unknown_commit" {
				got, err := PurchaseMemberExpansion(ctx, repos.Guild, q, memberExpansionCatalog(), now)
				if err != nil || got.After != 500 {
					t.Fatal("reconciliation", got, err)
				}
				reg, _ := Load(ctx, repos.Settings)
				if reg.Guilds[1].Mileage != 700000 {
					t.Fatal("reconciliation charged twice")
				}
			} else if !reflect.DeepEqual(before, after) {
				t.Fatal("failed purchase changed state")
			}
		})
	}
}

func TestGuildMemberExpansionConcurrentAndCrossTypeIdentity(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprint(same), func(t *testing.T) {
			repos, q, now := memberExpansionFixture(t)
			ctx := context.Background()
			start, results := make(chan struct{}), make(chan error, 8)
			for i := range 8 {
				go func(i int) {
					request := q
					if !same {
						request.RequestID = fmt.Sprintf("%064x", i+1)
					}
					<-start
					_, err := PurchaseMemberExpansion(ctx, repos.Guild, request, memberExpansionCatalog(), now)
					results <- err
				}(i)
			}
			close(start)
			passed := 0
			for range 8 {
				if err := <-results; err == nil {
					passed++
				} else if err != ErrMemberExpansionUsed {
					t.Fatal(err)
				}
			}
			if same && passed != 8 || !same && passed != 1 {
				t.Fatal("concurrent results", passed)
			}
			reg, _ := Load(ctx, repos.Settings)
			if reg.Guilds[1].Mileage != 700000 || len(reg.MemberExpansions) != 1 {
				t.Fatal("concurrent double charge")
			}
			for id := range reg.MemberExpansions {
				q.RequestID = id
			}
			q.ContentID = 11
			if _, err := PurchaseCargo(ctx, repos.Guild, q, memberExpansionCatalog(), now); err != ErrPurchaseIdentity {
				t.Fatal("cross type reuse", err)
			}
		})
	}
}

func TestGuildMemberExpansionRealMembershipCapacity(t *testing.T) {
	for _, path := range []string{"direct", "application", "invitation"} {
		t.Run(path, func(t *testing.T) {
			repos, q, now := memberExpansionFixture(t)
			ctx := context.Background()
			reg, _ := Load(ctx, repos.Settings)
			g := reg.Guilds[1]
			for i := 2; i <= 300; i++ {
				id := fmt.Sprint(i)
				g.Members[id] = Member{CharacterID: id, AccountID: "a" + id, Name: "成员" + id, Grade: 4}
			}
			target := r.CharacterRecord{CharacterID: "301", AccountID: "a301", Name: "新成员", Level: 90}
			if path == "application" {
				g.JoinMode = 0
				if err := reg.Apply(1, target, "申请", now); err != nil {
					t.Fatal(err)
				}
			}
			join := func(reg *Registry) error {
				switch path {
				case "application":
					_, err := reg.DecideApplication(1, "1", "301", true, now)
					return err
				case "invitation":
					_, err := reg.AcceptInvitation(1, q.Owner, target, now)
					return err
				default:
					_, err := reg.JoinDirect(1, target, now)
					return err
				}
			}
			if err := join(&reg); err != ErrMemberLimit {
				t.Fatal("300 boundary", err)
			}
			if err := Save(ctx, repos.Settings, reg, now); err != nil {
				t.Fatal(err)
			}
			if _, err := PurchaseMemberExpansion(ctx, repos.Guild, q, memberExpansionCatalog(), now); err != nil {
				t.Fatal(err)
			}
			reg, _ = Load(ctx, repos.Settings)
			if err := join(&reg); err != nil || len(reg.Guilds[1].Members) != 301 {
				t.Fatal("expanded capacity unused", err)
			}
		})
	}
}
