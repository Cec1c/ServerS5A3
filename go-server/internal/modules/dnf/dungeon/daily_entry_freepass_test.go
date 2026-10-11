package dungeon

import (
	"context"
	"errors"
	"sync"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestDailyFreePassConcurrentLimitAndExhaustion(t *testing.T) {
	repos, owner, cmd := dailyEntryFixture(t, 0)
	cmd.FreePasses = []EntryItemRequirement{{ItemID: 490005479, Count: 1, ConsumeOnEntry: true}}
	bag, _, _ := repos.Inventory.Load(context.Background(), "19")
	bag.Slots["0:21"] = dnfrepo.ItemStack{ItemID: 490005479, Count: 3}
	if err := repos.Inventory.Save(context.Background(), bag); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := owner.ConsumeDailyEntry(context.Background(), cmd); errs <- err }()
	}
	wg.Wait()
	close(errs)
	accepted := 0
	for err := range errs {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrDailyEntryLimit) {
			t.Fatal(err)
		}
	}
	c, _, _ := repos.Character.Load(context.Background(), "19")
	used, _ := DailyEntryUsed(c, 316, cmd.UpdatedAt)
	bag, _, _ = repos.Inventory.Load(context.Background(), "19")
	if accepted != 3 || used != 3 || bag.Slots["0:21"].Count != 0 {
		t.Fatalf("accepted%d used%d bag=%+v", accepted, used, bag)
	}
}

func TestDailyFreePassPriorityFixedCountAndNormalFallback(t *testing.T) {
	repos, owner, cmd := dailyEntryFixture(t, 30)
	cmd.FreePasses = []EntryItemRequirement{{ItemID: 490005479, Count: 1, ConsumeOnEntry: true}}
	bag, _, _ := repos.Inventory.Load(context.Background(), "19")
	bag.Slots["0:21"] = dnfrepo.ItemStack{ItemID: 490005479, Count: 2}
	if err := repos.Inventory.Save(context.Background(), bag); err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 3; i++ {
		result, err := owner.ConsumeDailyEntry(context.Background(), cmd)
		if err != nil || result.Used != i || len(result.Consumed) != 1 {
			t.Fatalf("entry %d: %+v %v", i, result, err)
		}
		item := result.Consumed[0]
		if i < 3 && (item.ItemID != 490005479 || item.Count != 1) {
			t.Fatalf("pass incorrectly scaled/bypassed: %+v", item)
		}
		if i == 3 && (item.ItemID != 10158964 || item.Count != 6) {
			t.Fatalf("normal fallback lost daily weight: %+v", item)
		}
	}
	bag, _, _ = repos.Inventory.Load(context.Background(), "19")
	if bag.Slots["0:20"].Count != 24 || bag.Slots["0:21"].Count != 0 {
		t.Fatalf("bag=%+v", bag)
	}
	if _, err := owner.ConsumeDailyEntry(context.Background(), cmd); !errors.Is(err, ErrDailyEntryLimit) {
		t.Fatalf("pass allowance bypassed: %v", err)
	}
}

func TestDailyFreePassProjectionFailureDoesNotFallbackOrMutate(t *testing.T) {
	repos, owner, cmd := dailyEntryFixture(t, 30)
	cmd.FreePasses = []EntryItemRequirement{{ItemID: 490005479, Count: 1, ConsumeOnEntry: true}}
	bag, _, _ := repos.Inventory.Load(context.Background(), "19")
	bag.Slots["0:21"] = dnfrepo.ItemStack{ItemID: 490005479, Count: 2}
	if err := repos.Inventory.Save(context.Background(), bag); err != nil {
		t.Fatal(err)
	}
	cmd.Project = func(int16, dnfrepo.ItemStack) (dnfrepo.ItemStack, error) {
		return dnfrepo.ItemStack{}, errors.New("projection failed")
	}
	if _, err := owner.ConsumeDailyEntry(context.Background(), cmd); err == nil {
		t.Fatal("projection accepted")
	}
	bag, _, _ = repos.Inventory.Load(context.Background(), "19")
	c, _, _ := repos.Character.Load(context.Background(), "19")
	used, _ := DailyEntryUsed(c, 316, cmd.UpdatedAt)
	if used != 0 || bag.Slots["0:21"].Count != 2 || bag.Slots["0:20"].Count != 30 {
		t.Fatalf("failure mutated assets/counter: %+v used%d", bag, used)
	}
}

func TestDailyFreePassCargoDoesNotCountAndMalformedDefinitionsFail(t *testing.T) {
	repos, owner, cmd := dailyEntryFixture(t, 0)
	cmd.FreePasses = []EntryItemRequirement{{ItemID: 490005479, Count: 1, ConsumeOnEntry: true}}
	bag, _, _ := repos.Inventory.Load(context.Background(), "19")
	bag.Slots["2:21"] = dnfrepo.ItemStack{ItemID: 490005479, Count: 3}
	if err := repos.Inventory.Save(context.Background(), bag); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ConsumeDailyEntry(context.Background(), cmd); !errors.Is(err, ErrEntryItemConsumeInsufficient) {
		t.Fatalf("cargo pass accepted: %v", err)
	}
	for _, pass := range []EntryItemRequirement{{ItemID: 0, Count: 1, ConsumeOnEntry: true}, {ItemID: 1, Count: 0, ConsumeOnEntry: true}, {ItemID: 1, Count: 1}} {
		cmd.FreePasses = []EntryItemRequirement{pass}
		if _, err := owner.ConsumeDailyEntry(context.Background(), cmd); !errors.Is(err, ErrEntryItemConsumeInvalid) {
			t.Fatalf("invalid pass accepted: %+v %v", pass, err)
		}
	}
}
