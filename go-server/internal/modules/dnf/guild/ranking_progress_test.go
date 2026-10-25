package guild

import (
	"math"
	"testing"
	"time"
)

func TestRankingDungeonConquestBranchesPurificationAndReset(t *testing.T) {
	rules, err := ParseRankingRules(rankingRulesFixture)
	if err != nil {
		t.Fatal(err)
	}
	unlock := &DungeonUnlockRules{byID: map[uint32]dungeonUnlockRule{}}
	for _, id := range []uint32{8500, 8504, 8507, 8505, 8503, 8508} {
		unlock.byID[id] = dungeonUnlockRule{limit: 100, gain: 1, threshold: 50}
	}
	catalog := &DungeonCatalog{Rules: unlock}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, rankingLocation)
	cycle, _ := (*DungeonPolicy)(nil).Cycle(now)
	g := &Guild{Dungeon: &DungeonProgress{CycleKey: cycle.Key, StartUnix: cycle.StartUnix, EndUnix: cycle.EndUnix, Counts: map[uint32]uint32{}}}
	for _, tt := range []struct {
		name   string
		counts map[uint32]uint32
		want   float32
	}{
		{"first dungeon", map[uint32]uint32{}, 10},
		{"first conquest", map[uint32]uint32{8500: 50}, 25},
		{"one fork insufficient", map[uint32]uint32{8500: 100, 8504: 100, 8507: 100, 8505: 50}, 65},
		{"both forks", map[uint32]uint32{8500: 100, 8504: 100, 8507: 100, 8505: 50, 8503: 50}, 90},
		{"all purified", map[uint32]uint32{8500: 100, 8504: 100, 8507: 100, 8505: 100, 8503: 100, 8508: 100}, 110},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g.Dungeon.Counts = tt.counts
			got, err := RankingDungeonPoints(g, nil, rules, catalog, now)
			if err != nil || got != tt.want {
				t.Fatal(got, err)
			}
		})
	}
	got, err := RankingDungeonPoints(g, nil, rules, catalog, time.Unix(int64(cycle.EndUnix), 0))
	if err != nil || got != 10 {
		t.Fatal("old cycle leaked", got, err)
	}
	rules.Dungeon.Maximum = 80
	got, err = RankingDungeonPoints(g, nil, rules, catalog, now)
	if err != nil || got != 80 {
		t.Fatal("PVF cap ignored", got, err)
	}
}

func TestRankingBenefitsExpireAndDoNotCountPurchasesTwice(t *testing.T) {
	rules, _ := ParseRankingRules(rankingRulesFixture)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, rankingLocation)
	g := &Guild{MemberLimit: 1000, Cargo: &Cargo{Capacity: 8}, Benefits: map[byte]Benefit{1: {Type: 1, Value: 10, ExpiresAt: now.Add(time.Hour)}, 3: {Type: 3, Value: 167, ExpiresAt: now.Add(time.Hour)}, 5: {Type: 5, Value: 169}, 7: {Type: 7, Value: 10}, 21: {Type: 21, Value: 1000, ExpiresAt: now.Add(time.Hour)}}}
	got, err := RankingBenefitPoints(g, rules, now)
	if err != nil || got != 50 {
		t.Fatal(got, err)
	}
	g.Benefits[7] = Benefit{Type: 7}
	got, err = RankingBenefitPoints(g, rules, now.Add(time.Hour))
	if err != nil || got != 20 {
		t.Fatal("expired/exhausted benefit still scored", got, err)
	}
}

func TestRankingPartyPointsMonthAndCorruptValues(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, rankingLocation)
	g := &Guild{RankingActivity: &RankingActivity{Month: RankingMonthStart(now), PartyClear: 7.5}}
	if got, err := RankingPartyPoints(g, now); err != nil || got != 7.5 {
		t.Fatal(got, err)
	}
	if got, err := RankingPartyPoints(g, now.AddDate(0, 1, 0)); err != nil || got != 0 {
		t.Fatal("last month became current", got, err)
	}
	if _, err := RankingPartyPoints(g, now.AddDate(0, -1, 0)); err == nil {
		t.Fatal("clock rollback accepted")
	}
	for _, bad := range []float64{-1, math.NaN(), math.Inf(1), math.MaxInt32 + 1} {
		g.RankingActivity.PartyClear = bad
		if _, err := RankingPartyPoints(g, now); err == nil {
			t.Fatal("corrupt points accepted", bad)
		}
	}
}
