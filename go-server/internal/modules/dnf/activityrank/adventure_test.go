package activityrank

import (
	"context"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestAdventureRankReusesTotalsAndSeparatesNewDates(t *testing.T) {
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	now := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	for _, ch := range []repo.CharacterRecord{
		{CharacterID: "1", AccountID: "a", Stats: map[string]int64{adventuregroup.RecommendedDungeonClearStatKey: 7}},
		{CharacterID: "2", AccountID: "a", Stats: map[string]int64{adventuregroup.RecommendedDungeonClearStatKey: 5}},
		{CharacterID: "3", AccountID: "b", Stats: map[string]int64{adventuregroup.RecommendedDungeonClearStatKey: 12}},
		{CharacterID: "4", AccountID: "a", Stats: map[string]int64{adventuregroup.RecommendedDungeonClearStatKey: 999, "delete_flag": 1}},
	} {
		if err := g.Character.Save(ctx, ch); err != nil {
			t.Fatal(err)
		}
	}
	for _, at := range []time.Time{now, now.AddDate(0, 0, -8)} {
		err := g.CharacterSettlement.WithinCharacterSettlement(ctx, "1", func(tx repo.Group) error {
			_, err := tx.Activity.Record(ctx, repo.ActivityEvent{ID: at.Format("20060102"), AccountID: "a", CharacterID: "1", Metric: repo.ActivityRecommendedDungeon, Amount: 1, OccurredAt: at, DungeonID: 310, Source: "recommended_clear"})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(ReuseAdventureTotals(g.Activity.(repo.ActivityRankingRepository), g.Character), repo.ActivityRankPolicy{Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, period := range []string{"all", "day", "week"} {
		b, err := s.Board(ctx, Query{Metric: repo.ActivityRecommendedDungeon, Period: period, AccountID: "a"}, now)
		if err != nil || b.Self == nil {
			t.Fatal(b, err)
		}
		if period == "all" {
			characters, _ := g.Character.ListByAccount(ctx, "a", 0)
			original := adventuregroup.ProjectActivity(0, characters)
			if b.Self.Score != int64(original.ContentCounts[0]) || b.Self.Score != 12 || len(b.Self.Characters) != 2 || b.TotalAccounts != 2 || b.Entries[0].Rank != 1 || b.Entries[1].Rank != 1 {
				t.Fatal("累计榜未直接复用冒险团", b)
			}
		} else if b.Self.Score != 1 || b.TotalAccounts != 1 || len(b.Self.Characters) != 1 {
			t.Fatal("历史累计被灌入日周榜", b)
		}
	}
	// 累计榜仍跟随冒险团现存角色，日期账本则保留已提交历史贡献。
	ch, _, _ := g.Character.Load(ctx, "2")
	ch.Stats["delete_flag"] = 1
	if err := g.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	b, err := s.Board(ctx, Query{Metric: repo.ActivityRecommendedDungeon, Period: "all", AccountID: "a"}, now.Add(CacheLifetime))
	if err != nil || b.Self == nil || b.Self.Score != 7 {
		t.Fatal(b, err)
	}
}
