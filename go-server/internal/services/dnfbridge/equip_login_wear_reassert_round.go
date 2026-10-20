// 本文件是"登录复述穿戴态"兜底在 **dnfbridge 侧的第四层范围收口**：
// 「客户端**又进了一次场景**之后，重述同一句"槽 D 上是 X"」不再被上一轮的每槽时钟挡住。
//
// 现场（2026-09-28 18:41:41.829 CST，`/opt/dnf90/logs/packet_log.txt` 会话 `game-000009`，
// 角色 6 test78 / 装备槽 10 / item 101600023，账号 `dnf:10011`）：
//
//	10:38:55.128  legacy type=1345 场景就绪 ack ⇒ 开窗（15000 / repeat / slot_refresh / per_slot on 5000）
//	10:38:56.398  :77583  A 形状 src=(0,10,IV=0) dst=(3,12,IV=101040493) ⇒ noop（本槽开表）
//	10:39:00.580  :77613  同一条陈述再来 ⇒ 窗口内 ⇒ noop
//	10:39:29.599  legacy type=1345 场景就绪 ack ⇒ **新一轮**（窗口重开）
//	10:41:01.145  :77924  A 形状 src=(**1,142**,IV=0) dst=(3,10,IV=101600023)
//	              会话窗口已过期（+91.5 s）⇒ 靠**每槽锚点**吸收 ⇒
//	              `game-equip-login-wear-slot-reassert-absorbed worn_slot=10` ⇒ noop
//	10:41:35.780  legacy type=1345 场景就绪 ack ⇒ **又一轮**（窗口重开，15 s）
//	10:41:41.750  :78228  A 形状 src=(**1,143**,IV=0) dst=(3,10,IV=101600023)
//	              距新 ack 5.97 s（**在会话窗口之内**）⇒ 真落库卸下（:78230 equipMove=unequip
//	              item=101600023 changed=true）—— 操作者报的"还是会卸下"就是这一条。
//
// 为什么它会掉出去（三层口径逐条查过，见报告 §2 的三张判定表）：
//   - 形状对（`srcIV == 0` + 装备端点写了实例）⇒ 三层都**被调用**了；
//   - 会话窗口：10:41:41.750 在 10:41:35.780 开的 15 s 窗口**之内** ⇒ `inWindow=true`；
//   - 每槽时钟：该槽的表是 **上一轮**（10:41:01.145）开的，5000 ms 早过 ⇒
//     `loginWearReassertSlotEpochOpen` 返回 false ⇒ 每槽闸门否决了整条吸收；
//   - 重复陈述（`DNFBRIDGE_EQUIP_LOGIN_REASSERT_REPEAT=absorb`）：客户端把**点名的空端点**
//     从 142 换成了 143，而那条判据把源端点算进身份 ⇒ `epoch.statement != statement` ⇒
//     不吸收 ⇒ 桥侧不给 `LoginWearWindowOpen` ⇒ 域侧三条登录闸门惰性 ⇒ 落
//     `equip/owner.go` 的**反转端点兜底** ⇒ 真落库卸下。
//
// 这一层修的是**第 3 条**那个口径，依据是**形状语义 + 会话上下文**，不是"它大概是复述"：
//
//  1. 形状语义：`srcIV == 0` 的这条包用源端点声明"这里没有物品"，它的全部内容就是
//     `dst=(装备槽 D, 实例 X)` 这一句（现场 142 → 143 的差别只在"客户端随手点哪个空格"，
//     `equip_login_wear_reassert_repeat.go:112-115` 自己就写着"槽位号本身不是身份证据"）。
//     因此**已吸收陈述的身份**只取**装备端点那一句**（moveCount + dst 三元组）——
//     点名的源端点（容器 + 槽位）是这条移动的"落点提议"，不是身份。
//  2. 会话上下文：`legacy type=1345` 场景就绪 ack 是客户端"我进场景了"的声明，也是本兜底
//     唯一的锚点（`game_dispatch_legacy.go:238`）。一次新的 ack = 客户端**又走了一遍进场景**，
//     它这一遍的复述串会**重述**同一批穿戴格；把上一轮的每槽时钟继续套在这一遍上，
//     就是把"同一次复述的第二次陈述"判成新意图。
//
// 为什么这不是"把闸门放宽"（**玩家的出口一个字节都没动**）：
//   - 重新开窗**每槽每一轮最多一次**（`epoch.round == 当前轮` 时本条判据直接返回 false）；
//   - 重新开窗**只对"重述同一句装备端点"的包**生效：点的不是同一件/不是同一格（例如玩家把
//     另一件拖下来、或客户端改口说别的物品）= 新意图，本条判据返回 false，照旧放行；
//   - 玩家自己把身上的东西拖出来那一条（`equipment_endpoint_echo_test.go:133-159` 钉的形状，
//     与本族同字段）仍然靠**点名端点的不同**在**同一轮内**放行 —— 那是
//     `equip_login_wear_reassert_repeat_test.go:243-270` 与
//     `equip_login_wear_reassert_anchor_test.go:447-485` 钉死的出口，本文件一个字都没改。
//
// 两档（默认即修复，回退必须显式）：
//
//	DNFBRIDGE_EQUIP_LOGIN_REASSERT_ROUND=absorb（默认，含未设置/写错的字串）
//	    新的场景就绪 ack = 新一轮：该轮里第一次重述同一句"槽 D 上是 X"时，
//	    重新开一次该槽的复述处理窗口（起点 = 现在，轮内仍不随重复包滑动）；
//	DNFBRIDGE_EQUIP_LOGIN_REASSERT_ROUND=session（及 off/0/false/no/legacy/disable/
//	    disabled/none）
//	    一键回退到今天：轮次永不推进（`gameSession.equipLoginWearReassertRound` 恒为 0），
//	    每槽账本仍然是"每会话一次"，判定与现场 10:41:41.750 那次真卸下逐字节相同。
package dnfbridge

import (
	"os"
	"strings"
	"time"
)

// loginWearReassertRoundEnv 决定"新的场景进入是否重开该槽的复述处理窗口"。
const (
	loginWearReassertRoundEnv = "DNFBRIDGE_EQUIP_LOGIN_REASSERT_ROUND"

	// loginWearReassertRoundAbsorb 是默认（修复档）：新一轮重述同一句 ⇒ 重开该槽窗口。
	loginWearReassertRoundAbsorb = "absorb"
	// loginWearReassertRoundSession 是回退档：轮次不推进（每槽每会话一次 = 今天）。
	loginWearReassertRoundSession = "session"
)

// loginWearReassertRoundMode 回答生效的档位（运行时读 env，不缓存；与
// loginWearReassertRepeatMode / loginWearReassertAnchorMode 同一读法）。
//
// 只有**显式**写 session（及其别名）才回退到今天；其余取值（含未设置、写错的字串、
// absorb）都是修复档 —— 默认即修复，回退必须显式。
func loginWearReassertRoundMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(loginWearReassertRoundEnv))) {
	case loginWearReassertRoundSession, "off", "0", "false", "no", "legacy", "disable", "disabled", "none":
		return loginWearReassertRoundSession
	default:
		return loginWearReassertRoundAbsorb
	}
}

// loginWearReassertRoundAbsorbEnabled 是上面那个档位的布尔形式（判定与日志共用一处真相）。
func loginWearReassertRoundAbsorbEnabled() bool {
	return loginWearReassertRoundMode() == loginWearReassertRoundAbsorb
}

// loginWearReassertRound 只用于事件日志/诊断（Service 上的透传；与
// loginWearReassertRepeat 同一口径：档位可观测性挂在**命中当刻的事件行**上，
// 不依赖 service.go 的启动回显块）。
func (s *Service) loginWearReassertRound() string {
	return loginWearReassertRoundMode()
}

// loginWearReassertWornIdentity 是一条复述陈述里**只属于装备端点**的那部分身份：
// 搬运件数 + 装备端点（容器 + 槽位 + 客户端指名的实例）。
//
// 为什么源端点不进身份：`srcIV == 0` 时源端点声明"这里没有物品"，它只是客户端为这次
// 移动**提议的一个空格**；现场同一句"槽 10 上是 101600023"在两轮里分别点名了 142 与 143
// （`packet_log.txt:77924` / `:78228`），点名的格子会变、这句陈述不变。
//
// 类型可直接比较（全部是定长标量），因此"是不是同一句"就是 `==`。
type loginWearReassertWornIdentity struct {
	moveCount                int32
	destinationListType      byte
	destinationSlotIndex     int16
	destinationInstanceValue int32
}

// loginWearReassertWornIdentityOf 取一条规范化陈述的装备端点身份。
func loginWearReassertWornIdentityOf(statement loginWearReassertStatement) loginWearReassertWornIdentity {
	return loginWearReassertWornIdentity{
		moveCount:                statement.moveCount,
		destinationListType:      statement.destinationListType,
		destinationSlotIndex:     statement.destinationSlotIndex,
		destinationInstanceValue: statement.destinationInstanceValue,
	}
}

// loginWearReassertSlotRoundReassertAbsorbable 回答"该槽的每槽时钟虽已过期，但客户端
// 又进了一次场景、并且在**这一轮**里重述了与上一轮被吸收那条**同一句装备端点陈述**"。
//
// 判据四条，缺一不可：
//   - 该槽本会话已经吸收过（`absorbed`，即确实有过那条陈述可比对）；
//   - 该槽记过轮次（`roundRecorded`；零值构造的账本一律不重开 —— 见
//     equip_login_wear_reassert_anchor.go 的字段说明，方向是"宁可少拦"）；
//   - 该槽上一次被吸收发生在**更早的轮次**（`epoch.round != 当前轮`）⇒ 本轮还没为它开过窗；
//   - 这一次的装备端点身份（moveCount + dst 三元组）与那条**逐字段相同**。
//
// 回退档（session）下第一句就返回 false：轮次永不推进，也没有账本会记轮次，
// 调用方随即按今天的行为放行，一个字节都不多写。
func (session *gameSession) loginWearReassertSlotRoundReassertAbsorbable(slot int16, statement loginWearReassertStatement) bool {
	if !loginWearReassertRoundAbsorbEnabled() {
		return false
	}
	if session == nil || session.equipLoginWearReassertSlots == nil {
		return false
	}
	epoch, seen := session.equipLoginWearReassertSlots[slot]
	if !seen || !epoch.absorbed || !epoch.statementRecorded || !epoch.roundRecorded {
		return false
	}
	if epoch.round == session.equipLoginWearReassertRound {
		return false
	}
	return loginWearReassertWornIdentityOf(epoch.statement) == loginWearReassertWornIdentityOf(statement)
}

// loginWearReassertSlotAbsorbedRound 读该槽上一次被吸收的轮次（只用于事件行/诊断）。
func (session *gameSession) loginWearReassertSlotAbsorbedRound(slot int16) int {
	if session == nil || session.equipLoginWearReassertSlots == nil {
		return 0
	}
	epoch, seen := session.equipLoginWearReassertSlots[slot]
	if !seen {
		return 0
	}
	return epoch.round
}

// reopenLoginWearReassertSlotWindow 把该槽的复述处理窗口重新开在本轮（起点 = now）。
//
// 只动时钟，不动陈述：陈述由随后的 noteLoginWearReassertSlotAbsorbed 刷新（它同时把
// `round` 写成当前轮，因此本轮不会再重开第二次）。窗口起点在**同一轮内**依旧不滑动
// —— 这与 `equip_login_wear_reassert_anchor.go` 的原始口径一致，只是"一轮"的定义
// 从"一个会话"收到了"一次场景进入"。
func (session *gameSession) reopenLoginWearReassertSlotWindow(slot int16, now time.Time) {
	if session == nil || session.equipLoginWearReassertSlots == nil {
		return
	}
	epoch, seen := session.equipLoginWearReassertSlots[slot]
	if !seen || !epoch.absorbed {
		return
	}
	epoch.absorbedAt = now
	session.equipLoginWearReassertSlots[slot] = epoch
}
