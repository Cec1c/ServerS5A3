package memory

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"sync"
	"testing"
	"time"
)

func TestActivityGoldOriginalTransactionsRollbackAndOwnership(t *testing.T) {
	for _, kind := range []string{"character", "account", "mail", "trade", "settlement"} {
		for _, fail := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "成功", true: "回滚"}[fail], func(t *testing.T) {
				g := activityFixture(t)
				ctx := context.Background()
				sentinel := errors.New("后续业务拒绝")
				apply := func(chars repo.CharacterRepository) error {
					ch, _, _ := chars.Load(ctx, "1")
					ch.Stats = map[string]int64{"gold": 90}
					if err := chars.Save(ctx, ch); err != nil {
						return err
					}
					if err := repo.RecordCharacterGold(ctx, chars, ch, repo.ActivityGoldRepair, 10, time.Now()); err != nil {
						return err
					}
					if fail {
						return sentinel
					}
					return nil
				}
				var err error
				switch kind {
				case "character":
					err = g.CharacterAssets.WithinCharacterAssets(ctx, "1", func(c repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository) error {
						return apply(c)
					})
				case "account":
					err = g.AccountAssets.WithinAccountCharacterAssets(ctx, "a", "1", func(_ repo.AccountInventoryRepository, c repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository) error {
						return apply(c)
					})
				case "mail":
					err = g.MailboxAssets.WithinMailboxAssets(ctx, "1", "2", func(c repo.CharacterRepository, _ repo.InventoryRepository, _ repo.MailboxRepository) error {
						return apply(c)
					})
				case "trade":
					err = g.CharacterTrade.WithinCharacterTrade(ctx, "1", "2", func(c repo.CharacterRepository, _ repo.InventoryRepository) error { return apply(c) })
				case "settlement":
					err = g.CharacterSettlement.WithinCharacterSettlement(ctx, "1", func(tx repo.Group) error {
						ch, _, _ := tx.Character.Load(ctx, "1")
						if err := repo.RecordGoldActivity(ctx, tx.Activity, ch, repo.ActivityGoldNPC, 10, time.Now()); err != nil {
							return err
						}
						if fail {
							return sentinel
						}
						return nil
					})
				}
				if fail && !errors.Is(err, sentinel) || !fail && err != nil {
					t.Fatal(err)
				}
				s, _ := g.Activity.Summary(ctx, repo.ActivityQuery{AccountID: "a"})
				gold, _ := repo.SummarizeActivityGold(s.Metrics)
				if fail && gold.Spent != 0 || !fail && gold.Spent != 10 {
					t.Fatal(gold)
				}
			})
		}
	}
}

func TestActivityRankSettlementConcurrentFrozenAndRetry(t *testing.T) {
	g := activityFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"1", "2"} {
		if err := recordActivity(g, activityEvent("event"+id, id, at)); err != nil {
			t.Fatal(err)
		}
	}
	store := g.Activity.(repo.ActivityRankingRepository)
	w, _ := repo.ActivityRankWeek("2026-10-26")
	rows, err := store.RankContributions(ctx, repo.ActivityFatigue, w)
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	req := repo.ActivityRankSettlement{Metric: repo.ActivityFatigue, Week: w.FromDay, Now: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), Policy: repo.ActivityRankPolicy{Version: "v1", Rewards: []repo.ActivityRankRewardTier{{FromRank: 1, ThroughRank: 10, AppearanceCode: "appearance:weekly"}}}}
	var wg sync.WaitGroup
	created := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, fresh, e := store.SettleRankWeek(ctx, req)
			if e != nil {
				t.Error(e)
			}
			created <- fresh
		}()
	}
	wg.Wait()
	close(created)
	n := 0
	for v := range created {
		if v {
			n++
		}
	}
	if n != 1 {
		t.Fatal("重复结算", n)
	}
	if err := recordActivity(g, activityEvent("late", "1", at)); err != nil {
		t.Fatal(err)
	}
	s, found, err := store.RankWeek(ctx, req.Metric, req.Week)
	if err != nil || !found || s.Entries[0].Score != 2 {
		t.Fatal(s, found, err)
	}
	s.Entries[0].Characters[0].Score = 999
	again, _, _ := store.RankWeek(ctx, req.Metric, req.Week)
	if again.Entries[0].Characters[0].Score == 999 {
		t.Fatal("快照可被调用者改写")
	}
	rewards, err := store.PendingRankRewards(ctx, "a", 100)
	if err != nil || len(rewards) != 1 || rewards[0].State != "pending" {
		t.Fatal(rewards, err)
	}
	req.Policy.Version = "v2"
	if _, _, err := store.SettleRankWeek(ctx, req); !errors.Is(err, repo.ErrActivityRankConflict) {
		t.Fatal("改规则覆盖旧周", err)
	}
	foreign, _ := store.PendingRankRewards(ctx, "foreign", 100)
	if len(foreign) != 0 {
		t.Fatal("跨账号奖励泄露")
	}
}
