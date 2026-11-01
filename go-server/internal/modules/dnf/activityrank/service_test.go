package activityrank

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

func TestActivityRankServicePagesSelfCacheAndBoundaries(t *testing.T) {
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"a", "b"} {
		if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: id, AccountID: id}); err != nil {
			t.Fatal(err)
		}
	}
	add := func(id, event string) {
		t.Helper()
		err := g.CharacterSettlement.WithinCharacterSettlement(ctx, id, func(tx repo.Group) error {
			_, e := tx.Activity.Record(ctx, repo.ActivityEvent{ID: event, AccountID: id, CharacterID: id, Metric: repo.ActivityFatigue, Amount: 1, OccurredAt: now, DungeonID: 310, Source: "dungeon_room"})
			return e
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	add("a", "a1")
	add("b", "b1")
	s, err := New(g.Activity.(repo.ActivityRankingRepository), repo.ActivityRankPolicy{Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	q := Query{Metric: repo.ActivityFatigue, Period: "day", AccountID: "b", Limit: 1}
	b, err := s.Board(ctx, q, now)
	if err != nil || b.TotalAccounts != 2 || len(b.Entries) != 1 || b.Self == nil || b.Self.AccountID != "b" || b.Self.Rank != 1 {
		t.Fatal(b, err)
	}
	b.Entries[0].Characters[0].Score = 999
	b.Self.Characters[0].Score = 999
	add("b", "b2")
	b, _ = s.Board(ctx, q, now.Add(time.Second))
	if b.Self.Score != 1 || b.Self.Characters[0].Score == 999 || b.Entries[0].Characters[0].Score == 999 {
		t.Fatal("缓存可变或失效边界错误", b)
	}
	b, _ = s.Board(ctx, q, now.Add(CacheLifetime))
	if b.Self.Score != 2 || b.Entries[0].AccountID != "b" {
		t.Fatal(b)
	}
	b, err = s.Board(ctx, q, now.Add(24*time.Hour))
	if err != nil || b.TotalAccounts != 0 || b.Self != nil {
		t.Fatal("跨日缓存污染", b, err)
	}
	q.Metric = repo.ActivityGoldTradeOut
	if _, err = s.Board(ctx, q, now); err == nil {
		t.Fatal("转账被用作排名")
	}
	q.Metric = repo.ActivityEpic
	q.Period = "week"
	q.Week = "2026-11-02"
	if _, err = s.Board(ctx, q, now); err == nil {
		t.Fatal("未来周查询未拒绝")
	}
}

func TestActivityRankServiceSettlementFrozenCacheAndDue(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	policy := repo.ActivityRankPolicy{Version: "v1"}
	s, _ := New(g.Activity.(repo.ActivityRankingRepository), policy)
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	q := Query{Metric: repo.ActivityEpic, Period: "week", Week: "2026-10-26"}
	before, err := s.Board(ctx, q, now)
	if err != nil || before.Settled {
		t.Fatal(before, err)
	}
	all, err := s.SettleDue(ctx, "2026-10-26", now, policy)
	if err != nil || len(all) != 5 {
		t.Fatal(all, err)
	}
	after, err := s.Board(ctx, q, now)
	if err != nil || !after.Settled {
		t.Fatal("结算未失效缓存", after, err)
	}
	again, created, err := s.Settle(ctx, repo.ActivityRankSettlement{Metric: repo.ActivityEpic, Week: q.Week, Policy: policy}, now.Add(time.Hour))
	if err != nil || created || !again.SettledAt.Equal(now) {
		t.Fatal("重复调度改变快照", again, created, err)
	}
}
