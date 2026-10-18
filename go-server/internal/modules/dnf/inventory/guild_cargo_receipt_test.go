package inventory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildCargoReceiptConcurrentAndSlotReuse(t *testing.T) {
	repos, o, q, rules, now := cargoFixture(t)
	q.RequestID = fmt.Sprintf("%064x", 1)
	var wg sync.WaitGroup
	results := make(chan GuildCargoResult, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := o.TransferGuildCargo(context.Background(), q, rules, now)
			if err != nil {
				t.Error(err)
			}
			results <- got
		}()
	}
	wg.Wait()
	close(results)
	first := 0
	for got := range results {
		if !got.Replayed {
			first++
		}
	}
	g, bag := readCargo(t, repos, q.GuildID)
	if first != 1 || bag.Slots["0:87"].Count != 7 || g.Cargo.Slots[0].Count != 3 || len(g.Cargo.Transfers) != 1 || len(g.Cargo.History) != 1 {
		t.Fatal("duplicate receipt")
	}
	bag.Slots["0:87"] = r.ItemStack{ItemID: 999, Count: 1}
	repos.Inventory.Save(context.Background(), bag)
	owner, _ := NewOwner(repos)
	got, err := owner.TransferGuildCargo(context.Background(), q, rules, now.Add(time.Hour))
	if err != nil || !got.Replayed {
		t.Fatalf("replay %+v %v", got, err)
	}
	q.Count++
	if _, err = owner.TransferGuildCargo(context.Background(), q, rules, now); !errors.Is(err, ErrGuildCargoStale) {
		t.Fatalf("altered replay %v", err)
	}
}

func TestGuildCargoPlacementUsesLockedContainerCapacity(t *testing.T) {
	repos, o, q, rules, now := cargoFixture(t)
	if _, err := o.TransferGuildCargo(context.Background(), q, rules, now); err != nil {
		t.Fatal(err)
	}
	rules.Placement = func(r.CharacterRecord, r.ItemStack) (itemplacement.SlotPolicy, error) {
		return itemplacement.SlotPolicy{ListType: 0, Start: 65, End: 120}, nil
	}
	_, bag := readCargo(t, repos, q.GuildID)
	bag.Slots = map[string]r.ItemStack{}
	for slot := 65; slot <= 96; slot++ {
		bag.Slots[fmt.Sprintf("0:%d", slot)] = r.ItemStack{ItemID: 999, Count: 1}
	}
	repos.Inventory.Save(context.Background(), bag)
	withdrawal := GuildCargoWithdrawalRequest{Owner: q.Owner, GuildID: q.GuildID, CargoSlot: 0, ItemID: q.SourceItemID, Count: 1, DestinationList: 0, Revalidate: q.Revalidate, RequestID: fmt.Sprintf("%064x", 2)}
	if _, err := o.WithdrawGuildCargo(context.Background(), withdrawal, rules, now); !errors.Is(err, ErrGuildCargoInventoryFull) {
		t.Fatalf("closed slot allocated %v", err)
	}
	if err := repos.Settings.Save(context.Background(), r.SettingsRecord{Scope: r.CharacterContainerStateScope("1"), Values: map[string]string{"main_list_param16": "8", "avatar_list_param16": "0", "personal_cargo_list_param16": "8"}}); err != nil {
		t.Fatal(err)
	}
	got, err := o.WithdrawGuildCargo(context.Background(), withdrawal, rules, now)
	if err != nil || got.Destination.Slot != 97 {
		t.Fatalf("expanded capacity %+v %v", got, err)
	}
}
