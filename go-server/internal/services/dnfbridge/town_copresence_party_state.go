package dnfbridge

import (
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// 本文件修一条现场确诊的组队缺陷：**出门（op1425）之后队员客户端会自己退队**。
//
// # 现场证据（2026-09-27 四步单变量二分，全自动、每步只动一个变量）
//
//	p10：组队后静置（此前已出门）→ `client_leave_party_op13` 0→2（队员**自发**退队）
//	     随后再让队长开选择器 → 2→2（**与选择器/跟随者转换无关**）
//	p12：组队后**不出门**、静置 60 秒 → 零新增 ⇒ 只有"走出赛丽亚房门"之后才会退队
//	p13：出门 + 抑制 msg 6（离开区域通知）→ 仍退队 ⇒ 排除 msg 6
//	p14：出门 + 抑制 msg 9（队伍帧）→ **零新增** ⇒ 触发点就是这条 op9
//
// 退队的原始日志（每轮同形）：
//
//	RECV cmd=1 type=13 ENUM_CMDPACKET_LEAVE_PARTY body_len=0
//	EVENT game-party-membership-removed source=client_leave_party_op13 char_id=4
//	      party_id_before=1 member_count_before=2 → member_count_after=1
//
// # 机制
//
// 出门路由把两名角色放进同一个共享城镇区域后，会给"已经在场的那位"补发 newcomer 的 actor 投影，
// 其中最后一条是 op9 队伍帧（`sendCurrentPartyActorFrameProjection`，
// source=`town_copresence_actor_initial_party_state`，见 online_player_manager.go）。它当时传的是
// **被投影 actor 那条会话的本地缓存** `runtimePartyStateSnapshot(actor.Session)`。
//
// 而 `party_manager_runtime.go` 的文件头写明了这条口径的坑：会话本地缓存只是"权威名单被投影到
// 某一条会话"的结果 —— 同角色的第二条连接、通道重连的替换 socket，以及大量非发布路径上的会话
// 读到的都是**空缓存（party_id=0）**。于是观察方客户端收到的是"这名队友其实不在任何队伍里"，
// 与它自己持有的队伍名册（op9/op11/op153 三件套）矛盾 ⇒ 客户端判定队伍无效、自己发 op13 退队 ⇒
// 服务端把队伍降成单人 ⇒ 之后"整队进图 / 队友端点 / 房间槽位表 / 队友 actor 投影"全部按单人跳过
// （现场 p9 就是 `party_member_count=1` + `runtime_party_member_below_two_members`）。
//
// # 修法
//
// 2026-10-06：official-24.9 档在未设置开关时默认使用权威名单；其他档维持旧默认。
// 显式 off 仍可回退。默认值仅在 townCopresenceActorPartyState 中按 profile 选择。
//
//	`DNFBRIDGE_TOWN_COPRESENCE_PARTY_AUTHORITATIVE=on`
//	  ⇒ 这条同屏 actor 的队伍帧改用**权威**队伍状态（`runtimePartyStateForSession`，它按角色查中央
//	    RuntimePartyManager，并把权威值写回会话缓存）；
//	  off（显式回退）⇒ 维持既有的"会话本地缓存"口径，行为与改动前逐字节一致。
//
// 回退：把开关显式设为 off 后重启；official-24.9 删除该行会恢复权威默认。
const townCopresencePartyAuthoritativeEnv = "DNFBRIDGE_TOWN_COPRESENCE_PARTY_AUTHORITATIVE"

func townCopresencePartyAuthoritativeEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(townCopresencePartyAuthoritativeEnv))) {
	case "on", "1", "true", "yes", "authoritative":
		return true
	default:
		return false
	}
}

// townCopresenceActorPartyState 解析"给同屏 newcomer 投影哪一份队伍帧"。
//
// 显式 off 返回会话缓存；official-24.9 未设置开关或显式 on 时返回权威状态。
// 注意 `runtimePartyStateForSession` 在查不到权威记录时会回落到同一个本地缓存，
// 因此打开开关不会引入"凭空造一个队伍"的风险。
func (s *Service) townCopresenceActorPartyState(session *gameSession) alignedcmd.PartyState {
	if townCopresencePartyAuthoritativeEnabled() || (strings.TrimSpace(os.Getenv(townCopresencePartyAuthoritativeEnv)) == "" && s != nil && s.options.currentClientVersion == "official-24.9") {
		return s.runtimePartyStateForSession(session)
	}
	return runtimePartyStateSnapshot(session)
}

// townCopresenceSkipSamePartyFrameEnv 是"观察方与该 actor 已在同一支队伍时，跳过那条
// '再声明一次队伍' 的 op9"的开关。
//
// official-24.9 未设置开关时默认开启城镇规则；其他档默认 off。
// `on` / `1` / `true` / `yes` / `skip` 显式打开，off 可回退。
//
// 为什么不应用在公共出口：把同一规则提到 `sendCurrentPartyActorFrameProjectionInContext` 后，
// 4 个既有用例（钉住副本内队友 op9 与 `DNFBRIDGE_PARTY_DUNGEON_OWNER_MODE` 的逐字节线格式）
// **立刻失败** ⇒ 副本那条路径的队友 op9 是**已被证据固化**的行为，不能一刀切跳过。
// 因此本规则只覆盖城镇同屏与区域队友广播两处（均已现场验证），副本侧仍需单独二分。
//
// # 为什么（p14 / p15 / p16 / p17 / p18 五次单变量对照的结论）
//
//	p14：出门 + 抑制 msg 9 ⇒ `client_leave_party_op13` **零新增**（队伍保住）
//	p15：出门 + 把这条 op9 的内容从"会话本地缓存"换成**权威**名单 ⇒ **仍然退队**
//	     ⇒ 触发客户端退队的是**这条包本身的存在**，不是它的内容
//	p16：出门 + 本规则 ⇒ **零退队**（城镇侧修好）
//	p17：全流程（出门→选择器→进图）⇒ 城镇 0 次，但**进图后仍有 1 次**
//	p18：把规则提到公共出口 `sendCurrentPartyActorFrameProjectionInContext` ⇒
//	     精确跳过副本侧两条队友 op9；至今仍剩一次（下一步单独二分）
//
// 而这条 op9 的语义是"声明被投影 actor 当前的队伍状态"：当观察方与该 actor 本来就在同一支
// 队伍里，观察方在组队成立/进图时已经拿到过完整名册（op11 端点 + op9 帧 + op153 实时信息），
// 这条重复声明既无信息量、又会让客户端判定队伍不一致而自己 op13 退队。
//
// 原意不受影响：只有"双方权威 party_id 相同且都 > 0"才跳过；actor 不在队伍里、或与观察方
// 不同队时照旧发送（那条路径正是这条包存在的理由），receiver 与 actor 同角色时也照发。
const townCopresenceSkipSamePartyFrameEnv = "DNFBRIDGE_TOWN_COPRESENCE_SKIP_SAME_PARTY_FRAME"

func townCopresenceSkipSamePartyFrameEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(townCopresenceSkipSamePartyFrameEnv))) {
	case "on", "1", "true", "yes", "skip":
		return true
	default:
		return false
	}
}

// shouldSkipTownCopresencePartyFrame 判定"这条 newcomer 队伍帧是否多余到可以跳过"。
//
// 判据一律取**权威**队伍状态（按角色查中央 RuntimePartyManager），不用会话本地缓存：
// 现场缺投影的会话读到的 party_id 会是 0，用它比较会得出"不同队"的错误结论。
func (s *Service) shouldSkipTownCopresencePartyFrame(target, actor *gameSession) bool {
	if s == nil || target == nil || actor == nil {
		return false
	}
	// This applies only at the town callers. Keep dungeon projections and the
	// actor's own state notification intact. Explicit off remains a rollback.
	if target.selectedCharacterID == 0 || target.selectedCharacterID == actor.selectedCharacterID {
		return false
	}
	if !townCopresenceSkipSamePartyFrameEnabled() && !(strings.TrimSpace(os.Getenv(townCopresenceSkipSamePartyFrameEnv)) == "" && s.options.currentClientVersion == "official-24.9") {
		return false
	}
	observer := s.runtimePartyStateForSession(target)
	projected := s.runtimePartyStateForSession(actor)
	if observer.PartyID <= 0 || projected.PartyID <= 0 {
		return false
	}
	return observer.PartyID == projected.PartyID
}
