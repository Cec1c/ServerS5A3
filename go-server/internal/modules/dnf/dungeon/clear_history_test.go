package dungeon

import (
	"context"
	"longheng.io/server/internal/modules/dnf/moderation"
	"longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func TestClearHistorySettlementReplayAndRollback(t *testing.T) {
	repos, cmd := luckyStarSettlementFixture(t, "0")
	cmd.LuckyStar = nil
	ctx := context.Background()
	now := time.Now().UTC()
	spent := int64(7)
	cmd.Clear = &moderation.Clear{FatigueSpent: &spent, DungeonID: 123, DungeonName: "测试地下城", StartedAt: now.Add(-time.Minute), FinishedAt: now, DurationMS: 60000}
	owner, _ := NewOwner(repos)
	for i := 0; i < 2; i++ {
		if _, err := owner.CommitSettlement(ctx, cmd); err != nil {
			t.Fatal(err)
		}
	}
	reader := repos.Character.(repository.ClearHistoryReader)
	q := moderation.Query{From: now.Add(-time.Hour), To: now.Add(time.Hour)}
	page, err := reader.QueryClears(ctx, q)
	if err != nil || len(page.Rows) != 1 || page.Rows[0].Count != 1 || page.Rows[0].FatigueSpent == nil || *page.Rows[0].FatigueSpent != 7 || page.Rows[0].FatigueSamples != 1 || page.Rows[0].FastestMS != 60000 || page.Rows[0].PeakHourly != 1 {
		t.Fatalf("history: %+v %v", page, err)
	}
	cmd.CompletionKey = "another-completion"
	repos.CharacterSettlement = failDespairPotionCommit{repos.CharacterSettlement}
	failed, _ := NewOwner(repos)
	if _, err := failed.CommitSettlement(ctx, cmd); err == nil {
		t.Fatal("expected rollback")
	}
	page, err = reader.QueryClears(ctx, q)
	if err != nil || page.Rows[0].Count != 1 {
		t.Fatalf("rollback leaked history: %+v %v", page, err)
	}
	if _, err := owner.CommitSettlement(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	page, err = reader.QueryClears(ctx, q)
	if err != nil || page.Rows[0].Count != 2 || *page.Rows[0].FatigueSpent != 14 || *page.Rows[0].AverageFatigue != 7 {
		t.Fatalf("second clear: %+v %v", page, err)
	}
}
