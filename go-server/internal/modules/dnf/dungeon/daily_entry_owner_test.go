package dungeon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func dailyEntryFixture(t *testing.T, tickets int64) (dnfrepo.Group, *Owner, DailyEntryCommand) {
	t.Helper()
	repos, owner := seedEntryItemInventory(t, map[string]dnfrepo.ItemStack{"0:20": {ItemID: 10158964, Count: tickets}})
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: "19", Stats: map[string]int64{"fatigue": 100}}); err != nil {
		t.Fatal(err)
	}
	return repos, owner, DailyEntryCommand{CharacterID: "19", DungeonID: 316, Limit: 3, RequiredItemWeight: 1, Requirements: []EntryItemRequirement{{ItemID: 10158964, Count: 2, ConsumeOnEntry: true}}, UpdatedAt: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
}

func TestDailyEntryStrictThreePersistsAcrossOwnerAndServiceDay(t *testing.T) {
	repos, owner, cmd := dailyEntryFixture(t, 30)
	for i := int64(1); i <= 3; i++ {
		result, err := owner.ConsumeDailyEntry(context.Background(), cmd)
		if err != nil || result.Used != i || len(result.Consumed) != 1 || result.Consumed[0].Count != 2*i {
			t.Fatalf("entry %d: %+v err=%v", i, result, err)
		}
		owner, _ = NewOwner(repos) // A new session/owner sees durable count.
	}
	if _, err := owner.ConsumeDailyEntry(context.Background(), cmd); !errors.Is(err, ErrDailyEntryLimit) {
		t.Fatalf("fourth entry: %v", err)
	}
	bag, _, _ := repos.Inventory.Load(context.Background(), "19")
	if bag.Slots["0:20"].Count != 18 {
		t.Fatal("fourth entry consumed assets")
	}
	cmd.UpdatedAt = time.Date(2026, 10, 5, 21, 59, 59, 0, time.UTC) // 05:59:59 UTC+8.
	if _, err := owner.ConsumeDailyEntry(context.Background(), cmd); !errors.Is(err, ErrDailyEntryLimit) {
		t.Fatal("allowance reset before 06:00")
	}
	cmd.UpdatedAt = cmd.UpdatedAt.Add(time.Second)
	result, err := owner.ConsumeDailyEntry(context.Background(), cmd)
	if err != nil || result.Used != 1 || result.Consumed[0].Count != 2 {
		t.Fatalf("next day: %+v %v", result, err)
	}
}

func TestDailyEntryInventoryFailureRollsBackCounterAndAllTickets(t *testing.T) {
	for _, projectionFails := range []bool{false, true} {
		repos, owner, cmd := dailyEntryFixture(t, 20)
		if projectionFails {
			cmd.Project = func(int16, dnfrepo.ItemStack) (dnfrepo.ItemStack, error) {
				return dnfrepo.ItemStack{}, errors.New("projection failed")
			}
		} else {
			cmd.Requirements = append(cmd.Requirements, EntryItemRequirement{ItemID: 999, Count: 1, ConsumeOnEntry: true})
		}
		if _, err := owner.ConsumeDailyEntry(context.Background(), cmd); err == nil {
			t.Fatal("expected transactional failure")
		}
		character, _, _ := repos.Character.Load(context.Background(), "19")
		used, _ := DailyEntryUsed(character, 316, cmd.UpdatedAt)
		bag, _, _ := repos.Inventory.Load(context.Background(), "19")
		if used != 0 || bag.Slots["0:20"].Count != 20 || character.Stats["fatigue"] != 100 {
			t.Fatal("failed entry mutated counter/assets")
		}
	}
}

func TestDailyEntryConcurrentRequestsCannotExceedThree(t *testing.T) {
	repos, owner, cmd := dailyEntryFixture(t, 100)
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := owner.ConsumeDailyEntry(context.Background(), cmd); results <- err }()
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrDailyEntryLimit) {
			t.Fatal(err)
		}
	}
	bag, _, _ := repos.Inventory.Load(context.Background(), "19")
	if accepted != 3 || bag.Slots["0:20"].Count != 88 {
		t.Fatalf("accepted=%d bag=%+v", accepted, bag)
	}
}
