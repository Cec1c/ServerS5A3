package dnfbridge

import (
	"os"
	"strings"
)

// 本文件实现"**队员自己打开副本选择器**时，给该队员本人补一条完整的跟随者选择器转换"。
//
// # 现场缺口
//
// `synchronizeRuntimePartyMembersEnterSelect`（game_enter_select_dungeon.go）的既有口径是
// **只有队长能驱动整队选择器**：发起者不是队长时，只记一条
// `game-party-enter-select-non-leader-initiated` 就 `return nil`（零发包）。那条口径本身是对的
// （既有注释写明：把队员当发起者会把队长推进"跟随者"状态，是历史上"进图崩溃"的根因之一）。
//
// 但现场日志里这条事件出现过 6 次（例如 `char_id=2 leader_char_id=3`、`char_id=5 leader_char_id=3`，
// `source=upper_15_party_members`），也就是**队员的客户端自己先开了选择器**。此时该队员只拿到
// 普通 op15 应答 + 疲劳包（3 包），拿不到队长驱动时队员会拿到的那条完整序列：
//
//	op23 区域脱离(area ff) → op2 mode0 → mode1 → op3 → op9 队伍名册 → class1/op15 成功体
//	→ 疲劳 → 副本权限快照 → 选择器上下文
//
// 而 `prepareRuntimePartyFollowerEnterSelect` 的注释写明：**当前客户端要用 op15 成功体来安装
// "可选副本页"，而且必须在该 actor 从城镇脱离之后**——否则地图窗口只有一个背景、不可交互。
// 操作者报障原文正是"队员看不到副本选择界面，只有一个背景没有副本"。
//
// # 本开关做什么
//
// 打开后：发起者不是队长时，**只对该发起者本人**执行那条完整转换（复用同一个原语
// `prepareRuntimePartyFollowerEnterSelect`），然后照旧返回。
//
//   - **绝不改变队伍归属**：`state.UserID`（队长）原样不动，队长不会被推进跟随者状态；
//   - **绝不 fan-out**：其他队员一个字节都不发（这条路径的语义是"这个人自己要看选择器"，
//     不是"整队进选择器"——整队进选择器仍然只由队长驱动）；
//   - 队长的队伍快照、队长会话的状态一个字段都不改。
//
// # 开关
//
//	`DNFBRIDGE_PARTY_SELECTOR_MEMBER_SELF_PREPARE`
//	  - `off`（默认，零值）= 逐字节回到改动前：只记 `game-party-enter-select-non-leader-initiated`，
//	    零发包；
//	  - `on` / `1` / `true` / `yes` = 打开上面的自补转换。
//
// 回退：把开关设 `off`（或删掉该行）并重启即可逐字节回到改动前。
const partySelectorMemberSelfPrepareEnv = "DNFBRIDGE_PARTY_SELECTOR_MEMBER_SELF_PREPARE"

func partySelectorMemberSelfPrepareEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(partySelectorMemberSelfPrepareEnv))) {
	case "on", "1", "true", "yes":
		return true
	default:
		return false
	}
}
