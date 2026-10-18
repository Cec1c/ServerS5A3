package guild

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func cargoInitializationFixture(t *testing.T) (r.Group, CargoQuery, CargoInitialization) {
	t.Helper()
	repos := guildTestGroup(t, 2)
	req := guildRequest(1, 0, "初始仓库", 73)
	req.Founder = r.GuildOwner{}
	if _, err := CreateSolo(context.Background(), repos.Guild, req, time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	return repos, CargoQuery{Owner: req.Creator, Revalidate: func() bool { return true }}, CargoInitialization{InitialCapacity: 8, MaxCapacity: 56, Now: time.Unix(1700000010, 0)}
}

func TestGuildCargoOpenInitializesOnceAndPreservesExpandedWarehouse(t *testing.T) {
	repos, query, policy := cargoInitializationFixture(t)
	ctx := context.Background()
	view, err := OpenCargo(ctx, repos.Guild, query, policy)
	if err != nil || view.Cargo == nil || view.Cargo.Capacity != 8 || len(view.Cargo.Slots) != 0 || len(view.Cargo.History) != 0 {
		t.Fatalf("first open: %+v %v", view, err)
	}
	for _, expanded := range []bool{false, true} {
		if expanded {
			reg, _ := Load(ctx, repos.Settings)
			g := reg.Guilds[view.GuildID]
			g.Cargo.Capacity = 24
			g.Cargo.Slots = map[uint16]r.ItemStack{23: {ItemID: 100, Count: 2, Extra: map[string]string{"serial": "keep"}}}
			g.Cargo.History = []CargoHistoryEntry{{At: policy.Now, CharacterID: "1", CharacterName: "角色1", Kind: CargoHistoryDeposit, Item: r.ItemStack{ItemID: 100, Count: 2}}}
			if err := Save(ctx, repos.Settings, reg, policy.Now); err != nil {
				t.Fatal(err)
			}
		}
		before, _, _ := repos.Settings.Load(ctx, r.GuildRegistryScope)
		policy.Now = policy.Now.Add(time.Hour)
		view, err = OpenCargo(ctx, repos.Guild, query, policy)
		if err != nil {
			t.Fatal(err)
		}
		if expanded && (view.Cargo.Capacity != 24 || view.Cargo.Slots[23].Count != 2 || len(view.Cargo.History) != 1) {
			t.Fatal("expanded warehouse lost")
		}
		view.Cargo.Capacity = 1
		after, _, _ := repos.Settings.Load(ctx, r.GuildRegistryScope)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("repeat open wrote or exposed mutable state")
		}
	}
}

type cargoInitializationFailSave struct{ r.SettingsRepository }

func (cargoInitializationFailSave) Save(context.Context, r.SettingsRecord) error {
	return errors.New("cargo save failure")
}

type cargoInitializationFailUOW struct{ r.GuildUnitOfWork }

func (u cargoInitializationFailUOW) WithinGuild(ctx context.Context, owners []r.GuildOwner, apply func(r.Group) error) error {
	return u.GuildUnitOfWork.WithinGuild(ctx, owners, func(tx r.Group) error {
		tx.Settings = cargoInitializationFailSave{tx.Settings}
		return apply(tx)
	})
}

func TestGuildCargoOpenRejectsWithoutMigrationAndRollsBack(t *testing.T) {
	for _, mode := range []string{"permission", "account", "member_account", "nonmember", "initial_zero", "small_limit", "overflow_limit", "zero_time", "before_save", "after_save", "save_failure", "commit_failure", "existing_over_limit"} {
		t.Run(mode, func(t *testing.T) {
			repos, query, policy := cargoInitializationFixture(t)
			ctx := context.Background()
			reg, _ := Load(ctx, repos.Settings)
			g, _, _ := reg.GuildForCharacter("1")
			uow := repos.Guild
			switch mode {
			case "permission":
				g.GradePermissions = map[byte]uint32{1: 0}
			case "account":
				query.Owner.AccountID = "different"
			case "member_account":
				m := g.Members["1"]
				m.AccountID = "different"
				g.Members["1"] = m
			case "nonmember":
				query.Owner = r.GuildOwner{CharacterID: "2", AccountID: "a2"}
			case "initial_zero":
				policy.InitialCapacity = 0
			case "small_limit":
				policy.MaxCapacity = 7
			case "overflow_limit":
				policy.MaxCapacity = 65537
			case "zero_time":
				policy.Now = time.Time{}
			case "before_save", "after_save":
				calls := 0
				query.Revalidate = func() bool { calls++; return calls < 2 || (mode == "after_save" && calls < 3) }
			case "save_failure":
				uow = cargoInitializationFailUOW{uow}
			case "commit_failure":
				uow = lateFailGuildTx{base: uow, fail: errors.New("late failure")}
			case "existing_over_limit":
				g.Cargo = &Cargo{Capacity: 57}
			}
			if err := Save(ctx, repos.Settings, reg, time.Unix(1700000001, 0)); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Settings.Load(ctx, r.GuildRegistryScope)
			got, err := OpenCargo(ctx, uow, query, policy)
			if err == nil || got.Cargo != nil {
				t.Fatalf("rejected open returned state: %+v %v", got, err)
			}
			after, _, _ := repos.Settings.Load(ctx, r.GuildRegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected initialization persisted state")
			}
		})
	}
}

func TestGuildCargoConcurrentOpenInitializesOnce(t *testing.T) {
	repos, query, policy := cargoInitializationFixture(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := OpenCargo(context.Background(), repos.Guild, query, policy)
			if err != nil || got.Cargo == nil || got.Cargo.Capacity != 8 {
				t.Errorf("concurrent open: %+v %v", got, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	reg, err := Load(context.Background(), repos.Settings)
	if err != nil || len(reg.Guilds) != 1 || reg.Guilds[1].Cargo == nil || reg.Guilds[1].Cargo.Capacity != 8 {
		t.Fatalf("final registry %+v %v", reg, err)
	}
}
