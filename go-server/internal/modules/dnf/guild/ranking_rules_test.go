package guild

import (
	"math"
	"os"
	"strings"
	"testing"
)

// The nested tags and numbers are from the matched ranking catalog. The
// independent member-ranking block deliberately assigns different points.
const rankingRulesFixture = `
[guild factor]
[member count]
[max point] 100
[rate] 0.10
[/member count]
[weekly uv]
[max point] 90
[rate] 0.10
[/weekly uv]
[guild dungeon]
[max point] 120
[min conquest point] 10
[dungeon conquest]
8500 8500 20 8504 8504 30 8507 8507 30 8505 8503 50 8508 8508 50
[/dungeon conquest]
[purify point rate] 0.10
[/guild dungeon]
[agitwar]
[battle point rate] 0.00
[defence point rate] 0.00
[attack point rate] 0.01
[/agitwar]
[guild contents]
[max point] 60
[rate] 10
[/guild contents]
[guild party clear]
7146 1 1.50 247 5 7.50 5106 5 7.50
[/guild party clear]
[/guild factor]
[guild member factor]
[adventure level]
1 10 1 1.50 11 15 2 3 16 20 3 4.50 21 25 5 7.50 26 30 7 10.50
[/adventure level]
[raid clear]
[point]
` + "`anton` 1 1.50 `luke` 2 3 `fiendwar` 2 3" + `
[/point]
[/raid clear]
[pvp]
0 0 1 1 1 4 2 2 5 8 3 3 9 12 5 5 13 16 6 6 17 21 7 7
[/pvp]
[event]
[event id] 0
[point] 1
[/event]
[/guild member factor]
[guild member ranking factor]
[guild party clear]
247 6 9 5106 6 9
[/guild party clear]
[/guild member ranking factor]
[newbie member requirement]
[join date]
` + "`2018-10-18 06:00:00`" + `
[/newbie member requirement]
`

func TestRankingRulesNestedCatalog(t *testing.T) {
	r, err := ParseRankingRules(rankingRulesFixture)
	if err != nil {
		t.Fatal(err)
	}
	assertRankingRules(t, r)
}

func assertRankingRules(t *testing.T, r *RankingRules) {
	t.Helper()
	if r.Dungeon.Rate != 0.1 || r.Dungeon.Maximum != 120 || r.WarAttack != 0.01 || r.Members.Maximum != 100 || r.WeeklyActive.Maximum != 90 || r.Contents.Rate != 10 || r.EventPoint != 1 || len(r.Conquests) != 5 {
		t.Fatalf("wrong nested fields: %+v", r)
	}
	if r.PartyClears[247] != (RankingPoints{5, 7.5}) || r.RaidClears["luke"] != (RankingPoints{2, 3}) || RankingLevelPoints(r.Adventure, 30, true) != 10.5 || RankingLevelPoints(r.PVP, 0, false) != 1 {
		t.Fatal("wrong scoring domain or range")
	}
	if r.NewbieAfter.UTC().Format("2006-01-02 15:04:05") != "2018-10-17 22:00:00" {
		t.Fatal(r.NewbieAfter)
	}
}

func TestRankingRulesExtractedCatalog(t *testing.T) {
	path := os.Getenv("GUILD_RANKING_EXTRACTED_ETC")
	if path == "" {
		t.Skip("set GUILD_RANKING_EXTRACTED_ETC to the extracted ranking catalog")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := ParseRankingRules(string(b))
	if err != nil {
		t.Fatal(err)
	}
	assertRankingRules(t, r)
	if len(r.PartyClears) != 41 {
		t.Fatalf("party clear rows=%d", len(r.PartyClears))
	}
}

func TestRankingRulesRejectIncompleteAndAmbiguousCatalog(t *testing.T) {
	for name, text := range map[string]string{
		"missing-zero":  strings.Replace(rankingRulesFixture, "[battle point rate] 0.00", "", 1),
		"missing-rate":  strings.Replace(rankingRulesFixture, "[purify point rate] 0.10", "", 1),
		"negative":      strings.Replace(rankingRulesFixture, "[rate] 0.10", "[rate] -0.10", 1),
		"duplicate":     strings.Replace(rankingRulesFixture, "[rate] 0.10", "[rate] 0.10\n[rate] 0.20", 1),
		"duplicate-row": strings.Replace(rankingRulesFixture, "247 5 7.50", "247 5 7.50 247 5 7.50", 1),
		"overlap":       strings.Replace(rankingRulesFixture, "11 15 2 3", "10 15 2 3", 1),
		"fraction-id":   strings.Replace(rankingRulesFixture, "8500 8500 20", "8500.5 8500 20", 1),
		"unfinished":    strings.Replace(rankingRulesFixture, "[/newbie member requirement]", "", 1),
		"wrong-close":   strings.Replace(rankingRulesFixture, "[/dungeon conquest]", "[/missing factor]", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseRankingRules(text); err == nil {
				t.Fatal("accepted invalid rules")
			}
		})
	}
}

func TestRankingFactorsRejectUnrepresentableScore(t *testing.T) {
	for _, n := range []float32{-1, float32(math.NaN()), float32(math.Inf(1)), float32(math.MaxInt32)} {
		if _, err := (RankingFactors{Members: n}).Total(); err == nil {
			t.Fatalf("accepted %v", n)
		}
	}
	f := RankingFactors{Members: 0.1, WeeklyActive: 0.1, Adventure: 1.5, RaidClear: 3}
	if total, err := f.Total(); err != nil || math.Abs(float64(total)-4.7) > 0.00001 {
		t.Fatalf("%v %v", total, err)
	}
}
