package dungeon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func dimensionGateFixture(t *testing.T) (dnfrepo.Group, *Owner, DimensionGateEntryCommand) {
	t.Helper()
	repos, owner := seedEntryItemInventory(t, map[string]dnfrepo.ItemStack{})
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: "19", Stats: map[string]int64{"fatigue": 100}}); err != nil {
		t.Fatal(err)
	}
	return repos, owner, DimensionGateEntryCommand{CharacterID: "19", Limit: 5, UpdatedAt: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
}

// 共享池：异次元裂缝内任意副本都记同一个角色级日计数，额度用尽后拒绝。
func TestDimensionGateDailyEntrySharedCounterAndServiceDayReset(t *testing.T) {
	repos, owner, cmd := dimensionGateFixture(t)
	for i := int64(1); i <= cmd.Limit; i++ {
		result, err := owner.ConsumeDimensionGateEntry(context.Background(), cmd)
		if err != nil || result.Used != i {
			t.Fatalf("entry %d: %+v err=%v", i, result, err)
		}
		owner, _ = NewOwner(repos) // 新会话/新 owner 读到的必须是落库后的计数。
	}
	if _, err := owner.ConsumeDimensionGateEntry(context.Background(), cmd); !errors.Is(err, ErrDailyEntryLimit) {
		t.Fatalf("sixth entry: %v", err)
	}
	stored, _, _ := repos.Character.Load(context.Background(), "19")
	used, err := DimensionGateDailyUsed(stored, cmd.UpdatedAt)
	if err != nil || used != cmd.Limit {
		t.Fatalf("exhausted pool used=%d err=%v", used, err)
	}
	cmd.UpdatedAt = time.Date(2026, 10, 5, 21, 59, 59, 0, time.UTC) // 05:59:59 UTC+8，仍未换日。
	if _, err := owner.ConsumeDimensionGateEntry(context.Background(), cmd); !errors.Is(err, ErrDailyEntryLimit) {
		t.Fatal("allowance reset before 06:00")
	}
	cmd.UpdatedAt = cmd.UpdatedAt.Add(time.Second)
	result, err := owner.ConsumeDimensionGateEntry(context.Background(), cmd)
	if err != nil || result.Used != 1 {
		t.Fatalf("next service day: %+v %v", result, err)
	}
	stored, _, _ = repos.Character.Load(context.Background(), "19")
	if next, err := DimensionGateDailyUsed(stored, cmd.UpdatedAt); err != nil || next != 1 {
		t.Fatalf("next service day counter=%d err=%v", next, err)
	}
}

// 并发请求不能超额；失败的请求不得写任何字节。
func TestDimensionGateDailyEntryConcurrentRequestsCannotExceedLimit(t *testing.T) {
	repos, owner, cmd := dimensionGateFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := owner.ConsumeDimensionGateEntry(context.Background(), cmd)
			results <- err
		}()
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
	stored, _, _ := repos.Character.Load(context.Background(), "19")
	used, err := DimensionGateDailyUsed(stored, cmd.UpdatedAt)
	if accepted != int(cmd.Limit) || used != cmd.Limit {
		t.Fatalf("accepted=%d used=%d err=%v", accepted, used, err)
	}
}

func TestDimensionGateDailyEntryRejectsInvalidCommands(t *testing.T) {
	_, owner, cmd := dimensionGateFixture(t)
	for _, invalid := range []DimensionGateEntryCommand{
		{CharacterID: "", Limit: 5, UpdatedAt: cmd.UpdatedAt},
		{CharacterID: "19", Limit: 0, UpdatedAt: cmd.UpdatedAt},
		{CharacterID: "19", Limit: 5},
	} {
		if _, err := owner.ConsumeDimensionGateEntry(context.Background(), invalid); !errors.Is(err, ErrEntryItemConsumeInvalid) {
			t.Fatalf("invalid command accepted: %+v %v", invalid, err)
		}
	}
	if result, err := owner.ConsumeDimensionGateEntry(context.Background(), DimensionGateEntryCommand{CharacterID: "404", Limit: 5, UpdatedAt: cmd.UpdatedAt}); err == nil {
		t.Fatalf("missing character accepted: %+v", result)
	}
}
