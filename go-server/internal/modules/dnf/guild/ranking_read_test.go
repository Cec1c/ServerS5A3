package guild

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildRankingReadMatchesSourceAndReusesExactVersion(t *testing.T) {
	store, reg, owner, now := newSnapshotSettingsFixture(t)
	ctx := context.Background()
	current, err := BuildRankingPeriod(now, map[uint32]RankingFactors{1: {Members: 12, RaidClear: 6}})
	if err != nil {
		t.Fatal(err)
	}
	previous, err := BuildRankingPeriod(RankingMonthStart(now).AddDate(0, -1, 0), map[uint32]RankingFactors{1: {Members: 4}})
	if err != nil {
		t.Fatal(err)
	}
	reg.Ranking = &RankingState{Current: current, Previous: previous}
	m := reg.Guilds[1].Members[owner.CharacterID]
	m.RankingActivity = &MemberRankingActivity{Month: RankingMonthStart(now), RaidClears: [3]uint32{1, 2, 0}, Mileage: 10}
	reg.Guilds[1].Members[owner.CharacterID] = m
	if err := Save(ctx, store, reg, now); err != nil {
		t.Fatal(err)
	}
	rules := &MemberRankingRules{RaidClears: [3]float64{1, 2, 3}, MileageRate: 1}
	wantPoints, err := MemberRankingPoints(m, rules, now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		view, err := ReadRankingView(ctx, store)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := view.Rows(owner, now, 5*time.Minute)
		if err != nil || rows != reg.Ranking.View(1, now) {
			t.Fatal(rows, err)
		}
		rows[0].Score = 999
		points, err := view.MemberPoints(owner, 1, m.ServerGroup, m.Name, rules, now)
		if err != nil || points != wantPoints {
			t.Fatal(points, err)
		}
		for _, podium := range []bool{true, false} {
			board, err := view.MonthlyBoard(now, podium, 1)
			want, expectedErr := reg.MonthlyRankingBoard(now, podium, 1)
			if err != expectedErr || len(board) != len(want) || len(board) != 1 {
				t.Fatal(board, err)
			}
			g := want[0].Guild
			expected := RankingBoardProjection{ID: g.ID, Name: g.Name, MasterName: g.MasterName, Mark: g.Mark, MemberCount: len(g.Members), RankingRow: want[0].RankingRow}
			if board[0] != expected {
				t.Fatal(board, expected)
			}
			board[0].Name = "poison"
		}
	}
	if store.loads != 1 {
		t.Fatalf("unchanged source loads=%d", store.loads)
	}
}

func TestGuildRankingReadFreshnessOwnershipAndLegacyInvalidation(t *testing.T) {
	store, reg, owner, now := newSnapshotSettingsFixture(t)
	ctx := context.Background()
	p, _ := BuildRankingPeriod(now, map[uint32]RankingFactors{1: {Members: 12}})
	reg.Ranking = &RankingState{Current: p}
	if err := Save(ctx, store, reg, now); err != nil {
		t.Fatal(err)
	}
	old, err := ReadRankingView(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{now.Add(-time.Nanosecond), now.Add(5 * time.Minute), RankingMonthStart(now).AddDate(0, 1, 0)} {
		if _, err := old.Rows(owner, at, 5*time.Minute); !errors.Is(err, ErrRankingSnapshotChanged) {
			t.Fatal(at, err)
		}
	}
	foreign := r.GuildOwner{CharacterID: owner.CharacterID, AccountID: "foreign"}
	if _, err := old.Rows(foreign, now, time.Minute); !errors.Is(err, ErrNotMember) {
		t.Fatal(err)
	}
	m := reg.Guilds[1].Members[owner.CharacterID]
	if _, err := old.MemberPoints(owner, 2, m.ServerGroup, m.Name, nil, now); !errors.Is(err, ErrNotMember) {
		t.Fatal(err)
	}
	if _, err := old.MemberPoints(owner, 1, m.ServerGroup+1, m.Name, nil, now); !errors.Is(err, ErrCharacterNotFound) {
		t.Fatal(err)
	}
	// Save an ordinary legacy source write at the exact same timestamp.
	reg.Guilds[1].Name = "Changed"
	p, _ = BuildRankingPeriod(now, map[uint32]RankingFactors{1: {Members: 30}})
	reg.Ranking.Current = p
	if err := Save(ctx, store, reg, now); err != nil {
		t.Fatal(err)
	}
	latest, err := ReadRankingView(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := latest.Rows(owner, now, time.Minute)
	if err != nil || rows[0].Score != 30 {
		t.Fatal(rows, err)
	}
	before, _ := old.Rows(owner, now, time.Minute)
	if before[0].Score != 12 {
		t.Fatal("bounded view changed", before)
	}
	if store.loads != 2 {
		t.Fatal(store.loads)
	}
	delete(reg.Guilds, 1)
	if err := Save(ctx, store, reg, now); err != nil {
		t.Fatal(err)
	}
	latest, err = ReadRankingView(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := latest.Rows(owner, now, time.Minute); !errors.Is(err, ErrNotMember) {
		t.Fatal(err)
	}
	board, err := latest.MonthlyBoard(now, false, 1)
	if err != nil || len(board) != 0 {
		t.Fatal(board, err)
	}
	store.mu.Lock()
	store.record.Values["state"] = "{invalid"
	store.mu.Unlock()
	if _, err := ReadRankingView(ctx, store); err == nil {
		t.Fatal("corrupt source served cached ranking")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ReadRankingView(cancelled, store); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestGuildRankingReadChurnAndMemberResultIsolation(t *testing.T) {
	store, reg, owner, now := newSnapshotSettingsFixture(t)
	p, _ := BuildRankingPeriod(now, map[uint32]RankingFactors{1: {Members: 12}})
	reg.Ranking = &RankingState{Current: p}
	if err := Save(context.Background(), store, reg, now); err != nil {
		t.Fatal(err)
	}
	view, err := ReadRankingView(context.Background(), hallChurningSettings{store})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := view.Rows(owner, now, time.Minute)
	if err != nil || !reflect.DeepEqual(rows, reg.Ranking.View(1, now)) {
		t.Fatal(rows, err)
	}
	if store.loads != 4 {
		t.Fatal("atomic fallback missing", store.loads)
	}
	var empty *RankingReadView
	if _, err := empty.Rows(owner, now, time.Minute); !errors.Is(err, ErrNotMember) {
		t.Fatal(err)
	}
	if _, err := empty.MonthlyBoard(now, false, 1); !errors.Is(err, ErrRepositoryUnavailable) {
		t.Fatal(err)
	}
}
