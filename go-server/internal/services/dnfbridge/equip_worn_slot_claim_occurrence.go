package dnfbridge

import (
	"os"
	"strconv"
	"strings"
	"time"

	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
)

// 本文件实现"脱下装备（drag-off）"那条链路的**重试档（方案 C）**：把
// `worn_slot_reassert` / `login_wear_reassert_noop` 这两条"复述 vs 真搬运"闸门从
// "一律吞掉"改成"**同一个装备槽在同一个登录纪元里只吞第 1 条，第 2 条起放行**"。
//
// 为什么需要它（现场，均为真字节）：
//
//   - 客户端从不发 `src=list3` 的搬运（13 小时 / 366 条 op19 RECV 里 `(srcList,dstList)`
//     没有一条 `(3,x)`）。它表达"把身上那件拖下来"用的是
//     `src=(0,背包槽,IV=0) count=1 dst=(3,装备槽,IV=物品)`（下称**形状 b**）。
//   - 厂商参考实现（`ServerS4A21-master/.../InventoryMoveService.cs:97-141`）把形状 b
//     判成**反转端点搬运 = 真·脱下**（把装备槽那件搬进客户端指名的背包格）；只有
//     `SourceInstanceValue > 0` 的那一支才判 `ClientEcho` 拒绝。
//   - 我们在 09-28 为了让"每次重登把武器卸进背包"停掉，把形状 b 整族吞成 noop
//     （`worn_slot_reassert_gate.go:133-137` 写明这是**操作者已知情接受**的残余风险，
//     并假设"玩家可以改用源端为装备槽的拖法"——**不存在这种拖法**）。
//     结果：09-29 全天 `equipMove=unequip` **0 条**（09-28 为 12 条），玩家拖不动，
//     装备留在服务端的"穿着"认知里 ⇒ 不能强化/增幅/附魔/分解，重选角色又穿回身上。
//
// 判据为什么是"同槽出现序号"（量出来的，不是猜的）：
//
//	场景                                    同槽形状 b 条数
//	登录批（09-28 16:36 那批）                每槽 1 条，按槽号顺序
//	玩家拖不动在重试（09-29 10:03 / 09:45）   槽 14 在 9 秒内 7 条（a/b/c 混发），槽 17/25 各 3-4 条
//
// 所以第 1 条按复述吞（登录批继续受保护），第 2 条起按厂商口径执行（重试 = 玩家的真实动作）。
//
// **epoch 边界（必须写清，否则会把 09-28 老症状带回来）**：
//
//	epoch = 连接（一次登录/选角）+ 场景就绪轮次（session.equipLoginWearReassertRound）。
//	- 新连接（登录、重新选角、掉线重连）⇒ connID 变 ⇒ 账本清零；
//	- 每次场景就绪 ack（legacy type=1345 ⇒ 客户端一次场景进入）⇒ 轮次自增 ⇒ 账本清零。
//	⇒ 连续换角色时，第 2 个角色的第 1 条永远算第 1 条。**不按进程启动计时**。
//
// 可观测性（操作者判读档位是否生效的第一手依据，每次命中形状 b 都打一行）：
//
//	EVENT kind=equip-move/worn-slot-reassert conn_id=… slot=<n> occurrence=<k>
//	      decision=<swallow|apply-vendor> epoch=<conn#round> retry_tier=<retry|swallow_all|off>
//
// 开关（与 `DNFBRIDGE_EMPTY_SLOT_ROW_ENCODING` **正交**，两者互不影响）：
//
//	DNFBRIDGE_EQUIP_MOVE_WORN_SLOT_REASSERT
//	  - 未设/空/任意未知值/on/retry        ⇒ **重试档（默认，修复档）**：吞第 1 条，第 2 条起放行
//	  - all / swallow / swallow_all        ⇒ **全吞档**：与今天（09-28 起）的默认行为逐字节相同
//	  - off/0/false/no/legacy/disable/disabled/none ⇒ **关闭档**：闸门完全惰性 = 今天写 off 的行为
//	    （形状 b 一律被反转端点兜底执行成 unequip）
const wornSlotClaimRetryTierEnv = "DNFBRIDGE_EQUIP_MOVE_WORN_SLOT_REASSERT"

const (
	wornSlotClaimTierRetry      = "retry"
	wornSlotClaimTierSwallowAll = "swallow_all"
	wornSlotClaimTierOff        = "off"
)

// wornSlotClaimRetryTier 归一化上面的开关（每次调用都读 env，不缓存）。
func wornSlotClaimRetryTier() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(wornSlotClaimRetryTierEnv))) {
	case "all", "swallow", "swallow_all", "swallow-all":
		return wornSlotClaimTierSwallowAll
	case "off", "0", "false", "no", "legacy", "disable", "disabled", "none":
		return wornSlotClaimTierOff
	default:
		return wornSlotClaimTierRetry
	}
}

// wornSlotClaimShape 判定**形状 b**（客户端在声明装备槽的穿戴态）并返回它点名的装备槽。
//
// 判据只读客户端自己写的字段，与域侧 `equipmentWornSlotReassertClaim`
// （worn_slot_reassert_gate.go）逐条一致：
//
//	srcList ∈ {0 主背包, 1 装扮栏}、dstList = 3 装备、count = 1、
//	srcIV == 0（客户端没有声明要搬的东西）、dstIV != 0（在装备端点点名一件物品）。
//
// 真搬运（穿上/换装）把身份写在**源**端点（srcIV != 0）⇒ 一律不匹配，不受本文件影响。
func wornSlotClaimShape(body []byte) (int16, bool) {
	request, err := dnfinventory.DecodeMoveItemspaceRequest(body)
	if err != nil {
		return 0, false
	}
	if request.SourceListType != loginWearFamilySourceListMain && request.SourceListType != loginWearFamilySourceListAvatar {
		return 0, false
	}
	if request.DestinationListType != currentSocketListEquipment {
		return 0, false
	}
	if request.MoveCount != 1 {
		return 0, false
	}
	if request.SourceInstanceValue != 0 {
		return 0, false
	}
	if request.DestinationInstanceValue == 0 {
		return 0, false
	}
	return request.DestinationSlotIndex, true
}

// wornSlotClaimEpochID 是账本的纪元标识（连接 + 场景就绪轮次）。
func wornSlotClaimEpochID(session *gameSession) string {
	if session == nil {
		return ""
	}
	return session.connID + "#" + strconv.Itoa(session.equipLoginWearReassertRound)
}

// noteWornSlotClaimOccurrence 记一次形状 b 的出现，返回"该槽在本纪元的序号"（1 起算）。
//
// 非形状 b ⇒ 返回 0（= 与今天逐字节一致：桥侧不计数、域侧闸门按老口径判定）。
// 不是重试档（合法开关写 all/off）⇒ 也返回 0（`all` = 全吞档要保持今天的默认行为，
// `off` = 闸门本身惰性、这个计数值没有读者）。
//
// 每命中一次形状 b 都写一行 `equip-move/worn-slot-reassert`（见文件头），
// 这是本轮唯一的可证伪抓手：操作者说"还是拖不动"时，先看 occurrence 有没有到 2、
// decision 是不是 apply-vendor，再看别处。
func (s *Service) noteWornSlotClaimOccurrence(session *gameSession, body []byte) int {
	slot, isClaim := wornSlotClaimShape(body)
	if !isClaim {
		return 0
	}
	tier := wornSlotClaimRetryTier()
	if tier != wornSlotClaimTierRetry {
		s.logPacketEvent("equip-move/worn-slot-reassert",
			"conn_id", session.connID,
			"slot", slot,
			"occurrence", 1,
			"decision", "swallow",
			"epoch", wornSlotClaimEpochID(session),
			"retry_tier", tier,
			"reason", "retry_tier_disabled_by_switch")
		return 0
	}
	s.prepareCurrentWornClaimReceiptOwner(session)
	epoch := wornSlotClaimEpochID(session)
	if session.equipWornSlotClaimEpoch != epoch || session.equipWornSlotClaimCounts == nil {
		session.equipWornSlotClaimEpoch = epoch
		session.equipWornSlotClaimCounts = make(map[int16]int, 8)
		session.equipWornSlotClaimBurstSeen = false
	}
	request, _ := dnfinventory.DecodeMoveItemspaceRequest(body)
	_ = request
	// 乙：**有界窗口**。窗口 = 最近一次场景就绪 ack 之后
	// `loginWearReassertWindowDurationMillis()` 毫秒内。实测（文件头 + 报告 §13）：
	// 客户端自动复述批落在 ack **+2.934 s / +4.002 s**（另有一批 +287.5 s，见 §13 残余风险），
	// 玩家手动拖落在 ack **+47.2 s** 起。窗口内 ⇒ 复述（吞）；窗口外单发 ⇒ 按厂商口径执行。
	inWindow := false
	ackDelta := time.Duration(0)
	if session.equipLoginWearWindowOpenedAt.IsZero() {
		// 本连接从未收到场景就绪 ack（异常/极老会话）：**保守按复述**（吞第 1 条，
		// 同槽第 2 条起仍然放行）—— 没有 ack 证据时不主动执行"脱下"。
		// 线上每个会话都有 ack（本份日志 228 次），该分支实际不可达。
		inWindow = true
	} else {
		ackDelta = s.gameplayNow().Sub(session.equipLoginWearWindowOpenedAt)
		inWindow = ackDelta >= 0 && ackDelta <= s.loginWearReassertWindowDuration()
	}
	// 批延续（不依赖窗口的兜底）：与上一条形状 b 落在同一个背包格、间隔 <0.5 s、
	// 装备槽不同 ⇒ 客户端那一批的后续包（实测批内间隔 0.199–0.321 s）。
	request, requestErr := dnfinventory.DecodeMoveItemspaceRequest(body)
	requestOK := requestErr == nil
	// 批延续（不依赖窗口的兜底）：与上一条形状 b 落在同一个背包格、间隔 <0.5 s、
	// 装备槽不同 ⇒ 客户端那一批的后续包（实测批内间隔 0.199–0.321 s）。
	burstContinuation := false
	if requestOK && session.equipWornSlotClaimBurstSeen &&
		request.SourceListType == session.equipWornSlotClaimBurstBagList &&
		request.SourceSlotIndex == session.equipWornSlotClaimBurstBagSlot &&
		slot != session.equipWornSlotClaimBurstSlot &&
		!session.equipWornSlotClaimBurstAt.IsZero() &&
		s.gameplayNow().Sub(session.equipWornSlotClaimBurstAt) < 500*time.Millisecond {
		burstContinuation = true
	}
	if requestOK {
		session.equipWornSlotClaimBurstSeen = true
		session.equipWornSlotClaimBurstBagList = request.SourceListType
		session.equipWornSlotClaimBurstBagSlot = request.SourceSlotIndex
		session.equipWornSlotClaimBurstSlot = slot
		session.equipWornSlotClaimBurstAt = s.gameplayNow()
	}
	session.equipWornSlotClaimCounts[slot]++
	occurrence := session.equipWornSlotClaimCounts[slot]
	// 决策与 reason（操作者判读档位的第一手依据）：
	//   occurrence ≥ 2 ⇒ apply-vendor / occurrence（窗口内的第二道保险）
	//   窗口内         ⇒ swallow      / window
	//   窗口外的批延续 ⇒ swallow      / burst
	//   窗口外、单发   ⇒ apply-vendor / window-outside   ← "拖一次就成功"
	decision, reason := "swallow", "window"
	// An identical repeat of the statement the login-wear layer already absorbed
	// is not player intent. v99+ manual UI drags carry tail=2 and bypass this
	// counter entirely; tail=0 is the automatic login/town reassert family.
	// Keep the late-landing occurrence counter for changed statements and
	// outside-window single moves, but do not let it override repeat/round.
	identicalLoginRepeat := false
	if occurrence > 1 {
		if statement, ok := loginWearReassertStatementOf(body); ok {
			identicalLoginRepeat = session.loginWearReassertSlotRepeatAbsorbable(slot, statement) ||
				s.currentWornClaimReceiptMatches(session, slot, statement)
		}
	}
	switch {
	case occurrence > 1 && identicalLoginRepeat:
		decision, reason = "swallow", "identical-login-reassert"
	case occurrence > 1:
		decision, reason = "apply-vendor", "occurrence"
	case inWindow:
		decision, reason = "swallow", "window"
	case burstContinuation:
		decision, reason = "swallow", "burst"
	default:
		decision, reason = "apply-vendor", "window-outside"
	}
	if decision == "apply-vendor" {
		// 记下"我们马上要把这件从这一格脱下来"，供甲（镜像回穿抑制）使用。
		s.noteWornSlotClaimWillUnequip(session, body)
	}
	s.logPacketEvent("equip-move/worn-slot-reassert",
		"conn_id", session.connID,
		"slot", slot,
		"occurrence", occurrence,
		"decision", decision,
		"reason", reason,
		"epoch", epoch,
		"ack_delta_ms", ackDelta.Milliseconds(),
		"in_window", inWindow,
		"window_ms", s.loginWearReassertWindowDuration().Milliseconds(),
		"burst_continuation", burstContinuation,
		"retry_tier", tier)
	if decision == "apply-vendor" {
		// 域侧契约：≥2 = 放行（两条登录闸门惰性），1 = 吞。
		return 2
	}
	return 1
}
