package inventory

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/itemplacement"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildCargoMySQLAtomicTransferAndReplay(t *testing.T) {
	db, name, newGroup := itemTransactionMySQLFixture(t)
	ctx := context.Background()
	seed, _, q, rules, now := cargoFixture(t)
	repos := newGroup()
	q.RequestID = fmt.Sprintf("%064x", 1)
	reg, _ := guild.Load(ctx, seed.Settings)
	for _, g := range reg.Guilds {
		for _, m := range g.Members {
			actor, found, err := seed.Character.Load(ctx, m.CharacterID)
			if err != nil || !found {
				t.Fatal("seed actor")
			}
			for _, err = range []error{repos.Account.Save(ctx, r.AccountRecord{AccountID: m.AccountID}), repos.Character.Save(ctx, actor)} {
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	_, bag := readCargo(t, seed, q.GuildID)
	for _, err := range []error{repos.Inventory.Save(ctx, bag), guild.Save(ctx, repos.Settings, reg, now), repos.Settings.Save(ctx, r.SettingsRecord{Scope: r.CharacterContainerStateScope("1"), Values: map[string]string{"main_list_param16": "8", "avatar_list_param16": "0", "personal_cargo_list_param16": "8"}})} {
		if err != nil {
			t.Fatal(err)
		}
	}
	owner, _ := NewOwner(repos)
	before, _, _ := repos.Inventory.Load(ctx, "1")
	registry, _, _ := repos.Settings.Load(ctx, r.GuildRegistryScope)
	if _, err := db.Exec("CREATE TRIGGER `" + name + "`.reject_cargo BEFORE INSERT ON `" + name + "`.dnf_setting_values FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected cargo failure'"); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.TransferGuildCargo(ctx, q, rules, now); err == nil {
		t.Fatal("SQL save failure accepted")
	}
	after, _, _ := newGroup().Inventory.Load(ctx, "1")
	ra, _, _ := newGroup().Settings.Load(ctx, r.GuildRegistryScope)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(registry, ra) {
		t.Fatal("transfer/history/receipt did not roll back")
	}
	if _, err := db.Exec("DROP TRIGGER `" + name + "`.reject_cargo"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	done := make(chan error, 8)
	for range 8 {
		worker, _ := NewOwner(newGroup())
		go func() { <-start; _, err := worker.TransferGuildCargo(ctx, q, rules, now); done <- err }()
	}
	close(start)
	for range 8 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	g, inv := readCargo(t, newGroup(), q.GuildID)
	if inv.Slots["0:87"].Count != 7 || g.Cargo.Slots[0].Count != 3 || len(g.Cargo.History) != 1 || len(g.Cargo.Transfers) != 1 {
		t.Fatal("duplicate SQL transfer")
	}
	rules.Placement = func(r.CharacterRecord, r.ItemStack) (itemplacement.SlotPolicy, error) {
		return itemplacement.SlotPolicy{ListType: 0, Start: 97, End: 97}, nil
	}
	withdrawal := GuildCargoWithdrawalRequest{Owner: q.Owner, GuildID: q.GuildID, CargoSlot: 0, ItemID: q.SourceItemID, Count: 2, DestinationList: 0, Revalidate: q.Revalidate, RequestID: fmt.Sprintf("%064x", 2)}
	got, err := owner.WithdrawGuildCargo(ctx, withdrawal, rules, now)
	if err != nil || got.Destination.Slot != 97 {
		t.Fatalf("locked SQL capacity %+v %v", got, err)
	}
	fresh, _ := NewOwner(newGroup())
	got, err = fresh.WithdrawGuildCargo(ctx, withdrawal, rules, now)
	if err != nil || !got.Replayed {
		t.Fatalf("SQL replay %+v %v", got, err)
	}
	g, inv = readCargo(t, newGroup(), q.GuildID)
	if inv.Slots["0:97"].Count != 2 || g.Cargo.Slots[0].Count != 1 || len(g.Cargo.History) != 2 {
		t.Fatal("SQL withdrawal state")
	}
}
