// 本文件只做一件事：把 op19 的**方向判读**写成一行可 grep 的现场事件。
//
// 判定本身在域层（`internal/modules/dnf/equip/worn_slot_reassert_gate.go` 的
// equipmentWornSlotReassert）：客户端把身份写在装备端点、源端声明里什么都没有时，
// 这条 op19 声明的是"那一格现在穿着什么"，**不是搬运请求** —— 两个方向都判 noop、
// 一个字节都不写，**与登录窗口、与任何开关、与时间无关**：
//
//	(a) 那一格穿着点名那件 ⇒ 声明已经成立        ⇒ worn_slot_reassert
//	(b) 那一格**空着**     ⇒ 声明与权威状态矛盾  ⇒ worn_slot_stale_claim
//	    （操作者 2026-09-28 更正的另一半：先手动脱下武器、再选角进角色时，
//	      旧口径走到末尾 `if aOccupied` ⇒ o.equip ⇒ 刚脱下来的武器又被穿回去）
//
// 桥侧这一行不复制那条判据（否则又会出现"桥说是复述、域说是真移动"的两套口径）：
// 它只记录**客户端自己声明的形状**、这一条是否被既有的登录引导窗口各层吸收、
// 以及接下来由哪一层负责把这条包变成 noop。判定与写库仍然只有 equip 域一处真相。
//
// 现场依据（`/opt/dnf90/logs/packet_log.txt`）：
//
//	:93098  t=13:24:21.46511482Z   hex 01130029000000c97de1fe22000010000000000001000000030c006dc1050600000000ffffffff0000
//	:91884  t=13:20:21.733523941Z  hex 01130029000000805fff851f000010000000000001000000030c006dc1050600000000ffffffff0000
//	    ⇒ src=(list0 主背包,slot16,IV=0) dst=(list3 装备,slot12,IV=101040493)：每次
//	      穿着光剑重登，客户端都自发复述这一句（穿钝器那 3 分钟 0 次）。
//	:76912  入站 t=10:37:20.89653374Z  hex 01130029000000162069b12b00000a000000000001000000030c006dc1050600000000ffffffff0000
//	    ⇒ 同一形状在兜底之前被判成 `equipMove=unequip changed=true`，武器真的落进背包
//	      （包体与上面两条**逐字节相同**，差别只在包头的计数器字节）。
//
// 所以"这条包像不像回显"在字节层面不可判；可判的只有端点角色 —— 这行事件把角色
// 逐个字段摊开，操作者不必再解码 hex。
package dnfbridge

import (
	"fmt"
	"os"
	"strings"

	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
)

// equipDirectionJudgementEvent 是这条判读事件的 kind（现场 grep 的锚点）。
const equipDirectionJudgementEvent = "game-equip-direction-judgement"

// equipWornSlotReassertSwitchEnv 是域层那条方向判定开关名在桥侧的**只读副本**：
// 只用于本行日志的 `worn_slot_reassert_switch=` 字段，桥侧**不**据此改变任何行为
// （开关的唯一消费者是 equip 域，见 worn_slot_reassert_gate.go）。两者取值必须一致，
// 由 equip_direction_judgement_test.go 钉住。
const equipWornSlotReassertSwitchEnv = "DNFBRIDGE_EQUIP_MOVE_WORN_SLOT_REASSERT"

// equipWornSlotReassertSwitchMode 报告域层那条判定的档位（"on" = 修复档，默认）。
func equipWornSlotReassertSwitchMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(equipWornSlotReassertSwitchEnv))) {
	case "off", "0", "false", "no", "legacy", "disable", "disabled", "none":
		return "off"
	default:
		return "on"
	}
}

// loginWearReassertSwitchMode 报告登录引导窗口兜底主开关的档位（只读，用于本行日志）。
func loginWearReassertSwitchMode() string {
	if equipLoginWearReassertNoopEnabled() {
		return "on"
	}
	return "off"
}

// logEquipDirectionJudgement 打出上面那条事件。
//
// 调用点只有一个：equipLoginWearFamilyWindowOpenForOp19 —— 也就是 game_aligned.go
// op19 分支在**这个形状**上唯一会走到的桥侧函数。因此"客户端复述穿戴态"的每一条
// op19 都会留下恰好一行，且与档位无关（回退档也打：档位可观测性不许依赖档位本身）。
//
// absorbed=true 表示这条包已被既有的登录引导窗口各层（会话窗口 / 槽锚点 / 每槽 /
// 轮次）吸收，接下来由 equip 域的 login_wear_reassert_noop 具名；absorbed=false
// 表示各层都放行了它，接下来由**方向判定本身**接管。方向判定按权威状态二选一，
// 两个方向都判 noop、一个字节都不写：
//
//	那一格穿着点名那件 ⇒ expected_noop_gate=worn_slot_reassert
//	那一格**空着**     ⇒ expected_noop_gate_when_that_slot_is_empty=worn_slot_stale_claim
//	                     （玩家刚把它脱下来；绝不用这条陈述把背包里那件穿回去）
func (s *Service) logEquipDirectionJudgement(session *gameSession, request dnfinventory.MoveItemspaceRequest, absorbed bool) {
	if s == nil {
		return
	}
	absorption := "released_to_the_domain_direction_judgement"
	noopGate := dnfequip.WornSlotReassertGate
	noopGateWhenEmpty := dnfequip.WornSlotStaleClaimGate
	if absorbed {
		absorption = "absorbed_by_the_login_wear_reassert_layers"
		noopGate = dnfequip.LoginWearReassertNoopGate
		noopGateWhenEmpty = dnfequip.LoginWearReassertNoopGate
	}
	s.logGameEvent(session, equipDirectionJudgementEvent,
		"judgement", "worn_endpoint_reassert",
		"declared", "destination_names_the_item_on_the_worn_endpoint_while_the_source_endpoint_declares_nothing",
		"src_list", request.SourceListType,
		"src_slot", request.SourceSlotIndex,
		"src_instance", request.SourceInstanceValue,
		"dst_list", request.DestinationListType,
		"dst_slot", request.DestinationSlotIndex,
		"dst_instance", request.DestinationInstanceValue,
		"endpoints", fmt.Sprintf("src=(%d,%d,%d)->dst=(%d,%d,%d)",
			request.SourceListType, request.SourceSlotIndex, request.SourceInstanceValue,
			request.DestinationListType, request.DestinationSlotIndex, request.DestinationInstanceValue),
		// 客户端在装备端点上报出的那件物品（= 它说"这一格是这件"）。域层会把它与
		// 权威穿戴行核对后写进 `equipMove=… item=` 那一行。
		"client_named_worn_item", request.DestinationInstanceValue,
		"window_absorption", absorption,
		"domain_rule", dnfequip.WornSlotReassertGate,
		"domain_rule_when_that_slot_is_empty", dnfequip.WornSlotStaleClaimGate,
		"worn_slot_reassert_switch", equipWornSlotReassertSwitchMode(),
		"login_wear_reassert_switch", loginWearReassertSwitchMode(),
		"expected_noop_gate", noopGate,
		"expected_noop_gate_when_that_slot_is_empty", noopGateWhenEmpty,
		"outcome_source", "the_following_op19_equipMove_and_noopGate_fields",
		"evidence", "packet_log_93098_91884_relog_reassert_76912_pre_gate_real_unequip_93127_genuine_equip_and_the_ad_report_unequip_then_reselect_re_equips")
}
