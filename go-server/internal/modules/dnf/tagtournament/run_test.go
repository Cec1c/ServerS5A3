package tagtournament

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestRunRequiresOrderedBossAndExplicitSpecialDecision(t *testing.T) {
	o, _, now := ownerFixture(t)
	r := admitRun(t, o, Normal)
	if r.BattleLocked() {
		t.Fatal("休息室不应锁装备技能")
	}
	*now = now.Add(time.Second)
	if _, _, err := r.BeginNext(*now, true); err == nil {
		t.Fatal("未打普通关直接进王")
	}
	e, restore, err := r.BeginNext(*now, false)
	if err != nil || restore || e.Optional || !r.BattleLocked() {
		t.Fatal(e, restore, err)
	}
	if _, _, err = r.BeginNext(*now, false); err == nil {
		t.Fatal("当前回合未结束就推进")
	}
	if err = r.ConfirmBossDefeated(*now, e.MapID+1, e.MonsterID); err == nil {
		t.Fatal("不同地图推进")
	}
	if err = r.ConfirmBossDefeated(*now, e.MapID, e.MonsterID+1); err == nil {
		t.Fatal("不同怪物推进")
	}
	if err = r.ConfirmBossDefeated(*now, e.MapID, e.MonsterID); err != nil {
		t.Fatal(err)
	}
	if err = r.ConfirmBossDefeated(*now, e.MapID, e.MonsterID); err == nil {
		t.Fatal("重复死亡推进")
	}
	for i := 0; i < 3; i++ {
		clearRound(t, r, now)
	}
	if r.Snapshot().Phase != SpecialChoice || !r.BattleLocked() {
		t.Fatal("王房选择阶段错误")
	}
	*now = now.Add(time.Second)
	e, restore, err = r.BeginNext(*now, true)
	if err != nil || !restore || !e.Optional {
		t.Fatal("王房入口未恢复", e, restore, err)
	}
	if err = r.ConfirmBossDefeated(*now, e.MapID, e.MonsterID); err != nil {
		t.Fatal(err)
	}
	if r.Snapshot().Phase != Finished || r.Snapshot().Cleared != 5 {
		t.Fatal("王房完成状态错误")
	}
	if _, _, err = r.BeginNext(*now, false); err == nil {
		t.Fatal("结束后再进关")
	}
}

func TestRunTagDeathCooldownAndSpecialDoesNotRevive(t *testing.T) {
	o, _, now := ownerFixture(t)
	r := admitRun(t, o, Normal)
	if err := r.Tag(*now, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.Tag(*now, 0); err == nil {
		t.Fatal("无视5秒切人冷却")
	}
	*now = now.Add(5 * time.Second)
	if err := r.Tag(*now, 0); err != nil {
		t.Fatal(err)
	}
	e, _, err := r.BeginNext(*now, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.ApplyVitals(*now, 0, 0, 25); err != nil {
		t.Fatal(err)
	}
	if err = r.ApplyVitals(*now, 0, 1, 25); err == nil {
		t.Fatal("同步把死亡角色复活")
	}
	if _, err = r.UseConsumable(*now, 0); err == nil {
		t.Fatal("死亡角色使用药物")
	}
	*now = now.Add(5 * time.Second)
	if err = r.Tag(*now, 1); err != nil {
		t.Fatal(err)
	}
	if err = r.ApplyVitals(*now, 0, 500, 500); err == nil {
		t.Fatal("修改备用角色")
	}
	if err = r.ApplyVitals(*now, 1, 100, 20); err != nil {
		t.Fatal(err)
	}
	if err = r.ConfirmBossDefeated(*now, e.MapID, e.MonsterID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		clearRound(t, r, now)
	}
	*now = now.Add(time.Second)
	if _, restore, err := r.BeginNext(*now, true); err != nil || !restore {
		t.Fatal(err)
	}
	s := r.Snapshot()
	if s.Vitals[0].HP != 0 || s.Vitals[0].MP != 25 || s.Vitals[1].HP != 2000 || s.Vitals[1].MP != 1000 {
		t.Fatal("王房复活死者或未补满存活队员", s)
	}
	if err = r.Tag(*now, 0); err == nil {
		t.Fatal("切入死亡角色")
	}
	if err = r.ApplyVitals(*now, 1, 0, 0); err != nil {
		t.Fatal(err)
	}
	if r.Snapshot().Phase != Defeated {
		t.Fatal("双死后未结束")
	}
	if _, _, err = r.BeginNext(*now, false); err == nil {
		t.Fatal("双死后仍能战斗")
	}
}

func TestRunConsumableUsesPVFAndCooldownDoesNotConsumeInventory(t *testing.T) {
	o, _, now := ownerFixture(t)
	o.catalog.consumables[1].RecoverySection = "mp recovery"
	r := admitRun(t, o, Hard)
	if _, err := r.UseConsumable(*now, 0); err == nil {
		t.Fatal("休息室直接使用战斗道具")
	}
	if _, _, err := r.BeginNext(*now, false); err != nil {
		t.Fatal(err)
	}
	if err := r.ApplyVitals(*now, 0, 100, 25); err != nil {
		t.Fatal(err)
	}
	if _, err := r.UseConsumable(*now, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := r.UseConsumable(*now, 1); err != nil {
		t.Fatal(err)
	}
	if v := r.Snapshot().Vitals[0]; v.HP != 400 || v.MP != 175 {
		t.Fatal("未按30%最大值恢复", v)
	}
	if _, err := r.UseConsumable(*now, 0); err == nil {
		t.Fatal("忽略20秒冷却")
	}
	*now = now.Add(20 * time.Second)
	if _, err := r.UseConsumable(*now, 0); err != nil {
		t.Fatal(err)
	}
	if v := r.Snapshot().Vitals[0]; v.HP != 700 {
		t.Fatal(v)
	}
	*now = now.Add(20 * time.Second)
	if _, err := r.UseConsumable(*now, 0); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(20 * time.Second)
	if _, err := r.UseConsumable(*now, 0); err != nil {
		t.Fatal(err)
	}
	if v := r.Snapshot().Vitals[0]; v.HP != v.MaxHP {
		t.Fatal("恢复溢出", v)
	}
}

func TestRunClockAndVitalBounds(t *testing.T) {
	o, _, now := ownerFixture(t)
	r := admitRun(t, o, Hard)
	if _, _, err := r.BeginNext(now.Add(-time.Second), false); err == nil {
		t.Fatal("时钟回退")
	}
	if _, _, err := r.BeginNext(*now, false); err != nil {
		t.Fatal(err)
	}
	for _, v := range [][2]int64{{-1, 0}, {1001, 0}, {10, -1}, {10, 501}} {
		if err := r.ApplyVitals(*now, 0, v[0], v[1]); err == nil {
			t.Fatal("非法生命魔法", v)
		}
	}
	*now = time.Date(2026, 10, 20, 6, 0, 0, 0, beijing)
	if err := r.Tag(*now, 1); err == nil {
		t.Fatal("跨日旧挑战仍切人")
	}
	if _, err := r.UseConsumable(*now, 0); err == nil {
		t.Fatal("跨日旧挑战仍用物品")
	}
	if err := r.Abandon(*now); err == nil {
		t.Fatal("跨日旧挑战仍可写入状态")
	}
}

func TestRunRecoveryNearInt64MaximumDoesNotOverflow(t *testing.T) {
	o, _, now := ownerFixture(t)
	a, err := o.Admit(context.Background(), "a", "1", [2]string{"1", "2"}, Hard)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRun(a, [2]Vitals{{1, math.MaxInt64, 1, math.MaxInt64}, {1, math.MaxInt64, 1, math.MaxInt64}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.BeginNext(*now, false); err != nil {
		t.Fatal(err)
	}
	if _, err = r.UseConsumable(*now, 0); err != nil {
		t.Fatal(err)
	}
	if v := r.Snapshot().Vitals[0]; v.HP <= 1 || v.HP > v.MaxHP {
		t.Fatal("恢复乘法溢出", v)
	}
}
