package dnfbridge

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func TestPlayerStatsGoldDetailsLedgerAndLegacy(t *testing.T) {
	s, id, now := playerStatsFixture(t)
	g, _ := s.repositoryGroup()
	ctx := context.Background()
	for i, e := range []repo.ActivityEvent{
		{CharacterID: "2", Metric: repo.ActivityGoldReinforce, Amount: 1000},
		{CharacterID: "2", Metric: repo.ActivityGoldAmplify, Amount: 2000},
		{CharacterID: "2", Metric: repo.ActivityGoldRepair, Amount: 30},
		{CharacterID: "2", Metric: repo.ActivityGoldEnchant, Amount: 9},
		{CharacterID: "10", Metric: repo.ActivityGoldForge, Amount: 77},
		{CharacterID: "10", Metric: repo.ActivityGoldRepair, Amount: 61},
		{CharacterID: "2", Metric: repo.ActivityGoldNPC, Amount: 9999},
	} {
		e.ID = "gold-detail:" + string(rune('a'+i))
		e.AccountID = "a"
		e.OccurredAt = now
		e.Source = repo.GoldActivitySource(e.Metric)
		if err := g.CharacterSettlement.WithinCharacterSettlement(ctx, e.CharacterID, func(tx repo.Group) error { _, err := tx.Activity.Record(ctx, e); return err }); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := s.buildPlayerActivitySnapshotVersion(ctx, id, now, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range snap.Periods {
		want := map[string]int64{repo.ActivityGoldSpent: 3177, repo.ActivityGoldReinforce: 1000, repo.ActivityGoldAmplify: 2000, repo.ActivityGoldForge: 77, repo.ActivityGoldRepair: 91, repo.ActivityGoldEnchant: 9}
		for k, n := range want {
			if p.Metrics[k] != n {
				t.Fatal(p.Key, k, p.Metrics[k], n)
			}
		}
		for _, r := range p.Characters {
			if r.ID == "2" && r.Metrics[repo.ActivityGoldSpent] != 3039 {
				t.Fatal(r)
			}
			if r.ID == "10" && r.Metrics[repo.ActivityGoldSpent] != 138 {
				t.Fatal(r)
			}
		}
	}
	old, err := encodeNativePlayerActivityVersion(snap, true, true)
	if err != nil {
		t.Fatal(err)
	}
	next, err := encodeNativePlayerActivityDetails(snap, true, true, true)
	if err != nil || string(next[:4]) != "PSA4" {
		t.Fatal(err)
	}
	rows := 0
	for _, p := range snap.Periods {
		rows += 1 + len(p.Characters)
	}
	if len(next) != len(old)+rows*40 {
		t.Fatal("金币只增加五项int64", len(next), len(old), rows)
	}
	// 分类被损坏时不能发布不一致的PSA4，旧PSA3忽略未声明的分类字段。
	snap.Periods[0].Metrics[repo.ActivityGoldRepair]++
	if _, err := encodeNativePlayerActivityDetails(snap, true, true, true); err == nil {
		t.Fatal("接受了金币分项与合计不一致")
	}
	again, err := encodeNativePlayerActivityVersion(snap, true, true)
	if err != nil || string(again) != string(old) {
		t.Fatal("旧PSA3布局被改变", err)
	}
	if _, err := encodeNativePlayerActivityDetails(snap, true, false, true); err == nil {
		t.Fatal("金币明细缺少扩展标记")
	}
}

func TestPlayerStatsGoldDetailsCacheVariants(t *testing.T) {
	s, x, activity, c := playerStatsGameFixture(t)
	for i, version := range []byte{'1', '2', '3', '3', '2'} {
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
		if got := string(packet[36:40]); got != "PSA"+string(version+1) {
			t.Fatal("缓存格式串用", i, got)
		}
		want := []int32{3, 6, 9, 9, 12}[i]
		if activity.reads.Load() != want {
			t.Fatal("切换明细增加额外汇总或缓存不命中", i, activity.reads.Load(), want)
		}
	}
}
