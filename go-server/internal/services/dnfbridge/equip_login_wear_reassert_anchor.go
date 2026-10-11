// 本文件是"登录复述穿戴态"兜底（DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP，闸门判据在
// equip/owner.go 的 equipmentLoginWearReassertNoop）在 **dnfbridge 侧的范围** 收口：
// 只回答一件事 —— 这条同族 op19 在什么时候还允许被判 noop。
//
// 现场（2026-09-28 上线后 16:56:15–17:11:34 CST，`_dist/weapon-unequip-stutter-round4.md`
// §2.3/§2.6）：15 秒窗口本身工作正常（6 次开窗、2 次 noop 命中、武器槽 12 一次都没被真卸），
// 但**窗口之外**到达的同族形状 op19 仍然走进 `equip/owner.go:1046` 的反转端点兜底并真落库：
//
//   - 角色 6 槽 17（100270515）09:09:20.875025325Z `equipMove=unequip changed=true`
//     （那件装备现在躺在背包 0:10，槽 17 在 DB 里至今是空的）；
//   - 角色 1 槽 12（101010856）09:01:49.314243779Z `equipMove=unequip changed=true`
//     （1.05 s 后又被客户端装回）。
//
// 到达时距该会话窗口开启 +46 s / +62 s / +68 s。也就是说
// equip_login_wear_window.go:26-29 那句"登录族包最晚在场景就绪 ack 后约 1.6 s"
// （另一处写 3.1 s）**在现场被证伪了：该族包的到达时间有一条很长的尾巴**。
// 因此把时长调大不是修法（60 s 上限，且只会同步放大误判面）；正确的锚点不是
// "场景就绪 ack + 固定时长"，而是**该装备槽最近一次被权威刷新**。
//
// 形状本身无法区分"复述"与"玩家真实脱下"：`equipment_endpoint_echo_test.go:133-159`
// 把"操作者真实手动脱下"钉成必须继续生效的回归，而它的包体与本族**逐字节同形**
// （背包端点实例 0 + 装备端点实例 = 该槽上那件 item id）。唯一可用的区分量只有**时间**，
// 所以本文件做的两件事都只加在"时间"上：
//
//  1. 锚点 DNFBRIDGE_EQUIP_LOGIN_REASSERT_ANCHOR（默认 slot_refresh）：
//     兜底范围从"场景就绪 ack 之后 WINDOW_MS"扩到"该装备槽的权威纪元之内" ——
//     只要这一格的内容还是服务端最后一次告诉客户端的那个样子（本会话里这一格
//     还没被玩家/客户端接管过），晚到的复述仍然判 noop。
//  2. 每槽 DNFBRIDGE_EQUIP_LOGIN_REASSERT_PER_SLOT（默认 on）：
//     每个装备槽每会话只有**一个**复述处理窗口（起点 = 该槽第一次被吸收，窗口内不滑动，
//     默认 5000 ms）。窗口内该槽的同族包都吸收；窗口过后该槽本会话**不再吸收** ⇒
//     "玩家手动脱下被吃掉"的暴露面从"15 s 内的每一条"降到"每槽最多一次"，
//     而且一定有出口：玩家再拖一次就真的生效，不必等窗口过期。
//
// 今天的行为（= 回退档）由两个回退字面量精确复原：
//
//	DNFBRIDGE_EQUIP_LOGIN_REASSERT_ANCHOR=scene_ready
//	DNFBRIDGE_EQUIP_LOGIN_REASSERT_PER_SLOT=off
//
// （两个都写 off/0/false/no/legacy/disable/none 也等价。）回退档下本文件的每槽状态
// **一个字节都不写**，闸门判定与改动前的 `equipLoginWearFamilyWindowOpenForOp19`
// 逐字节相同；唯一的非线格式差异是开窗事件多两个回显键（见 equip_login_wear_window.go）。
package dnfbridge

import (
	"os"
	"strings"
	"time"

	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
)

// 两个开关名与档位字面量。
const (
	// loginWearReassertAnchorEnv 决定兜底范围的**锚点**。
	loginWearReassertAnchorEnv = "DNFBRIDGE_EQUIP_LOGIN_REASSERT_ANCHOR"
	// loginWearReassertPerSlotEnv 决定兜底范围是否**按装备槽**结算。
	loginWearReassertPerSlotEnv = "DNFBRIDGE_EQUIP_LOGIN_REASSERT_PER_SLOT"

	// loginWearReassertAnchorSlotRefresh 是默认（修复档）：
	// 锚点 = 该装备槽最近一次被权威刷新（登录引导 + 每一次吸收后跟着发的那份刷新）。
	loginWearReassertAnchorSlotRefresh = "slot_refresh"
	// loginWearReassertAnchorSceneReady 是回退档：锚点 = 场景就绪 ack（今天）。
	loginWearReassertAnchorSceneReady = "scene_ready"

	// loginWearReassertPerSlotOn 是默认（修复档）：每个装备槽每会话一个复述处理窗口。
	loginWearReassertPerSlotOn = "on"
	// loginWearReassertPerSlotOff 是回退档：不按槽结算（今天）。
	loginWearReassertPerSlotOff = "off"
)

// loginWearReassertEchoIntervalMillis 是"每槽复述处理窗口"的时长（毫秒）。
//
// 依据：该族包在一轮登录里是**成串**来的，实测串内间隔 0.9–1.5 s、整串跨度 ≤3.1 s
// （`_dist/weapon-unequip-diagnosis.md` §1.5：61423 → 61550 → 61575 同一会话三条），
// 5 s 就是这条串的现场上界，也正是 2026-09-25..28 那一代的窗口长度
// （loginWearReassertWindowLegacyMillis）—— 所以直接复用它，不新增旋钮。
// 窗口起点不随窗口内的重复包滑动：否则玩家每隔 4 s 试一次就能把窗口永远续期。
const loginWearReassertEchoIntervalMillis = loginWearReassertWindowLegacyMillis

// loginWearReassertAnchorMode 回答生效的锚点档。运行时读 env，不缓存。
//
// 只有**显式**写 scene_ready（及其别名）才回退到今天；其余取值（含未设置、写错的字串、
// slot_refresh）都是修复档 —— 默认即修复，回退必须显式。
func loginWearReassertAnchorMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(loginWearReassertAnchorEnv))) {
	case loginWearReassertAnchorSceneReady, "scene", "ack", "legacy", "off", "0", "false", "no", "disable", "disabled", "none":
		return loginWearReassertAnchorSceneReady
	default:
		return loginWearReassertAnchorSlotRefresh
	}
}

// loginWearReassertPerSlotMode 回答生效的每槽档。运行时读 env，不缓存。
// 只有**显式**写 off（及其别名）才回退到今天；其余取值都是修复档。
func loginWearReassertPerSlotMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(loginWearReassertPerSlotEnv))) {
	case loginWearReassertPerSlotOff, "0", "false", "no", "legacy", "disable", "disabled", "none":
		return loginWearReassertPerSlotOff
	default:
		return loginWearReassertPerSlotOn
	}
}

// loginWearReassertAnchor 只用于启动日志/诊断（Service 上的透传）。
func (s *Service) loginWearReassertAnchor() string {
	return loginWearReassertAnchorMode()
}

// loginWearReassertPerSlot 只用于启动日志/诊断（Service 上的透传）。
func (s *Service) loginWearReassertPerSlot() string {
	return loginWearReassertPerSlotMode()
}

// loginWearReassertSlotEpoch 是一个装备槽在本会话里的"复述处理窗口"。
type loginWearReassertSlotEpoch struct {
	// absorbedAt 是该槽**本轮**第一次被兜底吸收的时刻（窗口起点，不随本轮内的重复包滑动）。
	// 2026-09-28 第四轮：起点随"场景就绪 ack = 新一轮"重开一次，
	// 见 equip_login_wear_reassert_round.go。
	absorbedAt time.Time
	// absorbed 表示该槽已经开过一次复述处理窗口。
	absorbed bool
	// statement / statementRecorded 记录该槽**最近一次**被吸收的那条客户端陈述
	// （规范化身份字段，见 equip_login_wear_reassert_repeat.go）。它只服务于第三层
	// 收口"同一条陈述的重复不再受每槽时钟限制"；回退档（REPEAT=gate）下这两个字段
	// 一个都不被读取，行为与今天逐字节相同。
	statement         loginWearReassertStatement
	statementRecorded bool
	// round / roundRecorded 记该槽**最近一次被吸收**发生在哪一轮
	// （`gameSession.equipLoginWearReassertRound` 的取值），只服务于第四层收口
	// "客户端又进了一次场景 ⇒ 该槽在新一轮里重开一次窗口"。
	//
	// `roundRecorded` 与 `statementRecorded` 同一口径（零值安全）：**没有轮次记账的账本
	// 一律不重开** —— 方向是"宁可少拦"（玩家的出口优先），而且生产路径上不存在这种账本：
	// 每槽账本只有 `noteLoginWearReassertSlotAbsorbed` 会写，而它只在**已经开过窗**
	// （= 收到过场景就绪 ack）之后才会被调用，那一刻轮次已经 ≥ 1。
	// 回退档（ROUND=session）下轮次恒为 0、`roundRecorded` 也随之为 false，
	// `loginWearReassertSlotRoundReassertAbsorbable` 因此恒假 —— 一个字节都不多写。
	// 见 equip_login_wear_reassert_round.go。
	round         int
	roundRecorded bool
}

// loginWearReassertScope 是一次范围判定的结果。
type loginWearReassertScope struct {
	// absorb 为真表示这条 op19 可以按"登录复述"交给域层闸门（透传 LoginWearWindowOpen）。
	absorb bool
	// late 为真表示它是靠**每槽锚点**在会话窗口之外被吸收的（现场 +46 s 那一类）。
	late bool
	// repeat 为真表示它是靠**同一条陈述的重复**被吸收的（每槽时钟已过期；见
	// equip_login_wear_reassert_repeat.go，这是 2026-09-28 18:32 部署件上线后的那一类）。
	repeat bool
	// round 为真表示它是靠**新一轮的场景进入**被吸收的：该槽的每槽时钟已过期，但客户端
	// 又收到过一次场景就绪 ack（新一轮），并且这一轮里重述的还是**同一句装备端点陈述**
	// （点名的空端点可以不同）—— 见 equip_login_wear_reassert_round.go，
	// 这是 2026-09-28 20:29 部署件上线后现场 10:41:41.750 那一类。
	round bool
	// absorbedRound 是该槽上一次被吸收发生在哪一轮（只在 round 为真时有意义，供事件行读）。
	absorbedRound int
	// slot 是这条包在装备端点（list3）上指名的槽位；slotOK=false 表示解析不出槽位
	// （形状已经判过了，这里只是取槽；取不到就不按槽结算）。
	slot   int16
	slotOK bool
}

// loginWearReassertFamilyWornSlot 取出该族 op19 在装备端点（list3）上指名的槽位。
// 复用 dnfinventory.DecodeMoveItemspaceRequest（与形状判据同一份解码器 ⇒ 偏移只有一处真相）。
func loginWearReassertFamilyWornSlot(body []byte) (int16, bool) {
	request, err := dnfinventory.DecodeMoveItemspaceRequest(body)
	if err != nil || request.DestinationListType != currentSocketListEquipment {
		return 0, false
	}
	return request.DestinationSlotIndex, true
}

// loginWearReassertSlotEpochOpen 回答该槽在这次会话里是否还允许被兜底吸收（每槽档）。
//
//	该槽从没被吸收过          ⇒ 允许（这就是"每槽一次"的那一次）；
//	该槽吸收过，且还在窗口内  ⇒ 允许（客户端同一串复述里的重复包）；
//	窗口已经过去              ⇒ 不允许 ⇒ 交回域层按真实移动处理（玩家的出口）。
func (session *gameSession) loginWearReassertSlotEpochOpen(slot int16, now time.Time) bool {
	if session == nil || session.equipLoginWearReassertSlots == nil {
		return true
	}
	epoch, seen := session.equipLoginWearReassertSlots[slot]
	if !seen || !epoch.absorbed {
		return true
	}
	return !now.After(epoch.absorbedAt.Add(time.Duration(loginWearReassertEchoIntervalMillis) * time.Millisecond))
}

// noteLoginWearReassertSlotAbsorbed 记下"该槽这一次被兜底吸收了"。
// 只在第一次吸收时置起点：窗口内重复包不许把起点往后推（否则窗口可被无限续期）。
//
// statement 是该槽最近一次被吸收的客户端陈述：它每次都刷新（"客户端现在这句话是什么"），
// 而 absorbedAt 只在第一次写。两个字段的生命周期故意不同，见
// equip_login_wear_reassert_repeat.go 的形状语义。
func (session *gameSession) noteLoginWearReassertSlotAbsorbed(slot int16, now time.Time, statement loginWearReassertStatement) {
	if session == nil {
		return
	}
	if session.equipLoginWearReassertSlots == nil {
		session.equipLoginWearReassertSlots = make(map[int16]loginWearReassertSlotEpoch, 4)
	}
	round := session.equipLoginWearReassertRound
	epoch, seen := session.equipLoginWearReassertSlots[slot]
	if seen && epoch.absorbed {
		epoch.statement = statement
		epoch.statementRecorded = true
		// 轮次随每次吸收刷新（"该槽本轮已经吸收过"是本轮不重开第二次的依据）；
		// 窗口起点 absorbedAt 仍然只在第一次写 —— 同一轮内的重复包不许把窗口续期。
		epoch.round = round
		epoch.roundRecorded = true
		session.equipLoginWearReassertSlots[slot] = epoch
		return
	}
	session.equipLoginWearReassertSlots[slot] = loginWearReassertSlotEpoch{
		absorbed:          true,
		absorbedAt:        now,
		statement:         statement,
		statementRecorded: true,
		round:             round,
		roundRecorded:     true,
	}
}

// loginWearReassertScopeForOp19 是范围判定的唯一入口（由
// equipLoginWearFamilyWindowOpenForOp19 调用；inWindow 是今天那套会话级窗口的判定结果）。
//
// 判定顺序（两层都必须通过）：
//
//  1. 范围：会话级窗口之内（今天）**或**每槽锚点纪元之内（默认档新增）。
//     每槽锚点档只与 WINDOW=repeat 组合：WINDOW=once 的字面语义（本会话只吸收一条）
//     不许被这条扩展悄悄改掉，因此 once 档下锚点开关是惰性的。
//     会话从未开过窗（`equipLoginWearWindowOpenedAt` 为零，例如根本没有收到场景就绪 ack）
//     时两层都不生效 —— 闸门保持惰性，与今天完全一致。
//  2. 每槽：该槽的复述处理窗口还开着（默认档）。窗口起点 = 该槽**本轮**第一次被吸收的时刻。
//  3. 重复（2026-09-28 18:57 部署件，默认档）：该槽时钟过期后，**与已吸收那条逐字段相同的
//     重复陈述**仍然吸收（`DNFBRIDGE_EQUIP_LOGIN_REASSERT_REPEAT=absorb`，一键回退
//     `gate`）。见 equip_login_wear_reassert_repeat.go。
//  4. 轮次（2026-09-28 20:27 部署件，默认档）：该槽时钟过期后，如果客户端**又收到过**
//     一次场景就绪 ack（新一轮），且这一轮里重述的是同一句**装备端点**陈述（点名的空端点
//     可以不同），则重开该槽窗口（`DNFBRIDGE_EQUIP_LOGIN_REASSERT_ROUND=absorb`，
//     一键回退 `session`）。见 equip_login_wear_reassert_round.go。
//
// 命中即**记账**（每槽窗口起点），因此调用方必须先拿判定的结果再决定是否吸收。
func (s *Service) loginWearReassertScopeForOp19(session *gameSession, body []byte, inWindow bool) loginWearReassertScope {
	if session == nil {
		return loginWearReassertScope{}
	}
	scope := loginWearReassertScope{}
	if slot, ok := loginWearReassertFamilyWornSlot(body); ok {
		scope.slot, scope.slotOK = slot, true
	}
	late := false
	if !inWindow {
		if loginWearReassertAnchorMode() != loginWearReassertAnchorSlotRefresh ||
			!loginWearReassertWindowRepeatEnabled() ||
			session.equipLoginWearWindowOpenedAt.IsZero() {
			return scope
		}
		late = true
	}
	// 每槽记账只在真正要按槽结算时才读时钟/写状态：回退档（PER_SLOT=off）下这里
	// 一个字节都不写、时钟一次都不读。
	if loginWearReassertPerSlotMode() == loginWearReassertPerSlotOn {
		if !scope.slotOK {
			return scope
		}
		statement, statementOK := loginWearReassertStatementOf(body)
		now := s.gameplayNow()
		if !session.loginWearReassertSlotEpochOpen(scope.slot, now) {
			// 该槽的每槽时钟已经过期。第三层收口（默认档）：如果这条 op19 与**该槽
			// 最近一次被吸收的那条陈述**逐字段相同，它仍然是同一次复述 —— 这条
			// 陈述的内容（"槽 D 上是 X"）在权威状态里已经成立，没有新信息可执行，
			// 因此继续吸收，而不是交给反转端点兜底去真卸下。
			// 现场 10:37:20.896（+6.546 s）与 10:38:29.707（+75.4 s）两条就是它。
			//
			// 第四层收口（默认档，见 equip_login_wear_reassert_round.go）：客户端又收到过
			// 一次场景就绪 ack（新一轮）时，**这一轮里第一次**重述同一句**装备端点**陈述
			// 也要重开该槽窗口 —— 现场 10:41:41.750 的 142 → 143 就是这一层；
			// 第三层按"点名的空端点也算身份"判，因此对它是惰性的。
			switch {
			case statementOK && session.loginWearReassertSlotRepeatAbsorbable(scope.slot, statement):
				scope.repeat = true
			case statementOK && session.loginWearReassertSlotRoundReassertAbsorbable(scope.slot, statement):
				scope.absorbedRound = session.loginWearReassertSlotAbsorbedRound(scope.slot)
				scope.round = true
				// 轮内仍不滑动：这里只把起点重开到"本轮第一次"。
				session.reopenLoginWearReassertSlotWindow(scope.slot, now)
			default:
				return scope
			}
		}
		if !statementOK {
			return scope
		}
		session.noteLoginWearReassertSlotAbsorbed(scope.slot, now, statement)
	}
	scope.absorb = true
	scope.late = late
	return scope
}
