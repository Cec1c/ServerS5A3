package dungeon

import (
	"context"
	"longheng.io/server/internal/modules/dnf/fatigue"
	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

func TestActivityDailyEntryFatigueRolloverAndFailedAdmission(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	now := time.Date(2026, 10, 24, 22, 0, 0, 0, time.UTC)
	account := repo.AccountRecord{AccountID: "a"}
	g.Account.Save(ctx, account)
	g.Character.Save(ctx, repo.CharacterRecord{AccountID: "a", CharacterID: "1", Stats: map[string]int64{"fatigue": 0, "fatigue_limit": 156, fatigue.DayKey: premium.ServiceDayID(now.Add(-time.Second))}})
	owner, _ := NewOwner(g)
	event := repo.ActivityEvent{ID: "cycle:fatigue:1", AccountID: "a", CharacterID: "1", Metric: repo.ActivityFatigue, Source: "cycle_entry", DungeonID: 310, OccurredAt: now}
	cmd := DailyEntryCommand{CharacterID: "1", DungeonID: 310, Limit: 1, UpdatedAt: now, Activity: &event, InTransaction: func(ch *repo.CharacterRecord) error {
		fatigue.Refresh(ch, account, now)
		before := ch.Stats["fatigue"]
		fatigue.Debit(ch)
		event.Amount = before - ch.Stats["fatigue"]
		return nil
	}}
	result, err := owner.ConsumeDailyEntry(ctx, cmd)
	if err != nil || result.Character.Stats["fatigue"] != 155 {
		t.Fatal(result, err)
	}
	event.ID = "rejected-admission"
	if _, err := owner.ConsumeDailyEntry(ctx, cmd); err == nil {
		t.Fatal("次数用完仍允许入场")
	}
	summary, _ := g.Activity.Summary(ctx, repo.ActivityQuery{AccountID: "a"})
	if summary.Metrics[repo.ActivityFatigue] != 1 {
		t.Fatal("跨日或失败入场计数错误", summary)
	}
}
