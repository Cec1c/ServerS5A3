package activityrank

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

func TestRankGoldOnlyAggregatesCultivationFees(t *testing.T) {
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	now := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	ch := repo.CharacterRecord{CharacterID: "1", AccountID: "a"}
	if err := g.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	err := g.CharacterSettlement.WithinCharacterSettlement(ctx, "1", func(tx repo.Group) error {
		for metric, n := range map[string]int64{repo.ActivityGoldReinforce: 20, repo.ActivityGoldAmplify: 30, repo.ActivityGoldRepair: 5, repo.ActivityGoldNPC: 9999, repo.ActivityGoldTradeOut: 100000, repo.ActivityGoldTradeFee: 1000, repo.ActivityGoldPostage: 500} {
			if err := repo.RecordGoldActivity(ctx, tx.Activity, ch, metric, n, now); err != nil {
				return err
			}
		}
		e, err := repo.NewColorlessActivity(ch, "colorless:one", 7, "skill", now, 310)
		if err != nil {
			return err
		}
		_, err = tx.Activity.Record(ctx, e)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(g.Activity.(repo.ActivityRankingRepository), repo.ActivityRankPolicy{Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, period := range []string{"day", "week", "all"} {
		for metric, want := range map[string]int64{repo.ActivityGoldSpent: 55, repo.ActivityColorless: 7} {
			b, err := s.Board(ctx, Query{Metric: metric, Period: period, AccountID: "a"}, now)
			if err != nil || b.Self == nil || b.Self.Score != want || len(b.Self.Characters) != 1 {
				t.Fatal(metric, period, b, err)
			}
		}
	}
}
