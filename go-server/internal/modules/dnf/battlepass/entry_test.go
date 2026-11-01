package battlepass

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestBattlePassEntryReadOnlyProjection(t *testing.T) {
	g, c, p, cmd := fixture(t)
	a, _, _ := g.Account.Load(nil, "a")
	before, _ := json.Marshal(a)
	projected, e := Inspect(c, p, a, cmd)
	if e != nil {
		t.Fatal(e)
	}
	after, _ := json.Marshal(a)
	if string(before) != string(after) {
		t.Fatal("只读投影改了传入账号")
	}
	actual := query(t, g, c, p, cmd)
	if !reflect.DeepEqual(projected.State, actual.State) || !reflect.DeepEqual(projected.Missions, actual.Missions) {
		t.Fatal("入口与业务窗口计算不一致")
	}
	cmd.Now = cmd.Now.Add(8 * 24 * time.Hour)
	a, _, _ = g.Account.Load(nil, "a")
	projected, e = Inspect(c, p, a, cmd)
	if e != nil {
		t.Fatal(e)
	}
	stored, _, _ := g.Account.Load(nil, "a")
	if !reflect.DeepEqual(a, stored) {
		t.Fatal("入口跨日写了账号记录")
	}
	actual = query(t, g, c, p, cmd)
	if !reflect.DeepEqual(projected.State, actual.State) || !reflect.DeepEqual(projected.Missions, actual.Missions) {
		t.Fatal("跨周投影与实际刷新不同")
	}
}
func TestBattlePassEntryClaimableBoundaries(t *testing.T) {
	_, c, _, _ := fixture(t)
	v := Snapshot{State: State{Level: 1}}
	c.Rewards[0][1] = nil
	if CanClaim(c, v) {
		t.Fatal("高级锁定或空奖励误提醒")
	}
	v.Missions[0].Flags = 1
	if !CanClaim(c, v) {
		t.Fatal("任务可领未提醒")
	}
	for _, flags := range []uint16{0, 3, 5, 9} {
		v.Missions[0].Flags = flags
		if CanClaim(c, v) {
			t.Fatalf("任务状态%d误提醒", flags)
		}
	}
	v.Missions[0].Flags = 1
	v.State.Level = 80
	if CanClaim(c, v) {
		t.Fatal("满级任务不可领经验")
	}
	v.Missions[0].Flags = 0
	v.State.Level = 1
	v.State.Premium = true
	if !CanClaim(c, v) {
		t.Fatal("高级可领未提醒")
	}
	v.State.RewardClaimed[1][0] = 1
	if CanClaim(c, v) {
		t.Fatal("已领取仍提醒")
	}
	c.Rewards[0][2] = []Reward{{15, 1}}
	if CanClaim(c, v) {
		t.Fatal("未达级误提醒")
	}
	v.State.Level = 2
	if !CanClaim(c, v) {
		t.Fatal("达级奖励未提醒")
	}
}
