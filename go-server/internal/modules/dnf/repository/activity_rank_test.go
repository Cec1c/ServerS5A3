package repository

import (
	"math"
	"testing"
	"time"
)

func TestActivityRankPeriodsTiesCosmeticsAndOverflow(t *testing.T) {
	boundary := time.Date(2026, 11, 1, 22, 0, 0, 0, time.UTC)
	before, _ := ActivityRankPeriod("week", boundary.Add(-time.Nanosecond))
	after, _ := ActivityRankPeriod("week", boundary)
	if before.FromDay != "2026-10-26" || before.ThroughDay != "2026-11-01" || after.FromDay != "2026-11-02" {
		t.Fatal(before, after)
	}
	if before.Closed(boundary.Add(4*time.Minute)) || !before.Closed(boundary.Add(5*time.Minute)) {
		t.Fatal("结算缓冲边界错误")
	}
	rows := []ActivityRankContribution{{"a", "1", 10}, {"a", "2", 5}, {"b", "3", 15}, {"c", "4", 3}, {"gm", "5", 1000}}
	ranks, err := BuildActivityRank(rows, []string{"gm"})
	if err != nil || len(ranks) != 3 || ranks[0].Score != 15 || len(ranks[0].Characters) != 2 || ranks[0].Rank != 1 || ranks[1].Rank != 1 || ranks[2].Rank != 3 {
		t.Fatal(ranks, err)
	}
	p := ActivityRankPolicy{Version: "v1", ExcludedAccounts: []string{"gm"}, Rewards: []ActivityRankRewardTier{{1, 1, "appearance:weekly_champion"}, {2, 10, "appearance:weekly_top10"}}}
	s, err := NewActivityRankSnapshot(ActivityRankSettlement{Metric: ActivityFatigue, Week: "2026-10-26", Policy: p, Now: boundary.Add(5 * time.Minute)}, rows)
	if err != nil || len(s.Rewards) != 3 || s.Rewards[0].AppearanceCode != s.Rewards[1].AppearanceCode || s.Rewards[2].Rank != 3 {
		t.Fatal(s, err)
	}
	for _, code := range []string{"gold:10000", "item:123", "appearance:", "appearance:../bad"} {
		p.Rewards[0].AppearanceCode = code
		if p.Validate() == nil {
			t.Fatal("接受非外观代号", code)
		}
	}
	if _, err := BuildActivityRank([]ActivityRankContribution{{"a", "1", math.MaxInt64}, {"a", "2", 1}}, nil); err == nil {
		t.Fatal("溢出未拒绝")
	}
	if _, err := BuildActivityRank([]ActivityRankContribution{{"a", "1", 1}, {"a", "1", 1}}, nil); err == nil {
		t.Fatal("重复角色投影未拒绝")
	}
}

func TestActivityGoldSourcesAndTransferIsolation(t *testing.T) {
	ch := CharacterRecord{CharacterID: "1", AccountID: "a"}
	now := time.Now()
	for _, m := range []string{ActivityGoldReinforce, ActivityGoldAmplify, ActivityGoldForge, ActivityGoldRepair, ActivityGoldNPC, ActivityGoldEnchant, ActivityGoldTradeOut, ActivityGoldTradeIn, ActivityGoldTradeFee, ActivityGoldMailOut, ActivityGoldPostage} {
		e, err := newGoldActivity(ch, m, 10, now)
		if err != nil {
			t.Fatal(m, err)
		}
		e.Source = "dungeon_pickup"
		if e.Validate() == nil {
			t.Fatal("伪造来源通过", m)
		}
	}
	v, err := SummarizeActivityGold(map[string]int64{ActivityGoldReinforce: 5, ActivityGoldRepair: 6, ActivityGoldTradeOut: 10000, ActivityGoldTradeIn: 9700, ActivityGoldTradeFee: 300, ActivityGoldMailOut: 5000, ActivityGoldPostage: 100})
	if err != nil || v.Spent != 11 || v.Transfers[ActivityGoldTradeOut] != 10000 || v.Fees[ActivityGoldTradeFee] != 300 {
		t.Fatal(v, err)
	}
	if _, err := SummarizeActivityGold(map[string]int64{ActivityGoldReinforce: math.MaxInt64, ActivityGoldRepair: 1}); err == nil {
		t.Fatal("金币总计溢出未拒绝")
	}
}
