package pvp

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/inventory"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func dailyCompleteFixture(t *testing.T, kind string, target uint32, parameter int64) (repo.Group, *Catalog, time.Time) {
	t.Helper()
	r := memory.NewMemoryGroup()
	c := testCatalog(t)
	c.Pools[0][0].Emblems, c.Pools[1][0].Emblems = 0, 0
	c.Pools[2] = []Mission{{ID: 133, Tier: 2, Weight: 10, RankMax: 30, Condition: kind, Target: target, Parameter: parameter, Emblems: 100}}
	if err := r.Character.Save(context.Background(), repo.CharacterRecord{AccountID: "a", CharacterID: "29"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	return r, c, now
}

func TestDailyStreakGroupsResetLossReplayAndServiceDay(t *testing.T) {
	r, c, now := dailyCompleteFixture(t, "winning streak", 2, 3)
	ctx := context.Background()
	// PVF 131: 两组三连胜。输/平清空未完成组，不借用历史全局连胜。
	wins := []bool{true, true, false, true, true, true, true, true, true}
	want := []uint32{0, 0, 0, 0, 0, 1, 1, 1, 2}
	for i, win := range wins {
		m := Match{Key: fmt.Sprint(i), StartedAt: now.Add(time.Duration(i) * time.Minute), CompletedAt: now.Add(time.Duration(i)*time.Minute + time.Second), Won: win}
		for retry := 0; retry < 2; retry++ {
			state, err := Complete(ctx, r, c, "a", "29", m)
			if err != nil || state.Rows[2].Progress != want[i] {
				t.Fatalf("第%d场重试%d: %+v %v", i, retry, state.Rows, err)
			}
			if state.Character.Stats[EmblemsKey] != map[bool]int64{false: 0, true: 100}[i == len(wins)-1] {
				t.Fatal("连胜奖励重复或提前发放", state.Character.Stats)
			}
		}
	}
	reset, err := Prepare(ctx, r, c, "a", "29", now.Add(24*time.Hour))
	if err != nil || reset.Rows[2].Progress != 0 || reset.Character.Stats[dailyKey(2, "streak")] != 0 {
		t.Fatal("跨日保留了昨日连胜进度", err)
	}
	m := Match{Key: "manual", StartedAt: now.Add(24 * time.Hour), CompletedAt: now.Add(24*time.Hour + time.Second), Won: true, Manual: true}
	state, err := Complete(ctx, r, c, "a", "29", m)
	if err != nil || state.Character.Stats[dailyKey(2, "streak")] != 0 {
		t.Fatal("手动房推进每日连胜", err)
	}
}

func TestDailyPoseCommitsOnePosePerRankedWinAndOnePVFRound(t *testing.T) {
	r, c, now := dailyCompleteFixture(t, "win pose", 1, 3)
	ctx := context.Background()
	for i, pose := range []byte{0, 0, 1, 2} {
		m := Match{Key: fmt.Sprint("pose", i), StartedAt: now.Add(time.Duration(i) * time.Minute), CompletedAt: now.Add(time.Duration(i)*time.Minute + time.Second), Won: true}
		if _, err := Complete(ctx, r, c, "a", "29", m); err != nil {
			t.Fatal(err)
		}
		p := WinPose{MatchKey: m.Key, MissionID: 133, Pose: pose, StartedAt: m.StartedAt, CompletedAt: m.CompletedAt, ReportedAt: m.CompletedAt.Add(time.Second)}
		before, _, _ := r.Character.Load(ctx, "29")
		bad := r
		bad.CharacterSettlement = failCommit{r.CharacterSettlement}
		if _, err := RecordWinPose(ctx, bad, c, "a", "29", p); err == nil {
			t.Fatal("姿态事务失败未上报")
		}
		after, _, _ := r.Character.Load(ctx, "29")
		if !reflect.DeepEqual(before, after) {
			t.Fatal("姿态失败泄露计数或印章")
		}
		got, err := RecordWinPose(ctx, r, c, "a", "29", p)
		if err != nil || !got.Accepted || got.Rows[2].Progress != map[bool]uint32{false: 0, true: 1}[i == 3] {
			t.Fatalf("姿态计数: %+v %v", got, err)
		}
		p.Pose = (pose + 1) % 3
		retry, err := RecordWinPose(ctx, r, c, "a", "29", p)
		if err != nil || !retry.Replayed || retry.Accepted || !reflect.DeepEqual(got.Character.Stats, retry.Character.Stats) {
			t.Fatal("同场更换姿态重复累计", err)
		}
	}
	ch, _, _ := r.Character.Load(ctx, "29")
	if ch.Stats[EmblemsKey] != 100 || ch.Stats[dailyKey(2, "paid")] != 1 {
		t.Fatal("PVF一轮未发且仅发100印章", ch.Stats)
	}
}

func TestDailyPoseRejectsLoserManualWrongMatchAndCrossDay(t *testing.T) {
	for _, scenario := range []string{"loser", "manual", "wrong_match", "cross_day", "old_pose"} {
		t.Run(scenario, func(t *testing.T) {
			r, c, now := dailyCompleteFixture(t, "win pose", 1, 3)
			m := Match{Key: "identity", StartedAt: now, CompletedAt: now.Add(time.Second), Won: scenario != "loser", Manual: scenario == "manual"}
			if scenario == "cross_day" {
				m.StartedAt = now.Add(-24 * time.Hour)
			}
			ctx := context.Background()
			if _, err := Complete(ctx, r, c, "a", "29", m); err != nil {
				t.Fatal(err)
			}
			p := WinPose{MatchKey: m.Key, MissionID: 133, Pose: 0, StartedAt: m.StartedAt, CompletedAt: m.CompletedAt, ReportedAt: m.CompletedAt.Add(time.Second)}
			if scenario == "wrong_match" {
				p.MatchKey = "other"
			}
			if scenario == "old_pose" {
				p.ReportedAt = m.CompletedAt.Add(31 * time.Second)
			}
			before, _, _ := r.Character.Load(ctx, "29")
			got, _ := RecordWinPose(ctx, r, c, "a", "29", p)
			after, _, _ := r.Character.Load(ctx, "29")
			if got.Accepted || !reflect.DeepEqual(before.Stats, after.Stats) {
				t.Fatal("无效姿态更改进度")
			}
		})
	}
}

func TestDailyItemRewardBacklogSurvivesFullBagResetAndCommitFailure(t *testing.T) {
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	c, err := Load(fourthDailySource())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	if err := r.Character.Save(ctx, repo.CharacterRecord{AccountID: "a", CharacterID: "29"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "29"}); err != nil {
		t.Fatal(err)
	}
	state, err := Prepare(ctx, r, c, "a", "29", now)
	if err != nil {
		t.Fatal(err)
	}
	state.Character.Stats[dailyKey(3, "progress")] = 3
	if err := r.Character.Save(ctx, state.Character); err != nil {
		t.Fatal(err)
	}
	full := func(repo.InventoryRecord, int64, int64, time.Time) (repo.InventoryRecord, error) {
		return repo.InventoryRecord{}, ErrTotalRewardInventoryFull
	}
	for retry := 0; retry < 2; retry++ {
		pending, err := GrantDailyItems(ctx, r, c, "a", "29", now, full)
		if err != nil || !reflect.DeepEqual(pending.Pending, []uint16{134}) || pending.Character.Stats[dailyPendingKey(134)] != 100 || pending.Character.Stats[dailyKey(3, "paid")] != 0 {
			t.Fatal("背包满丢奖励或重复登记", pending, err)
		}
	}
	reset, err := Prepare(ctx, r, c, "a", "29", now.Add(24*time.Hour))
	if err != nil || reset.Rows[3].Progress != 0 || reset.Character.Stats[dailyPendingKey(134)] != 100 {
		t.Fatal("跨日丢失待发奖励", err)
	}
	place := func(inv repo.InventoryRecord, id, n int64, _ time.Time) (repo.InventoryRecord, error) {
		return inventory.PlaceStackableReward(inv, alignedcmd.MagicBoxRewardItem{ItemID: id, Kind: "stackable", StackLimit: 1000, SlotStart: 121, SlotEnd: 122, PVFPath: "isolated.stk"}, n)
	}
	bad := r
	bad.CharacterSettlement = failCommit{r.CharacterSettlement}
	if _, err := GrantDailyItems(ctx, bad, c, "a", "29", now.Add(24*time.Hour), place); err == nil {
		t.Fatal("物品发放事务失败未上报")
	}
	inv, _, _ := r.Inventory.Load(ctx, "29")
	if len(inv.Slots) != 0 {
		t.Fatal("事务失败泄露物品")
	}
	for retry := 0; retry < 2; retry++ {
		got, err := GrantDailyItems(ctx, r, c, "a", "29", now.Add(24*time.Hour), place)
		if err != nil || got.Items[121].ItemID != 10092947 || got.Items[121].Count != 100 || got.Character.Stats[dailyPendingKey(134)] != 0 || len(got.Granted) != 1-retry {
			t.Fatalf("跨日补发/重试: %+v %v", got, err)
		}
	}
	if _, err := GrantDailyItems(ctx, r, c, "other", "29", now.Add(24*time.Hour), place); err == nil {
		t.Fatal("跨账号领取")
	}
}
