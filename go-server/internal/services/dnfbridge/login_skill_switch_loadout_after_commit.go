package dnfbridge

// 本文件**只剩兼容**：`DNFBRIDGE_LOGIN_SKILL_SWITCH_LOADOUT_AFTER_COMMIT` 这个 env 键
// 保留下来（读取只为日志），但**它已经不能开启任何行为** —— 档位语义恒为「不发」。
//
// ── 一、这里原来是什么
//
// 第五轮的边界修法：在 actor 世代重建之后，用产品路径已有的构造器
// `sendSkillSwitchLoadoutResend` 再补发一帧 `msg_id=1428`（「Buff 强化 / 技能换装」登记），
// 理由是此前**全部**四条 op1428 下行都落在场景提交之前/提交点上（= 被销毁的那一代对象）。
//
// ── 二、为什么整段删掉（不是"关掉"）
//
// 操作者真机验收（证据链：`_dist/buff-swap-equip-persistence.md` §15.1）：
//
//	skill-switch-loadout-after-commit-sent       2   ← 该帧确实发出去了（char 1, loadout_rows 16→17）
//	skill-switch-loadout-after-commit-skipped    0
//	skill-switch-loadout-after-commit-suppressed 0
//	skill-switch-loadout-after-commit-failed     0
//
// 而面板仍然为空（操作者原话：「装备没有选取，技能也没有打勾」）⇒ **该修法被证伪**。
// 当时用一行 env 回退（§15.2）。但"代码还在、只靠 env 关"留了一个地雷：
// **任何一次环境重置或配置清理都会让它静默复活**，把一帧已被验收证伪的下行重新发出去。
// 所以发送路径与它在 `login_actor_generation_rebuild.go` 的调用点**整段删除** ——
// **任何 env 取值都无法再发出这一帧。**
//
// ── 三、保留下来的东西与保证（由 login_skill_switch_loadout_after_commit_test.go 钉住）
//
//   - `loginSkillSwitchLoadoutAfterCommitEnabled()` **恒返回 false**（含默认档、
//     `before_transition_only` 档、以及任何写错的值）⇒ 恒不发；
//   - `loginSkillSwitchLoadoutAfterCommitSwitch()` 仍然**读**这个 env 键，只为在日志/排障时
//     如实回显"这个键被设成了什么"，前缀 `removed_ignored:`；
//   - 因此删掉/改写这个 env 键**不会**改变任何行为，也不会让任何东西复活。
//
// ⚠️ 与本功能无关的**产品路径**（`DNFBRIDGE_SKILL_SWITCH_*` 与四条既有重投影边界上的换装帧）
// **完全没有被动过**：本文件只把"第五轮多挂一个边界"这一件事删干净。

import (
	"os"
	"strings"
)

const (
	// loginSkillSwitchLoadoutAfterCommitEnv 是**已废弃**的开关名，仅为兼容保留（只读、只记日志）。
	loginSkillSwitchLoadoutAfterCommitEnv = "DNFBRIDGE_LOGIN_SKILL_SWITCH_LOADOUT_AFTER_COMMIT"

	// loginSkillSwitchLoadoutAfterCommitRemoved 是恒定档位名：本功能已移除，任何 env 取值都落到这一档。
	loginSkillSwitchLoadoutAfterCommitRemoved = "removed_always_off"
)

// loginSkillSwitchLoadoutAfterCommitEnabled 恒为 false。
//
// 保留这个函数（而不是删掉）是刻意的：后继读者可以从这里读到"这个键曾经能开启行为、
// 现在不能了"的**显式**结论，而不是从"某个函数不见了"去猜。
func loginSkillSwitchLoadoutAfterCommitEnabled() bool {
	return false
}

// loginSkillSwitchLoadoutAfterCommitSwitch 只用于日志/排障：如实回显该 env 键当前被设成了什么，
// 并给结果加上 `removed_ignored:` 前缀，使"键被设过"与"键会生效"在日志上不可能被混淆。
func loginSkillSwitchLoadoutAfterCommitSwitch() string {
	value := strings.ToLower(strings.TrimSpace(
		os.Getenv(loginSkillSwitchLoadoutAfterCommitEnv)))
	if value == "" {
		return loginSkillSwitchLoadoutAfterCommitRemoved
	}
	return "removed_ignored:" + value
}
