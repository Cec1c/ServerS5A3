package quest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func antonPrerequisiteFixture(t *testing.T) (dnfrepo.Group, *Catalog, AntonPrerequisiteClear) {
	t.Helper()
	r := memory.NewMemoryGroup()
	ctx := context.Background()
	input := AntonPrerequisiteClear{CharacterID: "19", AccountID: "account-19", InstanceID: "112233445566778899aabbccddeeff00", CompletedAt: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC), ResetHour: 6}
	if err := r.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: input.AccountID}); err != nil {
		t.Fatal(err)
	}
	if err := r.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{12830: {Status: "active", ProgressValue: int64(packTriggerChannels(5, 5)), UpdatedAt: input.CompletedAt.Add(-time.Hour), Extra: map[string]string{"quest_type": "[raid phase clear]"}}}}); err != nil {
		t.Fatal(err)
	}
	c := &Catalog{byID: map[int64]Definition{12830: {Type: "[raid phase clear]", IntData: []int64{0, 0, 5, -1, 1, 0, 5, -1}}}}
	return r, c, input
}

func TestAntonPrerequisiteOriginalTwoPhasesRequireFiveDistinctServiceDays(t *testing.T) {
	r, c, input := antonPrerequisiteFixture(t)
	o, _ := NewOwner(r)
	ctx := context.Background()
	for day := 0; day < 5; day++ {
		for phase := byte(0); phase < 2; phase++ {
			input.Phase = phase
			input.InstanceID = fmt.Sprintf("%032x", day+100)
			got, err := o.ApplyAntonPrerequisitePhaseClear(ctx, c, 12830, input)
			if err != nil || !got.Changed {
				t.Fatal(day, phase, got, err)
			}
			// Reconnect/replay and another successful raid on this same day do
			// not decrement again. This simulates time only in memory fixtures.
			for _, instance := range []string{input.InstanceID, fmt.Sprintf("%032x", day+200)} {
				replay := input
				replay.InstanceID = instance
				again, err := o.ApplyAntonPrerequisitePhaseClear(ctx, c, 12830, replay)
				if err != nil || again.Changed || again.ProgressValue != got.ProgressValue {
					t.Fatal("same service day replay", again, err)
				}
			}
		}
		input.CompletedAt = input.CompletedAt.Add(24 * time.Hour)
	}
	record, _, _ := r.Quest.Load(ctx, "19")
	if state := record.States[12830]; state.Status != "active" || state.ProgressValue != 0 || state.Extra["reward_state"] != "" {
		t.Fatal("five-day counters must only enable original submission", state)
	}
	for _, phase := range []byte{0, 1} {
		input.Phase = phase
		input.InstanceID = fmt.Sprintf("%032x", 300+int(phase))
		got, err := o.ApplyAntonPrerequisitePhaseClear(ctx, c, 12830, input)
		if err != nil || got.Changed {
			t.Fatal("zero counter advanced", got, err)
		}
	}
}

func TestAntonPrerequisiteBoundaryAndInvalidEvidencePreserveRepository(t *testing.T) {
	r, c, input := antonPrerequisiteFixture(t)
	ctx := context.Background()
	o, _ := NewOwner(r)
	input.CompletedAt = time.Date(2026, 10, 8, 21, 59, 59, 0, time.UTC) // 05:59:59 UTC+8.
	if got, err := o.ApplyAntonPrerequisitePhaseClear(ctx, c, 12830, input); err != nil || !got.Changed {
		t.Fatal(got, err)
	}
	input.CompletedAt = input.CompletedAt.Add(time.Second)
	input.InstanceID = "2233445566778899aabbccddeeff0011"
	if got, err := o.ApplyAntonPrerequisitePhaseClear(ctx, c, 12830, input); err != nil || !got.Changed || got.ProgressValue&511 != 3 {
		t.Fatal("deployment service-day boundary not observed", got, err)
	}
	for _, name := range []string{"foreign-account", "foreign-character", "bad-instance", "bad-phase", "bad-hour", "backdated", "bad-definition"} {
		t.Run(name, func(t *testing.T) {
			before, _, _ := r.Quest.Load(ctx, "19")
			bad := input
			local := &Catalog{byID: map[int64]Definition{12830: c.byID[12830]}}
			switch name {
			case "foreign-account":
				bad.AccountID = "foreign"
			case "foreign-character":
				bad.CharacterID = "20"
			case "bad-instance":
				bad.InstanceID = "x"
			case "bad-phase":
				bad.Phase = 2
			case "bad-hour":
				bad.ResetHour = 24
			case "backdated":
				bad.CompletedAt = input.CompletedAt.Add(-24 * time.Hour)
			case "bad-definition":
				d := local.byID[12830]
				d.IntData = []int64{0, 0, 5, -1, 1, 1, 5, -1}
				local.byID[12830] = d
			}
			got, err := o.ApplyAntonPrerequisitePhaseClear(ctx, local, 12830, bad)
			if got.Changed || (err == nil && name != "backdated") {
				t.Fatal("bad evidence accepted", name, got, err)
			}
			after, _, _ := r.Quest.Load(ctx, "19")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("invalid event changed repository")
			}
		})
	}
}

type antonPrerequisiteFailCommit struct {
	base dnfrepo.CharacterSettlementUnitOfWork
}

func (r antonPrerequisiteFailCommit) WithinCharacterSettlement(ctx context.Context, id string, fn func(dnfrepo.Group) error) error {
	return r.base.WithinCharacterSettlement(ctx, id, func(tx dnfrepo.Group) error {
		if err := fn(tx); err != nil {
			return err
		}
		return errors.New("injected late failure")
	})
}

func TestAntonPrerequisiteLateRollbackRetryAndConcurrentDailyCount(t *testing.T) {
	r, c, input := antonPrerequisiteFixture(t)
	ctx := context.Background()
	before, _, _ := r.Quest.Load(ctx, "19")
	failed := r
	failed.CharacterSettlement = antonPrerequisiteFailCommit{r.CharacterSettlement}
	o, _ := NewOwner(failed)
	if got, err := o.ApplyAntonPrerequisitePhaseClear(ctx, c, 12830, input); err == nil || got.Changed {
		t.Fatal("failed commit escaped", got, err)
	}
	after, _, _ := r.Quest.Load(ctx, "19")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rollback lost atomicity")
	}
	o, _ = NewOwner(r)
	var wg sync.WaitGroup
	results := make(chan AntonPrerequisiteProgress, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			attempt := input
			attempt.InstanceID = fmt.Sprintf("%032x", i+1)
			got, err := o.ApplyAntonPrerequisitePhaseClear(ctx, c, 12830, attempt)
			if err != nil {
				t.Error(err)
			}
			results <- got
		}(i)
	}
	wg.Wait()
	close(results)
	var count int
	for got := range results {
		if got.Changed {
			count++
		}
	}
	if count != 1 {
		t.Fatal("concurrent same-day clears credited", count)
	}
}

func TestClientTriggerCannotAdvancePersistedRaidQuestMetadata(t *testing.T) {
	r, _, _ := antonPrerequisiteFixture(t)
	o, _ := NewOwner(r)
	ctx := context.Background()
	before, _, _ := r.Quest.Load(ctx, "19")
	for _, kind := range []byte{0, 16, 32, 64} {
		for _, increment := range []bool{true, false} {
			got, err := o.ApplySetTrigger(ctx, Command{Operation: "set_quest_trigger", SelectedCharacterID: 19, QuestID: 12830, TriggerType: kind, IsIncrement: increment})
			if err != nil || got.StateChanged {
				t.Fatal("client advanced raid objective", got, err)
			}
		}
	}
	after, _, _ := r.Quest.Load(ctx, "19")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("client op33 changed actual remaining counters")
	}
}
