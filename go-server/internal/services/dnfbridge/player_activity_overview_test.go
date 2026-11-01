package dnfbridge

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"os"
	"testing"
	"time"
)

func TestPlayerStatsOverviewSharedLedgerAndAdventureCumulative(t *testing.T) {
	s, id, now := playerStatsFixture(t)
	g, _ := s.repositoryGroup()
	ctx := context.Background()
	if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: "a", RepresentAccountName: "测试冒险团"}); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]int64{"2": 1234, "10": 2, "7": 999} {
		ch, _, _ := g.Character.Load(ctx, key)
		ch = repo.CloneCharacter(ch)
		if ch.Stats == nil {
			ch.Stats = map[string]int64{}
		}
		ch.Stats[repo.ActivityRecommendedDungeon] = value
		if err := g.Character.Save(ctx, ch); err != nil {
			t.Fatal(err)
		}
	}
	err := g.CharacterSettlement.WithinCharacterSettlement(ctx, "2", func(tx repo.Group) error {
		for _, e := range []repo.ActivityEvent{
			{ID: "overview:colorless", AccountID: "a", CharacterID: "2", Metric: repo.ActivityColorless, Amount: 12, OccurredAt: now, DungeonID: 310, ItemID: 3037, Source: "skill"},
			{ID: "overview:repair", AccountID: "a", CharacterID: "2", Metric: repo.ActivityGoldRepair, Amount: 123, OccurredAt: now, Source: "repair"},
			{ID: "overview:npc", AccountID: "a", CharacterID: "2", Metric: repo.ActivityGoldNPC, Amount: 9999, OccurredAt: now, Source: "npc_purchase"},
			{ID: "overview:rec1", AccountID: "a", CharacterID: "2", Metric: repo.ActivityRecommendedDungeon, Amount: 1, OccurredAt: now, DungeonID: 310, Source: "recommended_clear"},
			{ID: "overview:rec2", AccountID: "a", CharacterID: "2", Metric: repo.ActivityRecommendedDungeon, Amount: 1, OccurredAt: now, DungeonID: 310, Source: "recommended_clear"},
		} {
			if _, err := tx.Activity.Record(ctx, e); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.buildPlayerActivitySnapshotVersion(ctx, id, now, true)
	if err != nil {
		t.Fatal(err)
	}
	if snap.AdventureName != "测试冒险团" {
		t.Fatal(snap.AdventureName)
	}
	for i, p := range snap.Periods {
		want := int64(2)
		if i == 2 {
			want = 1236
		}
		if p.Metrics[repo.ActivityRecommendedDungeon] != want || p.Metrics[repo.ActivityColorless] != 12 || p.Metrics[repo.ActivityGoldSpent] != 123 {
			t.Fatal(i, p.Metrics)
		}
		for _, r := range p.Characters {
			if r.ID == "2" && (r.Metrics[repo.ActivityColorless] != 12 || r.Metrics[repo.ActivityGoldSpent] != 123) {
				t.Fatal(r)
			}
			if r.ID == "7" && i == 2 && r.Metrics[repo.ActivityRecommendedDungeon] != 0 {
				t.Fatal("已删除角色误入冒险团实时累计", r)
			}
		}
	}
	encoded, err := encodeNativePlayerActivityVersion(snap, true, true)
	if err != nil || string(encoded[:4]) != "PSA3" {
		t.Fatal(err)
	}
	if path := os.Getenv("DNF_TEST_OVERVIEW_FIXTURE"); path != "" {
		if err := os.WriteFile(path, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
	old, err := s.buildPlayerActivitySnapshot(ctx, id, now)
	if err != nil {
		t.Fatal(err)
	}
	if old.AdventureName != "" || len(old.Periods[0].Metrics) != 5 {
		t.Fatal("旧统计快照被改写", old)
	}
	legacy, err := encodeNativePlayerActivityWithSources(old, true)
	if err != nil || string(legacy[:4]) != "PSA2" {
		t.Fatal(err)
	}
}

func TestPlayerStatsOverviewGameCacheSeparatesWireVersions(t *testing.T) {
	s, x, activity, c := playerStatsGameFixture(t)
	for i, version := range []byte{'2', '1', '1', '2'} {
		playerStatsEdit(t, s, x, func() {
			x.playerStats.last = time.Time{}
			b := playerStatsRequestBytes(uint32(i + 1))
			b[3] = version
			if err := s.handlePlayerStatsGame(x, b); err != nil {
				t.Fatal(err)
			}
		})
		packet := playerStatsPacket(t, c)
		playerStatsWaitIdle(t, x)
		want := "PSA2"
		if version == '2' {
			want = "PSA3"
		}
		if string(packet[36:40]) != want {
			t.Fatal("缓存串用统计版本", i, string(packet[36:40]))
		}
		wantReads := int32(3)
		if i == 1 || i == 2 {
			wantReads = 6
		}
		if i == 3 {
			wantReads = 9
		}
		if activity.reads.Load() != wantReads {
			t.Fatal(i, activity.reads.Load())
		}
	}
}

func TestPlayerStatsOverviewRejectsInvalidGoldTotals(t *testing.T) {
	for _, v := range []map[string]int64{{repo.ActivityGoldRepair: -1}, {repo.ActivityGoldRepair: math.MaxInt64, repo.ActivityGoldReinforce: 1}, {repo.ActivityColorless: -1}, {repo.ActivityRecommendedDungeon: -1}} {
		if _, err := playerActivityOverviewMetrics(v); err == nil {
			t.Fatal(v)
		}
	}
}
