package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/skillswitch"
	"longheng.io/server/internal/platform/config"
)

// 两个新开关的**环境变量默认档**钉子：走真实路径 `optionsFromConfig`（不是自造的 options 字面量）。
//
// 三处默认值必须永远一致，否则"空环境 = 修复档"这句话会静默失效
// （2026-09-28 线上回显 `skill_switch_inventory=project` 就是踩过这个坑）：
//
//	① optionsFromConfig 里 envString 的 fallback 字面量；
//	② 归一化函数的 default 分支（skillswitch.NormalizeLayout / normalizeSkillSwitchCandidatePairsMode）；
//	③ 零值 Service 的兜底（skillSwitchInventoryRecordBodyLayout / skillSwitchCandidatePairsMode）。
func TestOptionsNormalizeSkillSwitchSwitches(t *testing.T) {
	// ① 未设（空值 = envString 的 fallback 分支）⇒ 修复档 v4 + 实验关 + shape_guard。
	t.Setenv(skillSwitchInventoryRecordBodyEnv, "")
	t.Setenv(skillSwitchCandidatePairsEnv, "")
	t.Setenv(skillSwitchInventoryModeEnv, "")
	opts := optionsFromConfig(config.ServiceConfig{})
	service := &Service{options: opts}
	if got := service.skillSwitchInventoryRecordBodyLayout(); got != skillswitch.LayoutV4 {
		t.Fatalf("empty env record body layout = %q, want %q (default must be the fixed tier)",
			got, skillswitch.LayoutV4)
	}
	if opts.skillSwitchInventoryRecordBodyLayout != skillswitch.LayoutV4 {
		t.Fatalf("optionsFromConfig record body layout = %q, want %q",
			opts.skillSwitchInventoryRecordBodyLayout, skillswitch.LayoutV4)
	}
	if got := service.skillSwitchCandidatePairsMode(); got != skillSwitchCandidatePairsOff {
		t.Fatalf("empty env candidate pairs mode = %q, want %q (the experiment must default off)",
			got, skillSwitchCandidatePairsOff)
	}
	if got := service.skillSwitchInventoryMode(); got != skillSwitchInventoryShapeGuardTier {
		t.Fatalf("empty env inventory mode = %q, want %q", got, skillSwitchInventoryShapeGuardTier)
	}

	// ② 显式回退档 v6 与显式实验档 identity12 都必须被原样采纳。
	t.Setenv(skillSwitchInventoryRecordBodyEnv, "v6")
	if got := optionsFromConfig(config.ServiceConfig{}).skillSwitchInventoryRecordBodyLayout; got != skillswitch.LayoutV6 {
		t.Fatalf("v6 record body layout = %q, want %q", got, skillswitch.LayoutV6)
	}
	t.Setenv(skillSwitchInventoryRecordBodyEnv, "typo")
	if got := optionsFromConfig(config.ServiceConfig{}).skillSwitchInventoryRecordBodyLayout; got != skillswitch.LayoutV4 {
		t.Fatalf("unknown record body layout = %q, want the fixed tier %q", got, skillswitch.LayoutV4)
	}
	t.Setenv(skillSwitchCandidatePairsEnv, "identity12")
	if got := optionsFromConfig(config.ServiceConfig{}).skillSwitchCandidatePairsMode; got != skillSwitchCandidatePairsIdentity12 {
		t.Fatalf("identity12 candidate mode = %q, want %q", got, skillSwitchCandidatePairsIdentity12)
	}
	// 已证伪的 family-2 与任何未知取值都必须落到 off（实验不能被"手滑写法"打开）。
	for _, value := range []string{"typo", "family2", "on", "identity13"} {
		t.Setenv(skillSwitchCandidatePairsEnv, value)
		if got := optionsFromConfig(config.ServiceConfig{}).skillSwitchCandidatePairsMode; got != skillSwitchCandidatePairsOff {
			t.Fatalf("candidate mode %q = %q, want %q", value, got, skillSwitchCandidatePairsOff)
		}
	}
}
