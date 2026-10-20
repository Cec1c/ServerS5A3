package dnfbridge

// 「第五轮边界修法已移除」这条不变量的钉子。
//
// 为什么需要一个测试而不是只留注释：这次删掉的是一个**曾经能发下行**的开关。
// 只靠 env 关掉的做法留过一个地雷 —— 环境重置会让它静默复活（详见
// `_dist/buff-swap-equip-persistence.md` §15.2/§19.4）。本文件把
// "**任何 env 取值都不能开启行为**"钉成可执行的判据，这样以后无论谁改这个键、
// 或者把 env 清空，这条性质都不会被无声破坏。

import (
	"os"
	"testing"
)

// TestLoginSkillSwitchLoadoutAfterCommitIsRemovedForEveryValue 是本文件的主用例：
// **默认档、回退档、曾经的修复档字面量、任意乱写值 —— 全都必须是「不发」。**
func TestLoginSkillSwitchLoadoutAfterCommitIsRemovedForEveryValue(t *testing.T) {
	values := []string{
		"",                                       // 未设置 = 曾经是"修复档"，现在也必须是不发
		"after_actor_generation_rebuild",         // 已移除的修复档字面量
		"before_transition_only",                 // 当时的回退档
		"after_login_scene_ready",                // 乱写
		"on", "1", "true", "yes", "fix", "slots", // 其它家族的"开启"字面量，绝不能被误认
		"BEFORE_TRANSITION_ONLY",     // 大小写
		"  before_transition_only  ", // 前后空白
	}
	for _, value := range values {
		t.Setenv(loginSkillSwitchLoadoutAfterCommitEnv, value)
		if loginSkillSwitchLoadoutAfterCommitEnabled() {
			t.Fatalf("env=%q: 已移除的功能不允许被任何取值开启", value)
		}
	}
}

// TestLoginSkillSwitchLoadoutAfterCommitSwitchReadsButNeverEnables 钉两件事：
//  1. 这个键**仍然被读**（排障时能如实回显它被设成了什么）—— 这是"兼容保留"的一半；
//  2. 无论它被设成什么，档位名都带 `removed_ignored:` / 恒定 `removed_always_off`
//     —— 使"键被设过"与"键会生效"在日志上不可能被混淆。
func TestLoginSkillSwitchLoadoutAfterCommitSwitchReadsButNeverEnables(t *testing.T) {
	t.Setenv(loginSkillSwitchLoadoutAfterCommitEnv, "")
	if got := loginSkillSwitchLoadoutAfterCommitSwitch(); got != loginSkillSwitchLoadoutAfterCommitRemoved {
		t.Fatalf("未设置时档位名 = %q, want %q", got, loginSkillSwitchLoadoutAfterCommitRemoved)
	}

	// 用 t.Setenv 之外的方式写一次，确认读的是**这个**键名（防止键名被改错后测试仍然绿）。
	const want = "removed_ignored:before_transition_only"
	os.Setenv(loginSkillSwitchLoadoutAfterCommitEnv, "before_transition_only")
	defer os.Unsetenv(loginSkillSwitchLoadoutAfterCommitEnv)
	if got := loginSkillSwitchLoadoutAfterCommitSwitch(); got != want {
		t.Fatalf("档位名 = %q, want %q（键名=%q）", got, want, loginSkillSwitchLoadoutAfterCommitEnv)
	}
	if loginSkillSwitchLoadoutAfterCommitEnabled() {
		t.Fatal("读到了 env，但行为必须仍然是「不发」")
	}
}
