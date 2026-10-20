package raid

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func weeklyFixture(t *testing.T) (repo.Group, WeeklyClearCommand) {
	t.Helper()
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	for _, row := range []ClearMember{{"1", "a"}, {"2", "b"}, {"3", "a"}} {
		if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: row.CharacterID, AccountID: row.AccountID, Stats: map[string]int64{"unrelated": 77}}); err != nil {
			t.Fatal(err)
		}
		if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: row.CharacterID}); err != nil {
			t.Fatal(err)
		}
	}
	return g, WeeklyClearCommand{Mode: 1, Limit: 2, InstanceID: "00112233445566778899aabbccddeeff", Members: []ClearMember{{"1", "a"}, {"2", "b"}}, CompletedAt: time.Date(2026, 10, 7, 2, 0, 0, 0, time.UTC), Policy: WeeklyPolicy{ResetWeekday: time.Thursday, ResetHour: 6}}
}

func TestWeeklyClearGroupRestartReplayAndModeCharacterIsolation(t *testing.T) {
	g, cmd := weeklyFixture(t)
	ctx := context.Background()
	result, err := CommitWeeklyClear(ctx, g, cmd)
	if err != nil || result.Used["1"] != 1 || result.Used["2"] != 1 {
		t.Fatal(result, err)
	}
	result, err = CommitWeeklyClear(ctx, g, cmd)
	if err != nil || !result.Replayed || result.Used["1"] != 1 {
		t.Fatal(result, err)
	}
	cmd.InstanceID = "10112233445566778899aabbccddeeff"
	result, err = CommitWeeklyClear(ctx, g, cmd)
	if err != nil || result.Used["1"] != 2 {
		t.Fatal(result, err)
	}
	cmd.InstanceID = "20112233445566778899aabbccddeeff"
	if _, err := CommitWeeklyClear(ctx, g, cmd); !errors.Is(err, ErrWeeklyClearLimit) {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2", "3"} {
		character, _, _ := g.Character.Load(ctx, id)
		used, err := WeeklyClears(character, 1, cmd.CompletedAt, cmd.Policy)
		want := 2
		if id == "3" {
			want = 0
		}
		if err != nil || used != want || character.Stats["unrelated"] != 77 {
			t.Fatal(id, used, err)
		}
		if other, err := WeeklyClears(character, 2, cmd.CompletedAt, cmd.Policy); err != nil || other != 0 {
			t.Fatal(other, err)
		}
	}
}

func TestWeeklyClearBoundaryAndOldInstanceReplayAcrossWeeks(t *testing.T) {
	g, cmd := weeklyFixture(t)
	ctx := context.Background()
	if _, err := CommitWeeklyClear(ctx, g, cmd); err != nil {
		t.Fatal(err)
	}
	character, _, _ := g.Character.Load(ctx, "1")
	before := time.Date(2026, 10, 7, 21, 59, 59, 0, time.UTC)
	if used, err := WeeklyClears(character, 1, before, cmd.Policy); err != nil || used != 1 {
		t.Fatal(used, err)
	}
	cmd.CompletedAt = before.Add(time.Second)
	if used, err := WeeklyClears(character, 1, cmd.CompletedAt, cmd.Policy); err != nil || used != 0 {
		t.Fatal(used, err)
	}
	result, err := CommitWeeklyClear(ctx, g, cmd)
	if err != nil || !result.Replayed || result.Used["1"] != 0 {
		t.Fatal(result, err)
	}
	cmd.InstanceID = "10112233445566778899aabbccddeeff"
	if result, err := CommitWeeklyClear(ctx, g, cmd); err != nil || result.Used["1"] != 1 {
		t.Fatal(result, err)
	}
}

func TestWeeklyClearConcurrentGroupCommitsRespectActualLimit(t *testing.T) {
	g, base := weeklyFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := base
			cmd.InstanceID = fmt.Sprintf("%032x", i+1)
			_, err := CommitWeeklyClear(context.Background(), g, cmd)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrWeeklyClearLimit) {
			t.Fatal(err)
		}
	}
	if successes != 2 {
		t.Fatal(successes)
	}
	for _, id := range []string{"1", "2"} {
		character, _, _ := g.Character.Load(context.Background(), id)
		used, err := WeeklyClears(character, 1, base.CompletedAt, base.Policy)
		if err != nil || used != 2 {
			t.Fatal(id, used, err)
		}
	}
}

func TestWeeklyClearLateWrongOwnerAndCorruptStateRollbackWholeGroup(t *testing.T) {
	for _, kind := range []string{"wrong_owner", "future_epoch", "partial_marker", "duplicate_owner", "missing_transaction"} {
		t.Run(kind, func(t *testing.T) {
			g, cmd := weeklyFixture(t)
			ctx := context.Background()
			character, _, _ := g.Character.Load(ctx, "2")
			switch kind {
			case "wrong_owner":
				cmd.Members[1].AccountID = "other"
			case "future_epoch":
				epoch, _ := WeeklyEpoch(cmd.CompletedAt, cmd.Policy)
				character.Stats["raid_weekly_epoch_v1_1"] = epoch + 7*86400
				character.Stats["raid_weekly_clears_v1_1"] = 1
				g.Character.Save(ctx, character)
			case "partial_marker":
				epoch, _ := WeeklyEpoch(cmd.CompletedAt, cmd.Policy)
				character.Stats["raid_clear_v1_1_"+cmd.InstanceID] = epoch
				g.Character.Save(ctx, character)
			case "duplicate_owner":
				cmd.Members = append(cmd.Members, cmd.Members[0])
			case "missing_transaction":
				g.CharacterTrade = nil
			}
			if _, err := CommitWeeklyClear(ctx, g, cmd); err == nil {
				t.Fatal("invalid completion accepted")
			}
			first, _, _ := g.Character.Load(ctx, "1")
			if _, exists := first.Stats["raid_weekly_clears_v1_1"]; exists {
				t.Fatal("late failure charged first owner")
			}
		})
	}
}
