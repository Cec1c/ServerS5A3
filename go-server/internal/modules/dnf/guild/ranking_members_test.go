package guild

import (
	"math"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestRankingMemberFactorsUsesRealOwnersAndMonthlyClearReceipts(t *testing.T) {
	rules, err := ParseRankingRules(rankingRulesFixture)
	if err != nil {
		t.Fatal(err)
	}
	reg, g, master, member := textRegistry(t)
	_ = reg
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, rankingLocation)
	joined := RankingMonthStart(now).Add(time.Hour)
	for _, id := range []string{master.CharacterID, member.CharacterID} {
		m := g.Members[id]
		m.JoinedAt = joined
		m.LastOnlineAt = now.Add(-8 * 24 * time.Hour)
		g.Members[id] = m
	}
	facts := map[string]RankingMemberFacts{
		master.CharacterID: {Owner: master, AdventureLevel: 20, PVPGrade: 8, Online: true, Character: r.CharacterRecord{AccountID: master.AccountID, CharacterID: master.CharacterID, Stats: map[string]int64{
			"raid_clear_v1_1_01234567890123456789012345678901": now.Add(-time.Hour).Unix(),
			"raid_clear_v1_2_01234567890123456789012345678902": now.Add(-time.Hour).Unix(),
			"raid_clear_v1_3_01234567890123456789012345678903": joined.Add(-time.Second).Unix(),
			"raid_clear_v1_3_01234567890123456789012345678904": now.Add(time.Second).Unix(),
			"raid_clear_v1_1_bad":                              now.Unix(),
			"raid_weekly_clears_v1_1":                          100,
		}}},
		member.CharacterID: {Owner: member, AdventureLevel: 30, PVPGrade: 0, Newbie: true, Character: r.CharacterRecord{AccountID: member.AccountID, CharacterID: member.CharacterID}},
	}
	f, err := RankingMemberFactors(g, facts, rules, now)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(f.Members)-0.2) > 0.000001 || math.Abs(float64(f.WeeklyActive)-0.075) > 0.000001 || f.Adventure != 13.5 || f.PVP != 4 || f.RaidClear != 3 {
		t.Fatalf("%+v", f)
	}
	bad := facts[member.CharacterID]
	bad.Owner.AccountID = "other"
	facts[member.CharacterID] = bad
	if _, err := RankingMemberFactors(g, facts, rules, now); err == nil {
		t.Fatal("foreign account contributes points")
	}
}
