package guild

import (
	"reflect"
	"testing"
	"time"
)

func TestMonthlyRankingBoardHistoryTiesPaginationAndDisband(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, rankingLocation)
	reg := NewRegistry()
	factors := map[uint32]RankingFactors{}
	for id := uint32(1); id <= 9; id++ {
		reg.Guilds[id] = &Guild{ID: id, Name: "current name"}
		factors[id] = RankingFactors{Members: float32(10 - id)}
	}
	factors[2] = factors[1]
	factors[9] = RankingFactors{} // zero score is unranked
	previous, _ := BuildRankingPeriod(now.AddDate(0, -1, 0), factors)
	current, _ := BuildRankingPeriod(now, map[uint32]RankingFactors{1: {Members: 100}})
	reg.Ranking = &RankingState{Current: current, Previous: previous}
	rows, err := reg.MonthlyRankingBoard(now, true, 1)
	if err != nil || len(rows) != 2 || rows[0].Guild.ID != 1 || rows[0].Rank != 1 || rows[1].Rank != 3 || rows[1].Guild.ID != 3 {
		t.Fatal(rows, err)
	}
	rows, _ = reg.MonthlyRankingBoard(now, false, 1)
	if len(rows) != 5 || rows[0].Rank != 1 || rows[1].Rank != 1 || rows[2].Rank != 3 || rows[0].Score != 9 {
		t.Fatal(rows)
	}
	rows, _ = reg.MonthlyRankingBoard(now, false, 2)
	if len(rows) != 3 || rows[0].Guild.ID != 6 {
		t.Fatal(rows)
	}
	rows, _ = reg.MonthlyRankingBoard(now, false, 65535)
	if len(rows) != 0 {
		t.Fatal(rows)
	}
	delete(reg.Guilds, 1)
	rows, _ = reg.MonthlyRankingBoard(now, true, 1)
	if len(rows) != 2 || rows[0].Guild.ID != 2 || rows[0].Rank != 1 || rows[1].Rank != 3 {
		t.Fatal(rows)
	}
	if !reflect.DeepEqual(previous.Rows[1].Factors, factors[1]) {
		t.Fatal("query changed historical score")
	}
}

func TestMonthlyRankingBoardMissingMonthAndSixAMBoundary(t *testing.T) {
	boundary := time.Date(2026, 10, 1, 6, 0, 0, 0, rankingLocation)
	reg := NewRegistry()
	reg.Guilds[1] = &Guild{ID: 1}
	sept, _ := BuildRankingPeriod(boundary.Add(-time.Second), map[uint32]RankingFactors{1: {Members: 10}})
	reg.Ranking = &RankingState{Current: sept}
	before, _ := reg.MonthlyRankingBoard(boundary.Add(-time.Nanosecond), true, 1)
	after, _ := reg.MonthlyRankingBoard(boundary, true, 1)
	missing, _ := reg.MonthlyRankingBoard(boundary.AddDate(0, 1, 0), true, 1)
	if len(before) != 0 || len(after) != 1 || len(missing) != 0 {
		t.Fatal(before, after, missing)
	}
	for _, tc := range []struct {
		podium bool
		page   uint16
	}{{false, 0}, {true, 2}} {
		if _, err := reg.MonthlyRankingBoard(boundary, tc.podium, tc.page); err == nil {
			t.Fatal(tc)
		}
	}
	reg.Ranking.Current.Rows[1] = RankingRow{Rank: 1, Score: 999}
	if _, err := reg.MonthlyRankingBoard(boundary, true, 1); err == nil {
		t.Fatal("corrupt snapshot accepted")
	}
}
