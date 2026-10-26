package guild

import (
	"math/bits"
	"time"
)

// RankingPresence retains one bit per weekly unique-login bucket in the current
// ranking month. Weeks begin Monday 06:00 UTC+8; partial weeks belong to their
// actual month. No earlier visits are inferred from the latest login timestamp.
type RankingPresence struct {
	Month time.Time `json:"month"`
	Weeks uint8     `json:"weeks"`
}

func rankingWeekIndex(at time.Time) int {
	start := RankingMonthStart(at)
	days := (int(start.Weekday()) + 6) % 7
	weekStart := start.AddDate(0, 0, -days)
	return int(at.Sub(weekStart) / (7 * 24 * time.Hour))
}

func (m *Member) observeRankingPresence(at time.Time) {
	month := RankingMonthStart(at)
	observation := RankingPresence{Month: month}
	if m.RankingPresence != nil && m.RankingPresence.Month.Equal(month) {
		observation = *m.RankingPresence
	}
	observation.Weeks |= 1 << rankingWeekIndex(at)
	m.RankingPresence = &observation
}

func rankingWeeklyPresence(m Member, online bool, now time.Time) (float64, error) {
	month := RankingMonthStart(now)
	var observed uint8
	if p := m.RankingPresence; p != nil {
		if p.Month.IsZero() || !p.Month.Equal(RankingMonthStart(p.Month)) || p.Month.After(month) || p.Weeks&0xc0 != 0 {
			return 0, ErrRankingRules
		}
		if p.Month.Equal(month) {
			observed = p.Weeks
		}
	}
	// The legacy last-online value proves only this particular week.
	if !m.LastOnlineAt.Before(month) && !m.LastOnlineAt.After(now) && !m.LastOnlineAt.Before(m.JoinedAt) {
		observed |= 1 << rankingWeekIndex(m.LastOnlineAt)
	}
	if online {
		observed |= 1 << rankingWeekIndex(now)
	}
	weeks := rankingWeekIndex(now) + 1
	if observed>>weeks != 0 {
		return 0, ErrRankingRules
	}
	return float64(bits.OnesCount8(observed)) / float64(weeks), nil
}
