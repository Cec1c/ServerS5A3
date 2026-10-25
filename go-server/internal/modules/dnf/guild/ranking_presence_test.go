package guild

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRankingPresenceMonthlyWeeklyAverageAndRestart(t *testing.T) {
	start := time.Date(2026, 10, 1, 6, 0, 0, 0, rankingLocation)
	m := Member{JoinedAt: start}
	// October begins on Thursday: Oct 1-5 is the first partial week.
	m.observeRankingPresence(start)
	m.observeRankingPresence(start.Add(time.Hour))
	if got, err := rankingWeeklyPresence(m, false, start.Add(time.Hour)); err != nil || got != 1 {
		t.Fatal(got, err)
	}
	monday := time.Date(2026, 10, 5, 6, 0, 0, 0, rankingLocation)
	if got, err := rankingWeeklyPresence(m, false, monday); err != nil || got != 0.5 {
		t.Fatal(got, err)
	}
	m.observeRankingPresence(monday)
	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var restored Member
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if got, err := rankingWeeklyPresence(restored, false, monday.Add(7*24*time.Hour)); err != nil || got != 2.0/3 {
		t.Fatal(got, err)
	}
	next := time.Date(2026, 11, 1, 6, 0, 0, 0, rankingLocation)
	if got, err := rankingWeeklyPresence(restored, false, next); err != nil || got != 0 {
		t.Fatal(got, err)
	}
	if got, err := rankingWeeklyPresence(restored, true, next); err != nil || got != 1 {
		t.Fatal(got, err)
	}
	if _, err := rankingWeeklyPresence(restored, false, start.AddDate(0, -1, 0)); err == nil {
		t.Fatal("future observation accepted")
	}
}

func TestRankingPresenceDoesNotInventPriorWeeks(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, rankingLocation)
	m := Member{JoinedAt: RankingMonthStart(now), LastOnlineAt: now}
	if got, err := rankingWeeklyPresence(m, true, now); err != nil || got != 0.25 {
		t.Fatal(got, err)
	}
	for _, mask := range []uint8{0x10, 0x40, 0xff} {
		m.RankingPresence = &RankingPresence{Month: RankingMonthStart(now), Weeks: mask}
		if _, err := rankingWeeklyPresence(m, false, now); err == nil {
			t.Fatal("future/corrupt weekly bits accepted", mask)
		}
	}
}
