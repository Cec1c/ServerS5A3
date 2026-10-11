package dnfbridge

import "strings"

// 本文件是"队员点退出队伍必须真的离队"这条缺陷的修法开关。
//
// 现场缺陷（操作者原话）：**「p2 点击的是退出队伍，但是仍然在队伍里面」**
// —— 队员在队伍界面点「退出队伍」后，队长那边仍显示他在队里。
//
// 服务端侧可证的两条根因都在 `handleOnlineLeaveParty`（party_online.go）：
//
//	① `leaveManagedRuntimeParty` 返回 `removed=false` 时，处理函数把两种**完全不同**
//	   的情形当成同一种处理：
//	     - 管理器里**已经没有**这个角色（历史投影残留）：清自己的客户端投影 + 回 op13 ACK
//	       是正确且幂等的；
//	     - 管理器里**仍然有**这个角色，只是它记录的 `SessionGeneration` 与会话当前代际
//	       不一致（`result.Reason == "stale_session"`；频道重连、重选角、同角色
//	       旧/新两个 socket 并存之后都会出现）：这时"只清自己 + 只回 ACK"会让
//	       **管理器继续把 p2 当成队员、队长收不到任何移除记录**，下一轮名册/共在场广播
//	       又把 p2 画回队长的队伍框里 —— 正是操作者看到的现象。
//	② 会话未绑定权威角色时 `handleOnlineLeaveParty` 直接 `return nil`：**一个字节都不回**，
//	   客户端既不回滚也不提示，玩家看到的是"点了没反应"。
//
// 修法（默认档 `repair`）：
//   - `removed=false` 且管理器里仍有该角色时，按**角色**（而不是按代际）再摘一次
//     （新增 `RuntimePartyManager.LeaveCurrent`），成功后走既有成功路径
//     （广播 `runtime_party_member_removed` 给每个幸存者 + op13 ACK + 关队伍 UDP 中继）；
//   - 未绑定/未选角不再静默：记具名事件并回失败包，让客户端能回滚。
//
// 为什么"按角色无条件摘除"不会破坏既有的**防旧 socket**语义：只有角色的**当前权威会话**
// 才可能走到这里 —— `handleOnlinePartyCommand` 先用 `boundGameSessionCharacterSnapshot`
// 把非权威会话整条消费掉（`game-party-command-stale-session-ignored`），
// `leaveManagedRuntimeParty` 再用 `boundGameSessionCharacterCurrent` 复查一次。
// 因此退役/被替换的 socket 依旧摘不掉替代会话的成员身份；这条修法只影响
// "请求确实来自该角色当前会话、而管理器记的代际已经漂移"这一种状态。
//
// 一键回退：`DNFBRIDGE_PARTY_LEAVE_REPAIR=legacy` 逐字节回到改动前
// （`removed=false` 只清自己投影 + 回 ACK，不通知队长；未绑定直接静默返回）。
const partyLeaveRepairEnv = "DNFBRIDGE_PARTY_LEAVE_REPAIR"

const (
	partyLeaveRepairValueRepair = "repair"
	partyLeaveRepairValueLegacy = "legacy"
)

// partyLeaveRepairMode 是 partyLeaveRepairEnv 的归一化取值。
type partyLeaveRepairMode int

const (
	// partyLeaveRepairOn 是默认档（零值）：管理器仍持有该角色时按角色无条件摘除离队。
	partyLeaveRepairOn partyLeaveRepairMode = iota
	// partyLeaveRepairLegacy 是改动前行为，一键回退档。
	partyLeaveRepairLegacy
)

func (mode partyLeaveRepairMode) String() string {
	if mode == partyLeaveRepairLegacy {
		return partyLeaveRepairValueLegacy
	}
	return partyLeaveRepairValueRepair
}

// readPartyLeaveRepairMode 解析 DNFBRIDGE_PARTY_LEAVE_REPAIR。
// **默认 `repair`（= 修好后的行为）**：空/未设置/非法值都是 repair；
// 只有显式 `legacy` 回到改动前那条"只清自己投影、不通知队长"的路径。
func readPartyLeaveRepairMode(value string) partyLeaveRepairMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case partyLeaveRepairValueLegacy:
		return partyLeaveRepairLegacy
	default:
		return partyLeaveRepairOn
	}
}

// partyLeaveRepair 返回当前 Service 的归一化档位（零值 options 的测试/探针路径
// = repair = 默认档）。
func (s *Service) partyLeaveRepair() partyLeaveRepairMode {
	if s == nil {
		return partyLeaveRepairOn
	}
	return s.options.partyLeaveRepair
}
