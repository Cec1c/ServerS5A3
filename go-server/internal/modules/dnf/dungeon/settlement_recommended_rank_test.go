package dungeon

import (
	"context"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestRecommendedRankSettlementReplayDatesAndRollback(t *testing.T) {
	g, cmd := luckyStarSettlementFixture(t, "0")
	cmd.LuckyStar = nil
	cmd.DungeonID = 310
	cmd.UpdatedAt = time.Date(2026, 11, 1, 21, 59, 59, 0, time.UTC)
	ctx := context.Background()
	ch, _, _ := g.Character.Load(ctx, "c")
	ch.Stats[adventuregroup.RecommendedDungeonClearStatKey] = 7
	if err := g.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	o, _ := NewOwner(g)
	first, err := o.CommitSettlement(ctx, cmd)
	if err != nil || first.Replayed {
		t.Fatal(first, err)
	}
	cmd.UpdatedAt = cmd.UpdatedAt.Add(time.Second)
	replay, err := o.CommitSettlement(ctx, cmd)
	if err != nil || !replay.Replayed {
		t.Fatal(replay, err)
	}
	cmd.CompletionKey = "next-day-next-week"
	if _, err = o.CommitSettlement(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	for _, period := range []string{"day", "week", "all"} {
		w, _ := repo.ActivityRankPeriod(period, cmd.UpdatedAt)
		rows, err := g.Activity.(repo.ActivityRankingRepository).RankContributions(ctx, repo.ActivityRecommendedDungeon, w)
		want := int64(1)
		if period == "all" {
			want = 2
		}
		if err != nil || len(rows) != 1 || rows[0].Score != want {
			t.Fatal(period, rows, err)
		}
	}
	ch, _, _ = g.Character.Load(ctx, "c")
	if ch.Stats[adventuregroup.RecommendedDungeonClearStatKey] != 9 {
		t.Fatal(ch.Stats)
	}
	oldKey := cmd.CompletionKey
	cmd.CompletionKey = "clear-1"
	if _, err := o.CommitSettlement(ctx, cmd); err == nil {
		t.Fatal("旧局在新局之后被再次提交")
	}
	cmd.CompletionKey = oldKey
	// 事务最终保存失败时，日期统计和冒险团累计一起回滚。
	cmd.CompletionKey = "failed-clear"
	g.CharacterSettlement = failDespairPotionCommit{g.CharacterSettlement}
	failing, _ := NewOwner(g)
	if _, err := failing.CommitSettlement(ctx, cmd); err == nil {
		t.Fatal("未产生预期保存故障")
	}
	ch, _, _ = g.Character.Load(ctx, "c")
	events, err := g.Activity.Events(ctx, repo.ActivityQuery{AccountID: "a"})
	if err != nil || len(events) != 2 || ch.Stats[adventuregroup.RecommendedDungeonClearStatKey] != 9 {
		t.Fatal(ch, events, err)
	}
	for _, event := range events {
		if event.DungeonID != 310 || event.Metric != repo.ActivityRecommendedDungeon {
			t.Fatal(event)
		}
	}
}

func TestRecommendedRankIgnoresNonRecommendedSettlement(t *testing.T) {
	g, cmd := luckyStarSettlementFixture(t, "0")
	cmd.LuckyStar, cmd.RecommendedDungeonClear = nil, false
	o, _ := NewOwner(g)
	if _, err := o.CommitSettlement(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	events, err := g.Activity.Events(context.Background(), repo.ActivityQuery{AccountID: "a"})
	if err != nil || len(events) != 0 {
		t.Fatal(events, err)
	}
}
