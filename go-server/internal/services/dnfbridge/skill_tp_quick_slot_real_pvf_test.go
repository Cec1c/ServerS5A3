package dnfbridge

import (
	"context"
	"os"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillcmd"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 真档回归（固定QA档 Script.pvf，opt-in，与 skill_catalog_real_smoke_test.go 同一读档口径）：
// 逐职业把每支 TP（[feature skill type] / [special purchase cost]）技能当成刚学会，
// 按学习路径重建技能页布局，断言没有 TP 技能被放进普通快捷栏 0..5 / 198..203。
// 每支技能单独构造，避免同职业多支技能互相挤占分组槽后把失败原因混成 ErrSkillSlot。
func TestRealScriptPVFTPSkillsNeverTakeOrdinaryQuickSlots(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to run the real Script.pvf TP quick-slot probe")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := buildSkillCatalogFromSource(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	total, activeTP := 0, 0
	for job := byte(0); job < 32; job++ {
		for _, definition := range catalog.SkillsForJob(job) {
			if !definition.IsTPSkill() {
				continue
			}
			total++
			if definition.Active {
				activeTP++
				t.Logf("job=%d active TP skill=%d %q from the real archive", job, definition.ID, definition.Name)
			}
			states := map[int64]dnfrepo.SkillState{int64(definition.ID): {Level: 1, Enabled: true}}
			layout, err := skillcmd.BuildCurrentSkillLayout(catalog, job, 0, states, nil)
			if err != nil {
				t.Fatalf("job=%d TP skill=%d layout: %v", job, definition.ID, err)
			}
			for slot, skillID := range layout {
				if (slot >= 0 && slot < 6) || (slot >= 198 && slot < 204) {
					t.Fatalf("job=%d TP skill=%d (%s) was auto-placed in ordinary quick slot %d: %v", job, skillID, definition.Name, slot, layout)
				}
			}
		}
	}
	if total == 0 {
		t.Fatal("real archive exposed no TP skill; the probe lost its target")
	}
	t.Logf("real archive TP skills=%d active=%d; none placed in 0..5/198..203", total, activeTP)
}
