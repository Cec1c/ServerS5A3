package onlineevent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	r "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestDailyCompletionPersistsDistinctQualifiedServiceDates(t *testing.T) {
	ctx := context.Background()
	repos := seededRepositories(t, ctx)
	d := testDefinition()
	d.QualificationStageID = "ten_seconds"
	if e := repos.Character.Save(ctx, r.CharacterRecord{CharacterID: "character-2", AccountID: "account-1", Stats: map[string]int64{}}); e != nil {
		t.Fatal(e)
	}
	if e := repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "character-2", Slots: map[string]r.ItemStack{}}); e != nil {
		t.Fatal(e)
	}
	for day := 1; day <= 5; day++ {
		owner := mustOwner(t, repos) // Rebuild the owner each day; only repository state survives.
		at := time.Date(2026, 10, day, 7, 0, 0, 0, d.calendar())
		id := fmt.Sprintf("character-%d", 1+day%2)
		if _, e := owner.Observe(ctx, ObserveCommand{AccountID: "account-1", CharacterID: id, Definition: d, IntervalFrom: at, IntervalTo: at.Add(10 * time.Second)}); e != nil {
			t.Fatal(e)
		}
		for n := 0; n < 2; n++ {
			if _, e := owner.Claim(ctx, ClaimCommand{AccountID: "account-1", CharacterID: id, Definition: d, StageID: "ten_seconds", ClaimedAt: at.Add(11 * time.Second), Allocate: testAllocator(nil)}); e != nil {
				t.Fatal(e)
			}
		}
	}
	a, _, e := repos.Account.Load(ctx, "account-1")
	if e != nil {
		t.Fatal(e)
	}
	var got struct {
		QualifiedDates []string `json:"qualified_dates"`
	}
	if e = json.Unmarshal([]byte(a.Metadata["online_event_cumulative_v1:"+d.ID]), &got); e != nil {
		t.Fatalf("missing cross-day qualified ledger: %v", e)
	}
	want := []string{"2026-10-01", "2026-10-02", "2026-10-03", "2026-10-04", "2026-10-05"}
	if !reflect.DeepEqual(got.QualifiedDates, want) {
		t.Fatalf("got=%v want=%v", got.QualifiedDates, want)
	}
	if a.Metadata["unrelated"] != "preserved" {
		t.Fatal("unrelated account metadata changed")
	}
}

func cumulativeFixture(t *testing.T) (r.Group, *Owner, CumulativeDefinition, time.Time) {
	t.Helper()
	ctx := context.Background()
	repos := seededRepositories(t, ctx)
	d := testDefinition()
	d.QualificationStageID = "ten_seconds"
	for _, id := range []string{"character-2"} {
		if e := repos.Character.Save(ctx, r.CharacterRecord{CharacterID: id, AccountID: "account-1", Stats: map[string]int64{}}); e != nil {
			t.Fatal(e)
		}
		if e := repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: id, Slots: map[string]r.ItemStack{}}); e != nil {
			t.Fatal(e)
		}
	}
	cd := CumulativeDefinition{Daily: d, Stages: []CumulativeStage{{ID: "sum-1", RequiredDays: 5, Items: []ItemReward{{ItemID: 3001, Count: 1}}}}}
	base := time.Date(2026, 10, 1, 7, 0, 0, 0, d.calendar())
	for n := 0; n < 5; n++ {
		owner := mustOwner(t, repos)
		at := base.AddDate(0, 0, n)
		if _, e := owner.Observe(ctx, ObserveCommand{AccountID: "account-1", CharacterID: "character-1", Definition: d, IntervalFrom: at, IntervalTo: at.Add(10 * time.Second)}); e != nil {
			t.Fatal(e)
		}
		if _, e := owner.Claim(ctx, ClaimCommand{AccountID: "account-1", CharacterID: "character-1", Definition: d, StageID: "ten_seconds", ClaimedAt: at.Add(11 * time.Second), Allocate: testAllocator(nil)}); e != nil {
			t.Fatal(e)
		}
	}
	return repos, mustOwner(t, repos), cd, base.AddDate(0, 0, 5)
}
func cumulativeAllocator(calls *int) ItemAllocator {
	return func(inv *r.InventoryRecord, reward ItemReward) (CommittedItem, error) {
		if calls != nil {
			*calls++
		}
		key := fmt.Sprintf("reward:%d", reward.ItemID)
		stack := inv.Slots[key]
		stack.ItemID = reward.ItemID
		stack.Count += reward.Count
		inv.Slots[key] = stack
		return CommittedItem{SlotKey: key, SlotIndex: 10, ItemID: reward.ItemID, Delta: reward.Count, PostCount: stack.Count}, nil
	}
}
func TestCumulativeClaimAccountReplayCrossCharacterRestartAndConcurrent(t *testing.T) {
	repos, owner, d, at := cumulativeFixture(t)
	ctx := context.Background()
	calls := 0
	command := CumulativeClaimCommand{AccountID: "account-1", CharacterID: "character-1", Definition: d, StageID: "sum-1", ClaimedAt: at, Allocate: cumulativeAllocator(&calls)}
	first, e := owner.ClaimCumulative(ctx, command)
	if e != nil || first.Replayed || len(first.Items) != 1 {
		t.Fatalf("first=%+v err=%v", first, e)
	}
	command.CharacterID = "character-2"
	owner = mustOwner(t, repos)
	second, e := owner.ClaimCumulative(ctx, command)
	if e != nil || !second.Replayed || second.CharacterID != "character-1" || calls != 1 {
		t.Fatalf("cross-character replay=%+v calls=%d err=%v", second, calls, e)
	}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, e := mustOwner(t, repos).ClaimCumulative(ctx, command)
			if e != nil || !result.Replayed {
				t.Errorf("concurrent replay=%+v err=%v", result, e)
			}
		}()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("allocator calls=%d", calls)
	}
	status, e := owner.CumulativeStatus(ctx, CumulativeStatusCommand{AccountID: "account-1", CharacterID: "character-2", Definition: d, ObservedAt: at.AddDate(0, 0, 9)})
	if e != nil || len(status.QualifiedDates) != 5 || !reflect.DeepEqual(status.ClaimedStageIDs, []string{"sum-1"}) {
		t.Fatalf("offline/restart=%+v err=%v", status, e)
	}
	inv, _, _ := repos.Inventory.Load(ctx, "character-2")
	if len(inv.Slots) != 0 {
		t.Fatal("replayed reward moved to second character")
	}
	command.AccountID = "other-account"
	if _, e = owner.ClaimCumulative(ctx, command); !errors.Is(e, ErrAccountMismatch) {
		t.Fatalf("wrong account=%v", e)
	}
}
func TestCumulativeClaimRollbackFullBagAndContractChange(t *testing.T) {
	repos, owner, d, at := cumulativeFixture(t)
	ctx := context.Background()
	command := CumulativeClaimCommand{AccountID: "account-1", CharacterID: "character-2", Definition: d, StageID: "sum-1", ClaimedAt: at, Allocate: cumulativeAllocator(nil)}
	failure := errors.New("account commit failed")
	failed := repos
	failed.CharacterSettlement = rejectAfterApplySettlement{inner: repos.CharacterSettlement, err: failure}
	if _, e := mustOwner(t, failed).ClaimCumulative(ctx, command); !errors.Is(e, failure) {
		t.Fatal(e)
	}
	inv, _, _ := repos.Inventory.Load(ctx, "character-2")
	if len(inv.Slots) != 0 {
		t.Fatal("rollback leaked item")
	}
	status, e := owner.CumulativeStatus(ctx, CumulativeStatusCommand{AccountID: "account-1", CharacterID: "character-2", Definition: d, ObservedAt: at})
	if e != nil || len(status.ClaimedStageIDs) != 0 {
		t.Fatalf("rollback receipt=%+v %v", status, e)
	}
	command.Allocate = func(*r.InventoryRecord, ItemReward) (CommittedItem, error) { return CommittedItem{}, failure }
	if _, e = owner.ClaimCumulative(ctx, command); !errors.Is(e, failure) {
		t.Fatal(e)
	}
	command.Allocate = cumulativeAllocator(nil)
	if _, e = owner.ClaimCumulative(ctx, command); e != nil {
		t.Fatal(e)
	}
	changed := d
	changed.Stages = append([]CumulativeStage(nil), d.Stages...)
	changed.Stages[0].Items = []ItemReward{{ItemID: 3002, Count: 1}}
	command.Definition = changed
	if _, e = owner.ClaimCumulative(ctx, command); !errors.Is(e, ErrStateInvalid) {
		t.Fatalf("changed reward reused=%v", e)
	}
	command.Definition = d
	command.ClaimedAt = at.Add(-time.Second)
	if _, e = owner.ClaimCumulative(ctx, command); !errors.Is(e, ErrObservationStale) {
		t.Fatalf("backdated claim=%v", e)
	}
}

func TestCumulativeFirstClaimConcurrentAcrossCharactersCommitsOnlyOnce(t *testing.T) {
	repos, _, d, at := cumulativeFixture(t)
	ctx := context.Background()
	calls := 0
	results := make(chan ClaimResult, 8)
	errorsFound := make(chan error, 8)
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			result, err := mustOwner(t, repos).ClaimCumulative(ctx, CumulativeClaimCommand{
				AccountID: "account-1", CharacterID: fmt.Sprintf("character-%d", 1+n%2),
				Definition: d, StageID: "sum-1", ClaimedAt: at, Allocate: cumulativeAllocator(&calls),
			})
			if err != nil {
				errorsFound <- err
				return
			}
			results <- result
		}(n)
	}
	wg.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	firsts := 0
	for result := range results {
		if !result.Replayed {
			firsts++
		}
	}
	if firsts != 1 || calls != 1 {
		t.Fatalf("first commits=%d allocations=%d", firsts, calls)
	}
	var count int64
	for _, id := range []string{"character-1", "character-2"} {
		inv, _, err := repos.Inventory.Load(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		count += inv.Slots["reward:3001"].Count
	}
	if count != 1 {
		t.Fatalf("both character inventories have %d cumulative rewards", count)
	}
}

func TestCumulativeStatusCanImportOnlyValidatedCurrentDayCompletionReceipt(t *testing.T) {
	ctx := context.Background()
	repos := seededRepositories(t, ctx)
	old := testDefinition()
	owner := mustOwner(t, repos)
	at := time.Date(2026, 10, 2, 7, 0, 0, 0, old.calendar())
	if _, err := owner.Observe(ctx, ObserveCommand{AccountID: "account-1", CharacterID: "character-1", Definition: old, IntervalFrom: at, IntervalTo: at.Add(10 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Claim(ctx, ClaimCommand{AccountID: "account-1", CharacterID: "character-1", Definition: old, StageID: "ten_seconds", ClaimedAt: at.Add(11 * time.Second), Allocate: testAllocator(nil)}); err != nil {
		t.Fatal(err)
	}
	newDefinition := old
	newDefinition.QualificationStageID = "ten_seconds"
	if _, err := owner.Status(ctx, StatusCommand{AccountID: "account-1", CharacterID: "character-1", Definition: newDefinition, ObservedAt: at.Add(12 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	d := CumulativeDefinition{Daily: newDefinition, Stages: []CumulativeStage{{ID: "sum-1", RequiredDays: 1, Items: []ItemReward{{ItemID: 3001, Count: 1}}}}}
	status, err := owner.CumulativeStatus(ctx, CumulativeStatusCommand{AccountID: "account-1", CharacterID: "character-1", Definition: d, ObservedAt: at.AddDate(0, 0, 1)})
	if err != nil || !reflect.DeepEqual(status.QualifiedDates, []string{"2026-10-02"}) {
		t.Fatalf("validated current completion=%+v err=%v", status, err)
	}
	// Resetting a later day preserves the one proved date; it never infers
	// other past dates from elapsed wall time or invents offline completions.
	if _, err = owner.Status(ctx, StatusCommand{AccountID: "account-1", CharacterID: "character-1", Definition: newDefinition, ObservedAt: at.AddDate(0, 0, 20)}); err != nil {
		t.Fatal(err)
	}
	status, err = owner.CumulativeStatus(ctx, CumulativeStatusCommand{AccountID: "account-1", CharacterID: "character-1", Definition: d, ObservedAt: at.AddDate(0, 0, 20)})
	if err != nil || len(status.QualifiedDates) != 1 {
		t.Fatalf("offline inferred dates=%+v err=%v", status, err)
	}
}
func TestCumulativeLockedPartialCompletionAndSixAMBoundary(t *testing.T) {
	ctx := context.Background()
	repos := seededRepositories(t, ctx)
	owner := mustOwner(t, repos)
	d := testDefinition()
	d.QualificationStageID = "ten_seconds"
	cd := CumulativeDefinition{Daily: d, Stages: []CumulativeStage{{ID: "sum-1", RequiredDays: 1, Items: []ItemReward{{ItemID: 3001, Count: 1}}}}}
	at := time.Date(2026, 10, 2, 5, 59, 40, 0, d.calendar())
	if _, e := owner.Observe(ctx, ObserveCommand{AccountID: "account-1", CharacterID: "character-1", Definition: d, IntervalFrom: at, IntervalTo: at.Add(9 * time.Second)}); e != nil {
		t.Fatal(e)
	}
	command := CumulativeClaimCommand{AccountID: "account-1", CharacterID: "character-1", Definition: cd, StageID: "sum-1", ClaimedAt: at.Add(10 * time.Second), Allocate: cumulativeAllocator(nil)}
	if _, e := owner.ClaimCumulative(ctx, command); !errors.Is(e, ErrStageLocked) {
		t.Fatal(e)
	}
	if _, e := owner.Observe(ctx, ObserveCommand{AccountID: "account-1", CharacterID: "character-1", Definition: d, IntervalFrom: at.Add(9 * time.Second), IntervalTo: at.Add(10 * time.Second)}); e != nil {
		t.Fatal(e)
	}
	if _, e := owner.Claim(ctx, ClaimCommand{AccountID: "account-1", CharacterID: "character-1", Definition: d, StageID: "ten_seconds", ClaimedAt: at.Add(11 * time.Second), Allocate: testAllocator(nil)}); e != nil {
		t.Fatal(e)
	}
	status, e := owner.CumulativeStatus(ctx, CumulativeStatusCommand{AccountID: "account-1", CharacterID: "character-1", Definition: cd, ObservedAt: at.Add(21 * time.Second)})
	if e != nil || !reflect.DeepEqual(status.QualifiedDates, []string{"2026-10-01"}) {
		t.Fatalf("six-AM=%+v %v", status, e)
	}
	if _, e = owner.Status(ctx, StatusCommand{AccountID: "account-1", CharacterID: "character-1", Definition: d, ObservedAt: at.Add(21 * time.Second)}); e != nil {
		t.Fatal(e)
	}
	command.ClaimedAt = at.Add(22 * time.Second)
	if _, e = mustOwner(t, repos).ClaimCumulative(ctx, command); e != nil {
		t.Fatal(e)
	}
}

func fourStageQualificationDefinition() Definition {
	d := testDefinition()
	d.Stages = nil
	for n, seconds := range []uint64{1800, 3600, 7200, 10800} {
		d.Stages = append(d.Stages, Stage{ID: fmt.Sprintf("stage-%d", n+1), RequiredSeconds: seconds, Items: []ItemReward{{ItemID: int64(2001 + n), Count: 1}}})
	}
	d.QualificationStageID = "stage-4"
	return d
}

func TestDailyQualificationRequiresAllReceiptsRegardlessOfClaimOrder(t *testing.T) {
	for _, importOld := range []bool{false, true} {
		t.Run(fmt.Sprintf("import_old_%t", importOld), func(t *testing.T) {
			ctx := context.Background()
			repos := seededRepositories(t, ctx)
			owner := mustOwner(t, repos)
			if err := repos.Character.Save(ctx, r.CharacterRecord{CharacterID: "character-2", AccountID: "account-1", Stats: map[string]int64{}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "character-2", Slots: map[string]r.ItemStack{}}); err != nil {
				t.Fatal(err)
			}
			d := fourStageQualificationDefinition()
			original := d
			if importOld {
				original.QualificationStageID = ""
			}
			at := time.Date(2026, 10, 9, 7, 0, 0, 0, d.calendar())
			if _, err := owner.Observe(ctx, ObserveCommand{AccountID: "account-1", CharacterID: "character-1", Definition: original, IntervalFrom: at, IntervalTo: at.Add(10800 * time.Second)}); err != nil {
				t.Fatal(err)
			}
			for n, id := range []string{"stage-4", "stage-1", "stage-3", "stage-2"} {
				characterID := fmt.Sprintf("character-%d", 1+n%2)
				if _, err := owner.Claim(ctx, ClaimCommand{AccountID: "account-1", CharacterID: characterID, Definition: original, StageID: id, ClaimedAt: at.Add(time.Duration(10801+n) * time.Second), Allocate: cumulativeAllocator(nil)}); err != nil {
					t.Fatal(err)
				}
				if importOld {
					if _, err := owner.Status(ctx, StatusCommand{AccountID: "account-1", CharacterID: characterID, Definition: d, ObservedAt: at.Add(time.Duration(10801+n) * time.Second)}); err != nil {
						t.Fatal(err)
					}
				}
				cd := CumulativeDefinition{Daily: d, Stages: []CumulativeStage{{ID: "sum-1", RequiredDays: 1, Items: []ItemReward{{ItemID: 3001, Count: 1}}}}}
				status, err := owner.CumulativeStatus(ctx, CumulativeStatusCommand{AccountID: "account-1", CharacterID: characterID, Definition: cd, ObservedAt: at.Add(time.Duration(10801+n) * time.Second)})
				if err != nil {
					t.Fatal(err)
				}
				want := 0
				if n == 3 {
					want = 1
				}
				if len(status.QualifiedDates) != want {
					t.Fatalf("after %s: qualified=%v want count=%d", id, status.QualifiedDates, want)
				}
			}
		})
	}
}

func TestDailyQualificationRejectsInvalidLegacyReceiptSet(t *testing.T) {
	for _, kind := range []string{"different-service-date", "wrong-reward", "insufficient-duration"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			repos := seededRepositories(t, ctx)
			owner := mustOwner(t, repos)
			d := fourStageQualificationDefinition()
			old := d
			old.QualificationStageID = ""
			at := time.Date(2026, 10, 9, 7, 0, 0, 0, d.calendar())
			if _, e := owner.Observe(ctx, ObserveCommand{AccountID: "account-1", CharacterID: "character-1", Definition: old, IntervalFrom: at, IntervalTo: at.Add(10800 * time.Second)}); e != nil {
				t.Fatal(e)
			}
			for _, stage := range old.Stages {
				if _, e := owner.Claim(ctx, ClaimCommand{AccountID: "account-1", CharacterID: "character-1", Definition: old, StageID: stage.ID, ClaimedAt: at.Add(10801 * time.Second), Allocate: cumulativeAllocator(nil)}); e != nil {
					t.Fatal(e)
				}
			}
			account, _, e := repos.Account.Load(ctx, "account-1")
			if e != nil {
				t.Fatal(e)
			}
			state, e := parseState(account.Metadata[metadataKey(d.ID)], old)
			if e != nil {
				t.Fatal(e)
			}
			receipt := state.Claims["stage-1"]
			switch kind {
			case "different-service-date":
				receipt.ClaimedAt = receipt.ClaimedAt.AddDate(0, 0, -1)
			case "wrong-reward":
				receipt.Items[0].ItemID++
			case "insufficient-duration":
				state.Intervals = []creditedInterval{{Start: 3600, End: 7200}}
				state.OnlineSeconds = 3600
			}
			state.Claims["stage-1"] = receipt
			raw, e := encodeState(state)
			if e != nil {
				t.Fatal(e)
			}
			account.Metadata[metadataKey(d.ID)] = raw
			if e := repos.Account.Save(ctx, account); e != nil {
				t.Fatal(e)
			}
			_, e = owner.Status(ctx, StatusCommand{AccountID: "account-1", CharacterID: "character-1", Definition: d, ObservedAt: at.Add(10802 * time.Second)})
			want := ErrStateInvalid
			if !errors.Is(e, want) {
				t.Fatalf("err=%v want=%v", e, want)
			}
			account, _, _ = repos.Account.Load(ctx, "account-1")
			if account.Metadata[cumulativeKeyPrefix+d.ID] != "" {
				t.Fatal("invalid receipt set qualified")
			}
		})
	}
}

func TestQualificationContractIncludesEveryDailyStage(t *testing.T) {
	d := fourStageQualificationDefinition()
	changed := d
	changed.Stages = append([]Stage(nil), d.Stages...)
	changed.Stages[0].RequiredSeconds++
	if qualificationHash(d) == qualificationHash(changed) {
		t.Fatal("earlier-stage duration omitted from qualification contract")
	}
	ledger := cumulativeLedger{Version: 1, EventID: d.ID, QualificationHash: qualificationHash(d), QualifiedDates: []string{"2026-10-09"}}
	raw, e := json.Marshal(ledger)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := parseCumulativeLedger(string(raw), changed); !errors.Is(e, ErrStateInvalid) {
		t.Fatalf("changed earlier-stage contract reused: %v", e)
	}
}
