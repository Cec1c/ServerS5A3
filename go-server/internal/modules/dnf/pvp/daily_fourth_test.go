package pvp

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fourthDailySource() textSource {
	s := dailyTestSource()
	s["etc/missionsystem.etc"] += "[casual daily mission]\n3 134 10\n[/casual daily mission]\n"
	s["pvp_mission/mission.lst"] += "134 `daily/fourth.msn`\n"
	s["pvp_mission/daily/fourth.msn"] = "[grade] `[daily]` 3\n[rank range] 0 30\n[condition] `[colosseum win]` `[all]` 3 1\n[/condition]\n[reward item] 10092947 100\n[/reward item]\n"
	return s
}

func TestFourthDailyCardPreservesExistingDayAndItemReward(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	r := memory.NewMemoryGroup()
	if err := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: "29", AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	old, err := Prepare(ctx, r, testCatalog(t), "a", "29", now)
	if err != nil {
		t.Fatal(err)
	}
	old.Character.Stats[dailyKey(0, "progress")] = 1
	if err := r.Character.Save(ctx, old.Character); err != nil {
		t.Fatal(err)
	}
	c, err := Load(fourthDailySource())
	if err != nil {
		t.Fatal(err)
	}
	m := c.Pools[3][0]
	if m.ID != 134 || m.Target != 3 || m.Condition != "colosseum win" || m.RewardItemID != 10092947 || m.RewardItemCount != 100 || m.Emblems != 0 {
		t.Fatalf("第四卡未保留物品奖励: %+v", m)
	}
	state, err := Prepare(ctx, r, c, "a", "29", now)
	if err != nil || len(state.Rows) != 4 || state.Rows[3] != (Row{ID: 134, Target: 3}) {
		t.Fatalf("第四张卡未分配: %+v, %v", state.Rows, err)
	}
	for tier := 0; tier < 3; tier++ {
		if state.Rows[tier].ID != old.Rows[tier].ID || state.Character.Stats[dailyKey(tier, "progress")] != old.Character.Stats[dailyKey(tier, "progress")] {
			t.Fatal("补第四卡重置了原三张")
		}
	}
	restored, err := Prepare(ctx, r, c, "a", "29", now.Add(time.Minute))
	if err != nil || !reflect.DeepEqual(state.Rows, restored.Rows) {
		t.Fatal("重登丢失第四卡")
	}
	for i := 0; i < 3; i++ {
		state, err = Complete(ctx, r, c, "a", "29", Match{Key: "ordinary-" + string(rune('0'+i)), StartedAt: now, CompletedAt: now.Add(time.Duration(i+1) * time.Minute), Won: true})
		if err != nil || state.Rows[3].Progress != 0 || state.Character.Stats[dailyKey(3, "paid")] != 0 {
			t.Fatalf("普通胜利错误推进斗兽场: %+v, %v", state.Rows, err)
		}
	}
	state.Character.Stats[dailyKey(3, "progress")] = 3
	if err := r.Character.Save(ctx, state.Character); err != nil {
		t.Fatal(err)
	}
	completed, err := Prepare(ctx, r, c, "a", "29", now.Add(5*time.Minute))
	if err != nil || completed.Character.Stats[dailyKey(3, "paid")] != 0 {
		t.Fatal("物品奖励被印章结算误记为已发奖")
	}
	reset, err := Prepare(ctx, r, c, "a", "29", now.Add(24*time.Hour))
	if err != nil || reset.Rows[3].Progress != 0 || reset.Character.Stats[dailyKey(3, "paid")] != 0 {
		t.Fatal("第四卡跨日未重置")
	}
}

func TestFourthDailyRejectsInvalidRewardAndRollsBackAssignment(t *testing.T) {
	s := fourthDailySource()
	s["pvp_mission/daily/fourth.msn"] = strings.ReplaceAll(s["pvp_mission/daily/fourth.msn"], "10092947 100", "0 100")
	if _, err := Load(s); err == nil {
		t.Fatal("接受无效物品奖励")
	}
	r := memory.NewMemoryGroup()
	ctx := context.Background()
	character := repo.CharacterRecord{CharacterID: "29", AccountID: "a"}
	if err := r.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	c, err := Load(fourthDailySource())
	if err != nil {
		t.Fatal(err)
	}
	failed := r
	failed.CharacterSettlement = failCommit{r.CharacterSettlement}
	if _, err := Prepare(ctx, failed, c, "a", "29", time.Now()); err == nil {
		t.Fatal("忽略分配事务失败")
	}
	after, _, err := r.Character.Load(ctx, "29")
	if err != nil || !reflect.DeepEqual(after.Stats, character.Stats) {
		t.Fatal("失败事务写入第四卡")
	}
}
