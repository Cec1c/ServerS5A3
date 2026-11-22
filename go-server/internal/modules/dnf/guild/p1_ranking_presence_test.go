package guild

import (
	"testing"
	"time"
)

func TestP1MemberHasRankingPresenceFor(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	// 2026-11-16 是周一；06:00+08:00 为新一周排行边界。
	beforeBoundary := time.Date(2026, 11, 16, 5, 59, 50, 0, loc).UTC()
	afterBoundary := time.Date(2026, 11, 16, 6, 0, 10, 0, loc).UTC()
	sameWeekLater := time.Date(2026, 11, 16, 12, 0, 0, 0, loc).UTC()
	nextMonth := time.Date(2026, 12, 2, 10, 0, 0, 0, loc).UTC()

	var m Member
	if m.HasRankingPresenceFor(beforeBoundary) {
		t.Fatal("nil RankingPresence must report false")
	}
	if m.HasRankingPresenceFor(time.Time{}) {
		t.Fatal("zero time must report false")
	}

	m.observeRankingPresence(beforeBoundary)
	if !m.HasRankingPresenceFor(beforeBoundary) {
		t.Fatal("same bucket must report true")
	}
	if m.HasRankingPresenceFor(afterBoundary) {
		t.Fatal("crossing Monday 06:00 UTC+8 boundary must report false")
	}

	m.observeRankingPresence(afterBoundary)
	if !m.HasRankingPresenceFor(sameWeekLater) {
		t.Fatal("later in same ranking week must report true")
	}
	if m.HasRankingPresenceFor(nextMonth) {
		t.Fatal("different ranking month must report false")
	}
}
