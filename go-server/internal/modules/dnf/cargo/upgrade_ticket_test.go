package cargo

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func upgradeTicketFixture(t *testing.T, action uint32) (repo.Group, UpgradeTicketCommand) {
	t.Helper()
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	state := repo.SettingsRecord{Scope: repo.CharacterContainerStateScope("29"), Values: map[string]string{"main_list_param16": "0", "avatar_list_param16": "0", "personal_cargo_list_param16": "8"}}
	for _, e := range []error{g.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{"account_cargo_created": "true", "account_cargo_level": "8", "account_cera": "1000"}}), g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "29", AccountID: "a", Job: "0", Level: 90}), g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "29", Slots: map[string]repo.ItemStack{"0:87": {ItemID: 100, Count: 1}}}), g.Settings.Save(ctx, state)} {
		if e != nil {
			t.Fatal(e)
		}
	}
	c := UpgradeTicketCommand{AccountID: "a", CharacterID: "29", Slot: 87, Now: now, InitialSettings: state, Definition: UpgradeTicketDefinition{ItemID: 100, PVFPath: "stackable/a.stk", Action: action, MinimumLevel: 1, Jobs: []string{"[all]"}, AccountTiers: []int64{1, 8, 16, 24}}, Revalidate: func() bool { return true }, Expired: func(s repo.ItemStack, n time.Time) bool { return !s.ExpireAt.IsZero() && !s.ExpireAt.After(n) }, SetCount: func(s repo.ItemStack, n int64) (repo.ItemStack, error) { s.Count = n; return s, nil }}
	return g, c
}
func TestUpgradeTicketAtomicLastTicketConcurrency(t *testing.T) {
	for _, action := range []uint32{40, 45, 148} {
		t.Run(fmt.Sprint(action), func(t *testing.T) {
			g, c := upgradeTicketFixture(t, action)
			start := make(chan struct{})
			out := make(chan error, 2)
			var wg sync.WaitGroup
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); <-start; _, e := ApplyUpgradeTicket(context.Background(), g, c); out <- e }()
			}
			close(start)
			wg.Wait()
			close(out)
			success := 0
			for e := range out {
				if e == nil {
					success++
				}
			}
			if success != 1 {
				t.Fatalf("successes=%d", success)
			}
			a, _, _ := g.Account.Load(context.Background(), "a")
			if a.Metadata["account_cera"] != "1000" {
				t.Fatal("wallet mutated")
			}
			inv, _, _ := g.Inventory.Load(context.Background(), "29")
			if len(inv.Slots) != 0 {
				t.Fatal(inv)
			}
		})
	}
}
func TestUpgradeTicketRollbackAfterAllSaves(t *testing.T) {
	for _, action := range []uint32{40, 45, 148} {
		g, c := upgradeTicketFixture(t, action)
		ctx := context.Background()
		a, _, _ := g.Account.Load(ctx, "a")
		i, _, _ := g.Inventory.Load(ctx, "29")
		s, _, _ := g.Settings.Load(ctx, c.InitialSettings.Scope)
		checks := 0
		c.Revalidate = func() bool { checks++; return checks == 1 }
		_, e := ApplyUpgradeTicket(ctx, g, c)
		if !errors.Is(e, ErrUpgradeTicket) {
			t.Fatal(e)
		}
		aa, _, _ := g.Account.Load(ctx, "a")
		ii, _, _ := g.Inventory.Load(ctx, "29")
		ss, _, _ := g.Settings.Load(ctx, s.Scope)
		if !reflect.DeepEqual(a, aa) || !reflect.DeepEqual(i, ii) || !reflect.DeepEqual(s, ss) {
			t.Fatal("failed revalidation committed")
		}
	}
}
func TestUpgradeTicketRuntimeAccountTiersAndBounds(t *testing.T) {
	g, c := upgradeTicketFixture(t, 148)
	c.Definition.AccountTiers = []int64{1, 8, 24}
	r, e := ApplyUpgradeTicket(context.Background(), g, c)
	if e != nil || r.Before != 8 || r.After != 24 {
		t.Fatalf("PVF sequence not respected: %+v %v", r, e)
	}
	for _, tiers := range [][]int64{{}, {8, 16}, {1, 8, 8}, {1, 8, 9}, {1, 8, 248}, {1, 8}} {
		g, c := upgradeTicketFixture(t, 148)
		c.Definition.AccountTiers = tiers
		if _, e := ApplyUpgradeTicket(context.Background(), g, c); e == nil {
			t.Fatalf("accepted bad/max tiers %v", tiers)
		}
	}
	for _, slot := range []int16{-1, 300, 32767, 100} {
		g, c := upgradeTicketFixture(t, 40)
		c.Slot = slot
		if _, e := ApplyUpgradeTicket(context.Background(), g, c); e == nil {
			t.Fatalf("accepted slot %d", slot)
		}
	}
}
func TestUpgradeTicketSetCountFailureRollsBack(t *testing.T) {
	g, c := upgradeTicketFixture(t, 45)
	ctx := context.Background()
	i, _, _ := g.Inventory.Load(ctx, "29")
	st := i.Slots["0:87"]
	st.Count = 2
	i.Slots["0:87"] = st
	if e := g.Inventory.Save(ctx, i); e != nil {
		t.Fatal(e)
	}
	c.SetCount = func(repo.ItemStack, int64) (repo.ItemStack, error) {
		return repo.ItemStack{}, errors.New("raw row invalid")
	}
	if _, e := ApplyUpgradeTicket(ctx, g, c); e == nil {
		t.Fatal("accepted invalid raw")
	}
	after, _, _ := g.Inventory.Load(ctx, "29")
	if !reflect.DeepEqual(i, after) {
		t.Fatal("debit committed")
	}
}

func TestUpgradeTicketLastCapacityAndLegacyAccount(t *testing.T) {
	for _, action := range []uint32{40, 45, 148} {
		g, c := upgradeTicketFixture(t, action)
		ctx := context.Background()
		a, _, _ := g.Account.Load(ctx, "a")
		delete(a.Metadata, "account_cargo_created")
		a.Metadata["account_cargo_level"] = "16"
		if e := g.Account.Save(ctx, a); e != nil {
			t.Fatal(e)
		}
		s, _, _ := g.Settings.Load(ctx, c.InitialSettings.Scope)
		s.Values["main_list_param16"] = "16"
		s.Values["personal_cargo_list_param16"] = "248"
		if e := g.Settings.Save(ctx, s); e != nil {
			t.Fatal(e)
		}
		r, e := ApplyUpgradeTicket(ctx, g, c)
		want := uint16(24)
		if action == 45 {
			want = 264
		}
		if e != nil || r.After != want {
			t.Fatalf("last tier action=%d result=%+v err=%v", action, r, e)
		}
	}
}
