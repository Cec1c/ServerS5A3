package guild

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRankingMonthlyBoundaryTieAndMissingHistory(t *testing.T) {
	boundary := time.Date(2026, 11, 1, 6, 0, 0, 0, rankingLocation)
	s := &RankingState{}
	old, err := BuildRankingPeriod(boundary.Add(-time.Second), map[uint32]RankingFactors{1: {Members: 1}, 2: {Members: 1}, 3: {Members: 0.1}, 4: {}})
	if err != nil {
		t.Fatal(err)
	}
	if old.Rows[1].Rank != 1 || old.Rows[2].Rank != 1 || old.Rows[3].Rank != 3 || old.Rows[4].Rank != 0 {
		t.Fatal(old.Rows)
	}
	if err := s.Publish(old); err != nil {
		t.Fatal(err)
	}
	if s.View(1, boundary.Add(-time.Second))[0].Score != 1 || s.View(1, boundary)[0].Score != 0 || s.View(1, boundary)[1].Score != 1 {
		t.Fatal("month boundary does not preserve actual month")
	}
	current, _ := BuildRankingPeriod(boundary, map[uint32]RankingFactors{1: {Adventure: 10.5}})
	if err := s.Publish(current); err != nil {
		t.Fatal(err)
	}
	if rows := s.View(1, boundary); rows[0].Score != 10.5 || rows[1].Score != 1 || rows[2].Score != 0 {
		t.Fatal(rows)
	}
	current.Rows[1] = RankingRow{}
	if s.View(1, boundary)[0].Score != 10.5 {
		t.Fatal("publication retained caller map")
	}
	bytes, _ := json.Marshal(s)
	var loaded RankingState
	if err := json.Unmarshal(bytes, &loaded); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Validate(); err != nil {
		t.Fatal(err)
	}
	if loaded.View(1, boundary) != s.View(1, boundary) {
		t.Fatal("restart changed history")
	}
	// December had no observations. January must not label November as December.
	later, _ := BuildRankingPeriod(boundary.AddDate(0, 2, 0), map[uint32]RankingFactors{1: {Members: 2}})
	if err := loaded.Publish(later); err != nil {
		t.Fatal(err)
	}
	if rows := loaded.View(1, later.SampledAt); rows[1].Score != 0 || rows[2].Score != 10.5 {
		t.Fatal(rows)
	}
	before, _ := json.Marshal(loaded)
	if err := loaded.Publish(old); !errors.Is(err, ErrRankingSnapshotChanged) {
		t.Fatal(err)
	}
	after, _ := json.Marshal(loaded)
	if string(before) != string(after) {
		t.Fatal("old observation overwrote ranking")
	}
}

func TestRankingPublishPersistsAndRejectsChangedGuildSource(t *testing.T) {
	ctx := context.Background()
	repos := guildTestGroup(t, 2)
	reg, g, master, _ := textRegistry(t)
	at := g.UpdatedAt.Add(time.Hour)
	if err := Save(ctx, repos.Settings, reg, at); err != nil {
		t.Fatal(err)
	}
	digest, _ := RankingSourceDigest(reg)
	period, _ := BuildRankingPeriod(at, map[uint32]RankingFactors{g.ID: {Members: 0.2}})
	if err := PublishRanking(ctx, repos.Guild, master, digest, period); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Ranking.View(g.ID, at)[0].Rank != 1 {
		t.Fatal("ranking missing after reload")
	}
	afterDigest, _ := RankingSourceDigest(loaded)
	if digest != afterDigest {
		t.Fatal("computed ranking changed input digest")
	}
	loaded.Guilds[g.ID].Notice = "new notice"
	if err := Save(ctx, repos.Settings, loaded, at); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repos.Settings.Load(ctx, RegistryScope)
	if err := PublishRanking(ctx, repos.Guild, master, digest, period); !errors.Is(err, ErrRankingSnapshotChanged) {
		t.Fatal(err)
	}
	after, _, _ := repos.Settings.Load(ctx, RegistryScope)
	if before.Values[registryStateKey] != after.Values[registryStateKey] {
		t.Fatal("stale sample overwrote concurrent guild write")
	}
	newDigest, _ := RankingSourceDigest(loaded)
	boom := errors.New("late ranking write failure")
	if err := PublishRanking(ctx, lateFailGuildTx{base: repos.Guild, fail: boom}, master, newDigest, period); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	after, _, _ = repos.Settings.Load(ctx, RegistryScope)
	if before.Values[registryStateKey] != after.Values[registryStateKey] {
		t.Fatal("failed ranking publication persisted")
	}
}

func TestRankingStoredScoresAndRanksMustMatchFactors(t *testing.T) {
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, rankingLocation)
	for _, corrupt := range []func(*RankingPeriod){
		func(p *RankingPeriod) { r := p.Rows[1]; r.Score++; p.Rows[1] = r },
		func(p *RankingPeriod) { r := p.Rows[1]; r.Rank++; p.Rows[1] = r },
		func(p *RankingPeriod) { p.Start = p.Start.Add(time.Hour) },
	} {
		p, _ := BuildRankingPeriod(at, map[uint32]RankingFactors{1: {Members: 1}})
		corrupt(p)
		if err := (&RankingState{Current: p}).Validate(); err == nil {
			t.Fatal("accepted corrupt snapshot")
		}
	}
}
