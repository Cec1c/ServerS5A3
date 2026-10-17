package dungeon

import (
	"context"
	"errors"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestCycleEntrySeparateLedgerNonceAndSharedLimit(t *testing.T) {
	ctx := context.Background()
	repos, owner, cmd := dailyEntryFixture(t, 100)
	cmd.DungeonID = 5097
	cmd.CycleEntry = true
	cmd.RequiredItemWeight = 0
	cmd.ClearNonceHi = 91
	cmd.ClearNonceLo = 92
	rule := CycleEntryAllowance{DungeonIDs: []int64{5097, 3570}, Limit: 3}
	cmd.InTransaction = func(c *dnfrepo.CharacterRecord) error {
		if err := ValidateCycleEntryAllowance(*c, rule, cmd.UpdatedAt); err != nil {
			return err
		}
		c.Stats["fatigue"]--
		return nil
	}
	for i := int64(1); i <= 3; i++ {
		cmd.DungeonID = 5097
		if i == 2 {
			cmd.DungeonID = 3570
		}
		cmd.ClearNonceHi = i
		cmd.ClearNonceLo = 100 + i
		result, err := owner.ConsumeDailyEntry(ctx, cmd)
		if err != nil {
			t.Fatal(err)
		}
		cleared, _ := CycleCountSum(result.Character, rule.DungeonIDs, cmd.UpdatedAt, false)
		used, _ := CycleCountSum(result.Character, rule.DungeonIDs, cmd.UpdatedAt, true)
		if used != i || cleared != 0 || result.Character.Stats["fatigue"] != 100-i {
			t.Fatalf("entry%d clear%d used%d character=%+v", i, cleared, used, result.Character)
		}
		bagBefore, _, _ := repos.Inventory.Load(ctx, "19")
		replay, err := owner.ConsumeDailyEntry(ctx, cmd)
		if err != nil || replay.Used != result.Used {
			t.Fatalf("nonce replay=%+v err=%v", replay, err)
		}
		bagAfter, _, _ := repos.Inventory.Load(ctx, "19")
		if !reflect.DeepEqual(bagBefore.Slots, bagAfter.Slots) {
			t.Fatal("replay charged ticket")
		}
		owner, _ = NewOwner(repos)
	}
	cmd.ClearNonceHi = 4
	if _, err := owner.ConsumeDailyEntry(ctx, cmd); !errors.Is(err, ErrDailyEntryLimit) {
		t.Fatalf("fourth shared entry err=%v", err)
	}
	wallet, _, _ := repos.Character.Load(ctx, "19")
	bag, _, _ := repos.Inventory.Load(ctx, "19")
	if wallet.Stats["fatigue"] != 97 || bag.Slots["0:20"].Count != 94 {
		t.Fatal("failed entry consumed assets")
	}
	// Three entries alone do not unlock trial; successful-clear progress is separate.
	trial := CycleEntryAllowance{DungeonIDs: []int64{5098, 3571}, Limit: 1, PrerequisiteDungeonIDs: rule.DungeonIDs, PrerequisiteCount: 3}
	if err := ValidateCycleEntryAllowance(wallet, trial, cmd.UpdatedAt); !errors.Is(err, ErrEntryPrerequisiteMissing) {
		t.Fatalf("entries unlocked trial: %v", err)
	}
}

func TestCycleEntryLegacyKnownClearsAndDayReset(t *testing.T) {
	ctx := context.Background()
	repos, owner, cmd := dailyEntryFixture(t, 20)
	cmd.DungeonID = 5097
	cmd.RequiredItemWeight = 0
	for i := 0; i < 2; i++ {
		if _, err := owner.ConsumeDailyEntry(ctx, cmd); err != nil {
			t.Fatal(err)
		}
	}
	before, _, _ := repos.Character.Load(ctx, "19")
	known, err := CycleEntryUsed(before, 5097, cmd.UpdatedAt)
	if err != nil || known != 2 {
		t.Fatalf("known clears=%d err=%v", known, err)
	}
	cmd.CycleEntry = true
	result, err := owner.ConsumeDailyEntry(ctx, cmd)
	if err != nil || result.Used != 3 {
		t.Fatalf("legacy minimum=%+v err=%v", result, err)
	}
	clear, _ := DailyEntryUsed(result.Character, 5097, cmd.UpdatedAt)
	if clear != 2 {
		t.Fatal("entry changed clear history")
	}
	cmd.UpdatedAt = time.Date(2026, 10, 5, 22, 0, 0, 0, time.UTC)
	result, err = owner.ConsumeDailyEntry(ctx, cmd)
	if err != nil || result.Used != 1 {
		t.Fatalf("new service day=%+v err=%v", result, err)
	}
}

func TestCycleEntrySharedConcurrentBudgetAndRollback(t *testing.T) {
	ctx := context.Background()
	repos, owner, base := dailyEntryFixture(t, 100)
	base.CycleEntry = true
	base.RequiredItemWeight = 0
	rule := CycleEntryAllowance{DungeonIDs: []int64{5097, 3570}, Limit: 3}
	base.InTransaction = func(c *dnfrepo.CharacterRecord) error { return ValidateCycleEntryAllowance(*c, rule, base.UpdatedAt) }
	var wg sync.WaitGroup
	ch := make(chan error, 12)
	for i := 0; i < 12; i++ {
		cmd := base
		cmd.DungeonID = 5097
		if i%2 != 0 {
			cmd.DungeonID = 3570
		}
		wg.Add(1)
		go func() { defer wg.Done(); _, err := owner.ConsumeDailyEntry(ctx, cmd); ch <- err }()
	}
	wg.Wait()
	close(ch)
	success := 0
	for err := range ch {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrDailyEntryLimit) {
			t.Fatal(err)
		}
	}
	char, _, _ := repos.Character.Load(ctx, "19")
	used, err := CycleCountSum(char, rule.DungeonIDs, base.UpdatedAt, true)
	if success != 3 || used != 3 || err != nil {
		t.Fatalf("success%d used%d err%v", success, used, err)
	}
	// Ticket projection fails before either wallet/counter persistence.
	repos, owner, base = dailyEntryFixture(t, 20)
	base.DungeonID = 5097
	base.CycleEntry = true
	base.RequiredItemWeight = 0
	base.Project = func(int16, dnfrepo.ItemStack) (dnfrepo.ItemStack, error) {
		return dnfrepo.ItemStack{}, errors.New("projection failure")
	}
	if _, err := owner.ConsumeDailyEntry(ctx, base); err == nil {
		t.Fatal("missing transaction error")
	}
	char, _, _ = repos.Character.Load(ctx, "19")
	used, _ = CycleEntryUsed(char, 5097, base.UpdatedAt)
	bag, _, _ := repos.Inventory.Load(ctx, "19")
	if used != 0 || char.Stats["fatigue"] != 100 || bag.Slots["0:20"].Count != 20 {
		t.Fatal("failed projection committed cycle entry")
	}
}
