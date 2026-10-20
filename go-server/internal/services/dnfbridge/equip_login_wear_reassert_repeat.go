// 本文件是"登录复述穿戴态"兜底在 **dnfbridge 侧的第三层范围收口**：
// 「同一条陈述的重复」不再受"每槽复述处理窗口"的时钟限制。
//
// 现场（2026-09-28 18:32 CST 部署件上线后，`/opt/dnf90/logs/packet_log.txt`，
// 角色 6 test78 / 武器 101040493 / 装备槽 12，会话 `game-000006`）：
//
//	10:36:55.400  game-connect game-000006
//	10:37:12.990  game-legacy-town-scene-ready-ack-accepted（窗口 15000/ repeat /
//	              anchor=slot_refresh / per_slot=on / per_slot_window_ms=5000）
//	10:37:14.350  :76836 op19 …0a000000000001000000030c006dc1050600000000ffffffff0000
//	              src=(0,10,IV=0) count=1 dst=(3,12,IV=101040493) ⇒ noop
//	              noopGate=login_wear_reassert_noop（**该槽的每槽窗口在这里开表**）
//	10:37:17.352 / 17.761 / 18.049  槽 17 / 18 / 16 的三条同族包 ⇒ noop（family-repeat）
//	10:37:18.861 / 19.151 / 19.376 / 19.604
//	              四条反向伴生包（srcIV=item、dstIV=0）⇒ noop noopGate=identity_echo
//	10:37:20.896  :76910 **与 :76836 逐字节同一条陈述** ⇒ 真卸下
//	              （:76912 `equipMove=unequip … item=101040493 changed=true`）
//	10:38:29.707  :77169 同一条陈述再一次 ⇒ 真卸下（:77171）
//
// 判据缺口（本条修的就是它）：`:76910` 距该槽第一次被吸收 6.546 s，比
// `loginWearReassertEchoIntervalMillis`（5000 ms）长 1.5 s ⇒
// `loginWearReassertSlotEpochOpen` 返回 false ⇒ `scope.absorb=false` ⇒
// 桥侧不给 `LoginWearWindowOpen` ⇒ 域侧三条登录闸门（判据里都要求这个标志）
// 全部惰性 ⇒ 落 `equip/owner.go` 的**反转端点兜底** ⇒ 真落库卸下。
// 也就是说：**闸门没被绕过，是每槽时钟把闸门关掉了**（同一个 op19 分支，
// 同一条 `resolveMoveEndpoints`）。
//
// 为什么"重复同一条陈述"可以再吸收（这是形状语义，不是"它大概是复述"）：
//   - 这一族的 op19 用 `SourceInstanceValue == 0` 声明**源端点为空**，它的全部内容
//     就是 `dst=(装备槽 D, 实例 X)` 这一句"槽 D 上是 X"（vendor 字段映射见
//     changelog:12627 与 owner.go:1008-1010 的现场原文）；
//   - 域侧闸门 `equipmentLoginWearReassertNoop` 要求 `X == 该槽实际穿着的 item id`，
//     并且命中后**一个字节都不写库**（noop）⇒ 该槽的权威内容在吸收前后完全一致；
//   - 于是在"同一会话 + 同一条陈述 + 该槽权威内容未变"三者同时成立时，客户端
//     再发一次这句话**没有引入任何新信息**：请求要的结果（槽 D 上是 X）在权威状态里
//     **已经成立**，没有任何搬运可以执行。域侧那三条闸门的第一条判据就是"结果已达成"。
//
// 为什么不改成"把每槽窗口调大"（既有负结果，不重新论证）：
//
//	`equip_login_wear_window.go:35-40` 已把"把时长调大"记为**被现场证伪**的方向
//	（窗口从 5 s 放宽到 15 s 之后 +46 s/+62 s/+68 s 的同族包照样真卸）；本文件因此
//	不碰任何时长，只把"同一条陈述"从时钟里摘出来。
//
// 玩家真实脱下的出口**必须保留**（`equipment_endpoint_echo_test.go:133-159` 钉的是
// 逐字节同形的真脱下）：本闸门只吸收**与已吸收过的那条陈述逐字节同字段**的重复。
// 玩家把装备拖到**另一个**背包端点、或任何字段不同的陈述，走的仍然是每槽窗口那套
// 时钟（默认 5000 ms 后放行）—— 与今天逐字节相同。
//
// 两档（默认即修复，回退必须显式）：
//
//	DNFBRIDGE_EQUIP_LOGIN_REASSERT_REPEAT=absorb（默认，含未设置/写错的字串）
//	    同槽 + 同陈述 + 已在窗口内被吸收过 ⇒ 无视每槽时钟继续吸收；
//	DNFBRIDGE_EQUIP_LOGIN_REASSERT_REPEAT=gate（及 0/false/no/legacy/disable/
//	    disabled/none/off）
//	    一键回退到今天：每槽时钟是唯一出口，重复陈述到点就放行（= 现场 10:37:20.896
//	    那次真卸下）。
//
// 回退档下本文件的状态**一个字节都不读也不写**（`loginWearReassertSlotRepeatAbsorbable`
// 第一句就返回 false；`loginWearReassertSlotEpoch.statement` 只是一个惰性字段）。
package dnfbridge

import (
	"os"
	"strings"

	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
)

// 开关名与两个档位字面量。
const (
	// loginWearReassertRepeatEnv 决定"同一条陈述的重复"是否继续吸收。
	loginWearReassertRepeatEnv = "DNFBRIDGE_EQUIP_LOGIN_REASSERT_REPEAT"

	// loginWearReassertRepeatAbsorb 是默认（修复档）。
	loginWearReassertRepeatAbsorb = "absorb"
	// loginWearReassertRepeatGate 是回退档：只认每槽时钟（今天）。
	loginWearReassertRepeatGate = "gate"
)

// loginWearReassertRepeatMode 回答生效的档位（运行时读 env，不缓存；与
// loginWearReassertAnchorMode / loginWearReassertPerSlotMode 同一读法）。
//
// 只有**显式**写 gate（及其别名）才回退到今天；其余取值（含未设置、写错的字串、
// absorb）都是修复档 —— 默认即修复，回退必须显式。
func loginWearReassertRepeatMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(loginWearReassertRepeatEnv))) {
	case loginWearReassertRepeatGate, "off", "0", "false", "no", "legacy", "disable", "disabled", "none":
		return loginWearReassertRepeatGate
	default:
		return loginWearReassertRepeatAbsorb
	}
}

// loginWearReassertRepeatAbsorbEnabled 是上面那个档位的布尔形式（判定与日志共用一处真相）。
func loginWearReassertRepeatAbsorbEnabled() bool {
	return loginWearReassertRepeatMode() == loginWearReassertRepeatAbsorb
}

// loginWearReassertRepeat 只用于事件日志/诊断（Service 上的透传）。
//
// 为什么没有进启动回显：`service.go` 的启动回显块不在本轮的改动边界内；
// 改为在**每次命中的事件行**上打 `repeat=<档>`（见
// equip_login_wear_family_window.go），档位可观测性因此不依赖启动回显，
// 而且带上了"命中当刻的档位"这一更强的证据。回退档下这两条事件都不会出现，
// 现场只需 `grep -a 'equip-login-wear-repeat-reassert-absorbed'`。
func (s *Service) loginWearReassertRepeat() string {
	return loginWearReassertRepeatMode()
}

// loginWearReassertStatement 是这条 op19 在**客户端自己声明的身份字段**上的规范化陈述。
//
// 只取身份字段（与 owner.go 的三条闸门读的是同一批字段）：`SourceInstanceValue == 0`
// 时源端点声明"这里没有物品"，因此**槽位号本身不是身份证据**；真正的内容是
// `destination(槽, 实例)` 那一句。尾部四个字段（DestinationStack / ActorIndex /
// TrailingState0/1）不进陈述：它们是搬运参数而不是身份（与"耐久不是身份"同口径）。
//
// 类型可直接比较（全部是定长标量），因此"是不是同一条陈述"就是 `==`，没有哈希、
// 没有前缀匹配，也不会把"相似"当成"相同"。
type loginWearReassertStatement struct {
	sourceListType           byte
	sourceSlotIndex          int16
	sourceInstanceValue      int32
	moveCount                int32
	destinationListType      byte
	destinationSlotIndex     int16
	destinationInstanceValue int32
}

// loginWearReassertStatementOf 解码一条 op19 body 的规范化陈述。复用
// dnfinventory.DecodeMoveItemspaceRequest（与形状判据、槽位判据同一份解码器 ⇒
// 28 字节布局只有一处真相），解码失败即 ok=false（形状不明的包既不吃窗口，
// 也不参与"同一条陈述"的比较）。
func loginWearReassertStatementOf(body []byte) (loginWearReassertStatement, bool) {
	request, err := dnfinventory.DecodeMoveItemspaceRequest(body)
	if err != nil {
		return loginWearReassertStatement{}, false
	}
	return loginWearReassertStatement{
		sourceListType:           request.SourceListType,
		sourceSlotIndex:          request.SourceSlotIndex,
		sourceInstanceValue:      request.SourceInstanceValue,
		moveCount:                request.MoveCount,
		destinationListType:      request.DestinationListType,
		destinationSlotIndex:     request.DestinationSlotIndex,
		destinationInstanceValue: request.DestinationInstanceValue,
	}, true
}

// loginWearReassertSlotRepeatAbsorbable 回答"该槽的每槽时钟虽已过期，但这条 op19
// 与**本会话该槽最近一次被吸收的那条陈述**逐字段相同，因此仍然是同一次复述"。
//
// 判据只有两条，都要求前一次吸收已发生：
//   - 该槽本会话已经吸收过（`absorbed`，即每槽窗口确实开过表）；
//   - 这一次的陈述与开表那次（或该槽最近一次被吸收的那次）**逐字段相同**。
//
// 回退档（gate）下第一句就返回 false：调用方随即按今天的行为放行，一个字节都不多写。
func (session *gameSession) loginWearReassertSlotRepeatAbsorbable(slot int16, statement loginWearReassertStatement) bool {
	if !loginWearReassertRepeatAbsorbEnabled() {
		return false
	}
	if session == nil || session.equipLoginWearReassertSlots == nil {
		return false
	}
	epoch, seen := session.equipLoginWearReassertSlots[slot]
	if !seen || !epoch.absorbed || !epoch.statementRecorded {
		return false
	}
	return epoch.statement == statement
}
