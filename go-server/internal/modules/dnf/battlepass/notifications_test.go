package battlepass

import "testing"

func TestBattlePassNotificationSharedMask(t *testing.T) {
	c := &Catalog{}
	v := Snapshot{State: State{Level: 1}}
	if ClaimNotifications(c, v) != 0 {
		t.Fatal("空任务奖励提醒")
	}
	for i, want := range map[int]byte{0: 2, 3: 2, 4: 4, 7: 4, 8: 8, 25: 8} {
		v.Missions[i].Flags = 1
		if got := ClaimNotifications(c, v); got != want {
			t.Fatal("类别错误", i, got, want)
		}
		v.Missions[i].Flags = 3
		if ClaimNotifications(c, v) != 0 {
			t.Fatal("领取后未清除")
		}
		v.Missions[i].Flags = 0
	}
	v.Missions[0].Flags = 1
	v.Missions[4].Flags = 1
	v.Missions[8].Flags = 1
	c.Rewards[1][1] = []Reward{{ItemID: 15, Count: 1}}
	if ClaimNotifications(c, v) != 14 {
		t.Fatal("高级锁定误提醒")
	}
	v.State.Premium = true
	if ClaimNotifications(c, v) != 30 {
		t.Fatal("组合提醒不符")
	}
	v.State.Level = 80
	v.State.RewardClaimed[1][0] = 1
	if ClaimNotifications(c, v) != 0 {
		t.Fatal("满级或已领仍提醒")
	}
}
