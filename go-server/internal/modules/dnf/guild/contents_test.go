package guild

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func cargoPurchaseCatalog() ContentCatalog {
	return ContentCatalog{CargoLimit: 56, Contents: map[uint32]Content{
		11: {ID: 11, Type: 4, Value: 16, Days: -1, Price: 10000},
		12: {ID: 12, Type: 4, Value: 8, Days: -1, Price: 5000},
	}}
}

func cargoPurchaseFixture(t *testing.T) (r.Group, CargoPurchase, time.Time) {
	t.Helper()
	repos, q, policy := cargoInitializationFixture(t)
	reg, err := Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	reg.Guilds[1].Mileage = 30000
	if err := Save(context.Background(), repos.Settings, reg, policy.Now); err != nil {
		t.Fatal(err)
	}
	return repos, CargoPurchase{Owner: q.Owner, ContentID: 11, RequestID: fmt.Sprintf("%064x", 1), InitialCapacity: 8, Revalidate: q.Revalidate}, policy.Now
}

func TestGuildCargoPurchaseActualExpansionAndDurableReplay(t *testing.T) {
	forEachPurchaseReceiptTimeZone(t, testGuildCargoPurchaseActualExpansionAndDurableReplay)
}

func testGuildCargoPurchaseActualExpansionAndDurableReplay(t *testing.T, loc *time.Location) {
	repos, req, now := cargoPurchaseFixture(t)
	now = now.In(loc)
	ctx := context.Background()
	first, err := PurchaseCargo(ctx, repos.Guild, req, cargoPurchaseCatalog(), now)
	if err != nil || first.Before != 8 || first.After != 24 || first.Price != 10000 {
		t.Fatalf("first=%+v %v", first, err)
	}
	reg, _ := Load(ctx, repos.Settings)
	g := reg.Guilds[1]
	g.Cargo.Slots = map[uint16]r.ItemStack{23: {ItemID: 100, Count: 2}}
	g.Cargo.History = []CargoHistoryEntry{{At: now, CharacterID: "1", CharacterName: "会员", Kind: CargoHistoryDeposit, Item: r.ItemStack{ItemID: 100, Count: 2}}}
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repos.Settings.Load(ctx, RegistryScope)
	replayed, err := PurchaseCargo(ctx, repos.Guild, req, cargoPurchaseCatalog(), now.Add(time.Hour))
	if err != nil || !equalCargoPurchaseReceipt(replayed, first) {
		t.Fatalf("replay=%+v %v", replayed, err)
	}
	after, _, _ := repos.Settings.Load(ctx, RegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("replay rewrote state")
	}
	for i, want := range []uint32{40, 56} {
		req.RequestID = fmt.Sprintf("%064x", i+2)
		result, err := PurchaseCargo(ctx, repos.Guild, req, cargoPurchaseCatalog(), now)
		if err != nil || result.After != want {
			t.Fatalf("second independent purchase=%+v %v", result, err)
		}
	}
	reg, _ = Load(ctx, repos.Settings)
	g = reg.Guilds[1]
	if g.Mileage != 0 || g.Cargo.Capacity != 56 || g.Cargo.Slots[23].Count != 2 || len(g.Cargo.History) != 1 || len(reg.CargoPurchases) != 3 {
		t.Fatal("charge/expansion corrupted existing state")
	}
	req.RequestID = fmt.Sprintf("%064x", 4)
	if _, err := PurchaseCargo(ctx, repos.Guild, req, cargoPurchaseCatalog(), now); err != ErrCargoLimitReached {
		t.Fatal("capacity overflow", err)
	}
	actor, _, _ := repos.Character.Load(ctx, "1")
	if actor.Stats["gold"] != 600000 {
		t.Fatal("guild purchase spent character gold")
	}
}

func TestGuildCargoPurchaseRejectedAndRollback(t *testing.T) {
	for _, mode := range []string{"no_permission", "wrong_account", "wrong_member_account", "nonmember", "insufficient", "oversize", "invalid_capacity", "invalid_policy", "unsupported", "wrong_currency", "bad_request_id", "save_failure", "late_failure", "before_save", "after_save"} {
		t.Run(mode, func(t *testing.T) {
			repos, req, now := cargoPurchaseFixture(t)
			ctx := context.Background()
			reg, _ := Load(ctx, repos.Settings)
			cat := cargoPurchaseCatalog()
			uow := repos.Guild
			switch mode {
			case "no_permission":
				reg.Guilds[1].GradePermissions = map[byte]uint32{1: CargoPermission}
			case "wrong_account":
				req.Owner.AccountID = "other"
			case "wrong_member_account":
				m := reg.Guilds[1].Members["1"]
				m.AccountID = "other"
				reg.Guilds[1].Members["1"] = m
			case "nonmember":
				req.Owner = r.GuildOwner{CharacterID: "2", AccountID: "a2"}
			case "insufficient":
				reg.Guilds[1].Mileage = 9999
			case "oversize":
				reg.Guilds[1].Cargo = &Cargo{Capacity: 48}
			case "invalid_capacity":
				reg.Guilds[1].Cargo = &Cargo{Capacity: 0}
			case "invalid_policy":
				req.InitialCapacity = 57
			case "unsupported":
				c := cat.Contents[11]
				c.Type = 3
				cat.Contents[11] = c
			case "wrong_currency":
				c := cat.Contents[11]
				c.Gold = true
				cat.Contents[11] = c
			case "bad_request_id":
				req.RequestID = ""
			case "save_failure":
				uow = cargoInitializationFailUOW{uow}
			case "late_failure":
				uow = lateFailGuildTx{base: uow, fail: fmt.Errorf("late failure")}
			case "before_save", "after_save":
				calls := 0
				req.Revalidate = func() bool { calls++; return calls < 2 || mode == "after_save" && calls < 3 }
			}
			if err := Save(ctx, repos.Settings, reg, now); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Settings.Load(ctx, RegistryScope)
			got, err := PurchaseCargo(ctx, uow, req, cat, now)
			if err == nil || got.GuildID != 0 {
				t.Fatalf("accepted: %+v %v", got, err)
			}
			after, _, _ := repos.Settings.Load(ctx, RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed purchase modified state")
			}
		})
	}
}

func TestGuildCargoPurchaseConcurrentReceiptsAndLastExpansion(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprint(same), func(t *testing.T) {
			repos, req, now := cargoPurchaseFixture(t)
			reg, _ := Load(context.Background(), repos.Settings)
			reg.Guilds[1].Cargo = &Cargo{Capacity: 48}
			if err := Save(context.Background(), repos.Settings, reg, now); err != nil {
				t.Fatal(err)
			}
			req.ContentID = 12
			var wg sync.WaitGroup
			results := make(chan error, 8)
			for i := range 8 {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					q := req
					if !same {
						q.RequestID = fmt.Sprintf("%064x", i+1)
					}
					_, e := PurchaseCargo(context.Background(), repos.Guild, q, cargoPurchaseCatalog(), now)
					results <- e
				}(i)
			}
			wg.Wait()
			close(results)
			passed := 0
			for e := range results {
				if e == nil {
					passed++
				} else if e != ErrCargoLimitReached {
					t.Fatal(e)
				}
			}
			if same && passed != 8 || !same && passed != 1 {
				t.Fatal("wrong concurrent results", passed)
			}
			reg, _ = Load(context.Background(), repos.Settings)
			if reg.Guilds[1].Cargo.Capacity != 56 || reg.Guilds[1].Mileage != 25000 || len(reg.CargoPurchases) != 1 {
				t.Fatal("last expansion charged more than once")
			}
		})
	}
}

func TestGuildCargoPurchaseUsesBit8AndRejectsTokenReuse(t *testing.T) {
	repos, req, now := cargoPurchaseFixture(t)
	ctx := context.Background()
	reg, _ := Load(ctx, repos.Settings)
	g := reg.Guilds[1]
	g.Members["2"] = Member{CharacterID: "2", AccountID: "a2", Name: "成员", Grade: 4}
	g.GradePermissions = map[byte]uint32{4: PurchasePermission}
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	req.Owner = r.GuildOwner{CharacterID: "2", AccountID: "a2"}
	if _, err := PurchaseCargo(ctx, repos.Guild, req, cargoPurchaseCatalog(), now); err != nil {
		t.Fatal("bit8 member denied", err)
	}
	req.ContentID = 12
	if _, err := PurchaseCargo(ctx, repos.Guild, req, cargoPurchaseCatalog(), now); err != ErrPurchaseIdentity {
		t.Fatal("token reused", err)
	}
}

type purchaseCommitUnknown struct{ r.GuildUnitOfWork }

func (u purchaseCommitUnknown) WithinGuild(ctx context.Context, owners []r.GuildOwner, apply func(r.Group) error) error {
	if err := u.GuildUnitOfWork.WithinGuild(ctx, owners, apply); err != nil {
		return err
	}
	return r.ErrGuildCommitUnknown
}

func TestGuildCargoPurchaseReconcilesUnknownCommit(t *testing.T) {
	repos, req, now := cargoPurchaseFixture(t)
	ctx := context.Background()
	got, err := PurchaseCargo(ctx, purchaseCommitUnknown{repos.Guild}, req, cargoPurchaseCatalog(), now)
	if err != r.ErrGuildCommitUnknown || got.GuildID != 0 {
		t.Fatal("unknown commit reported success", got, err)
	}
	got, err = PurchaseCargo(ctx, repos.Guild, req, cargoPurchaseCatalog(), now)
	if err != nil || got.After != 24 {
		t.Fatal("could not reconcile receipt", got, err)
	}
	reg, err := Load(ctx, repos.Settings)
	if err != nil || reg.Guilds[1].Mileage != 20000 || reg.Guilds[1].Cargo.Capacity != 24 || len(reg.CargoPurchases) != 1 {
		t.Fatal("unknown commit retry charged twice", err)
	}
}
