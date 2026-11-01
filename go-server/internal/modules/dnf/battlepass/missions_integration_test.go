package battlepass

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/disjoint"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

// 从实际合成资产事务到账号共享快照，再领取经验；失败和重试均不得多算。
func TestBattlePassEmblemGameplayToClaimAndRefresh(t *testing.T) {
	g, c, p, cmd := fixture(t)
	ctx := context.Background()
	c.Missions[1] = Mission{1, 1, 100, 19, "合成徽章", -1}
	cmd.Counters = nil
	if v := query(t, g, c, p, cmd); v.Missions[0].Flags&1 != 0 {
		t.Fatal("未合成便可完成")
	}
	if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{"0:5": {ItemID: 100, Count: 2}}}); err != nil {
		t.Fatal(err)
	}
	owner, _ := disjoint.NewOwner(g)
	project := func(a *disjoint.Assets) (disjoint.Changes, error) {
		if a.Inventory.Slots["0:5"].Count < 2 {
			return disjoint.Changes{}, errors.New("徽章不足")
		}
		delete(a.Inventory.Slots, "0:5")
		a.Inventory.Slots["0:6"] = repo.ItemStack{ItemID: 101, Count: 1}
		return disjoint.Changes{Inventory: true, EmblemUpgradeFrom: 2}, nil
	}
	bad := disjoint.Command{AccountID: "a", CharacterID: "1", UpdatedAt: cmd.Now, Project: func(a *disjoint.Assets) (disjoint.Changes, error) {
		_, _ = project(a)
		return disjoint.Changes{}, errors.New("模拟产物保存失败")
	}}
	if err := owner.CompoundEmblems(ctx, bad); err == nil {
		t.Fatal("未拒绝失败")
	}
	check := func(want uint32) {
		t.Helper()
		chars, _ := g.Character.ListByAccount(ctx, "a", 10)
		cmd.Counters = Counters(chars, repo.ActivityDay(p.Start), repo.ActivityDay(cmd.Now))
		v := query(t, g, c, p, cmd)
		if v.Missions[0].Progress != want {
			t.Fatal(v.Missions[0])
		}
	}
	check(0)
	good := disjoint.Command{AccountID: "a", CharacterID: "1", UpdatedAt: cmd.Now, Project: project}
	if err := owner.CompoundEmblems(ctx, good); err != nil {
		t.Fatal(err)
	}
	check(1)
	if err := owner.CompoundEmblems(ctx, good); err == nil {
		t.Fatal("重复消耗未拒绝")
	}
	check(1)
	cmd.CharacterID = "2"
	check(1)
	cmd = ready(cmd, p)
	cmd.Action = 1
	cmd.Target = 1
	v := query(t, g, c, p, cmd)
	if v.State.XP != 100 || v.Missions[0].Flags&2 == 0 {
		t.Fatal("完成未加经验", v.State)
	}
	if _, err := Execute(ctx, g, c, p, cmd); !errors.Is(err, ErrClaim) {
		t.Fatal("重复完成", err)
	}
	cmd.Action = 0
	cmd.Now = cmd.Now.Add(24 * time.Hour)
	check(0)
	if v := query(t, g, c, p, cmd); v.State.XP != 100 {
		t.Fatal("每日刷新重置赛季经验")
	}
}

func TestBattlePassNewMissionKindsAndExclusiveRemainBlocked(t *testing.T) {
	for _, kind := range []uint32{1, 2, 5, 6, 9, 12, 19, 20, 21, 22, 25, 31, 41, 42} {
		if !Supported(kind) {
			t.Fatal("缺少计数支持", kind)
		}
	}
	for _, kind := range []uint32{14, 37, 46} {
		if Supported(kind) {
			t.Fatal("未排程或未知玩法被开放", kind)
		}
	}
}
