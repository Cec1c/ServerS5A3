package fiendwar

import (
	"testing"
	"time"
)

func TestMerchantVisibilityExpiresAtScriptDuration(t *testing.T) {
	b := &FiendWarBattle{Config: &FiendWarBattleConfig{MerchantPositions: []int{6}, MerchantRates: []FiendWarMerchantRate{{Kind: 1, DurationSeconds: 60, Weight: 100000}}}}
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	b.act(FiendWarInstruction{Op: "create merchant"}, now)
	if b.MerchantArea != 6 || b.MerchantDuration != time.Minute {
		t.Fatal("商人秒数与权重读反", b)
	}
	clone := b.Clone()
	if err := b.Tick(now.Add(59 * time.Second)); err != nil || b.MerchantArea != 6 {
		t.Fatal("商人提前消失", err)
	}
	if err := b.Tick(now.Add(time.Minute)); err != nil || b.MerchantArea != 0 {
		t.Fatal("商人到期仍显示", err)
	}
	if clone.MerchantArea != 6 || !clone.MerchantExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatal("商人投影快照被修改")
	}
	b.act(FiendWarInstruction{Op: "create merchant"}, now.Add(2*time.Minute))
	if b.MerchantArea != 6 || !b.MerchantExpiresAt.Equal(now.Add(3*time.Minute)) {
		t.Fatal("脚本再次创建未产生新有效期")
	}
}
