package dnfbridge

import (
	"os"
	"strconv"
	"strings"
	"time"

	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
)

// 本文件实现**甲：镜像回穿抑制**，以及**乙：有界窗口**所需的窗口度量。
//
// ## 甲：为什么要挡"我们刚脱下，客户端又把它穿回去"
//
// 现场（2026-09-29T10:35，逐条原文）：
//
//	10:35:31.967 equipMove=unequip item=100070551 src=(0,18,IV=0)         dst=(3,14,IV=100070551)
//	10:35:32.733 equipMove=equip   item=100070551 src=(0,19,IV=100070551) dst=(3,14,IV=0)   ← 0.765 s 后穿回
//	10:35:33.454 unequip → 10:35:34.049 equip（0.595 s）→ 10:35:34.724 unequip → 10:35:37.136 equip（2.412 s）
//
// 那条 `equip` 用的是**形状 c**（`src=(0,背包格,IV=物品) dst=(3,装备槽,IV=0)`）：厂商参考实现
// （`ServerS4A21-master/.../InventoryMoveService.cs:899-906` `IsEquipmentClientEchoWhenSourceMissing`）
// 把这一族判 **ClientEcho（拒绝、一字节不改）** —— 它是客户端在复述**脱下之前**的本地视图。
// 我们只在"背包端点权威为空"时才按 ClientEcho noop（`equipmentEndpointIdentityEcho`）；
// 而我们刚把物品放进那个背包格之后，闸门不再命中 ⇒ 真落库 `equip` ⇒ 玩家看到
// "脱下后闪一下又穿回身上"。
//
// **判据只用我们自己的落库记录**：当我们判定一条形状 b 要**执行成真脱下**时，
// 记下 (物品, 装备槽, 时刻)；随后若来一条形状 c 点名的正是同一件 + 同一槽、且间隔在窗口内，
// 就把它判 `mirror_requip_echo` noop。与客户端声明、与时间窗、与 occurrence 都无关。
//
// 窗口 N 的取法（**从数据量出来**，全部 16 对 unequip→同物品 equip 的间隔）：
//
//	0.595 / 0.683 / 0.765 / 0.800 s   ← 今天这场拉锯（机器节奏：4 次里 3 次 < 0.8 s）
//	1.045 s（09-28 09:01:49）
//	2.282 / 2.405 / 2.863 / 3.328 / 3.358 / 2.412 s   ← 同族（含今天 2.412 s）
//	57.439 s  87.691 s  2296 s  2943 s  10459 s       ← 玩家真的想再穿上 / 其它流程
//
// ⇒ 快速簇上界 **3.358 s**，下一档直接跳到 **57.4 s**。取 **4.0 s**：覆盖全部快速簇并留余量，
// 离下一档还有 14 倍。窗口内的间隔会打进日志（`interval_ms`），便于操作者判读是否过宽。
//
// 开关：
//
//	DNFBRIDGE_EQUIP_MIRROR_REQUIP_ECHO      on/未设/任意值 = 修复档；off/0/false/no/legacy/… = 关闭
//	DNFBRIDGE_EQUIP_MIRROR_REQUIP_ECHO_MS   窗口毫秒数（默认 4000；非法值回落默认）
const (
	mirrorReequipEchoSwitchEnv = "DNFBRIDGE_EQUIP_MIRROR_REQUIP_ECHO"
	mirrorReequipEchoWindowEnv = "DNFBRIDGE_EQUIP_MIRROR_REQUIP_ECHO_MS"
	mirrorReequipEchoWindowMS  = 4000
	// mirrorReequipEchoWindowMaxMS 是允许配置的上界（60 s）：再大就会把"玩家真的想再穿上"
	// 也一起挡掉（实测下一档是 57.4 s）。
	mirrorReequipEchoWindowMaxMS = 60000
)

func mirrorReequipEchoEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(mirrorReequipEchoSwitchEnv))) {
	case "off", "0", "false", "no", "legacy", "disable", "disabled", "none":
		return false
	default:
		return true
	}
}

func mirrorReequipEchoWindow() time.Duration {
	raw := strings.TrimSpace(os.Getenv(mirrorReequipEchoWindowEnv))
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > mirrorReequipEchoWindowMaxMS {
		value = mirrorReequipEchoWindowMS
	}
	return time.Duration(value) * time.Millisecond
}

// wornSlotClaimIsMirrorShape 判定**形状 c**：`srcList ∈ {0,1}`、`dstList = 3`、count = 1、
// `srcIV > 0`（身份写在背包端点）、`dstIV == 0`（装备端点为空）。
// 与域侧 `equipmentEndpointIdentityEcho` 的客户端形状判据一致。
func wornSlotClaimIsMirrorShape(body []byte) (itemID int32, slot int16, ok bool) {
	request, err := dnfinventory.DecodeMoveItemspaceRequest(body)
	if err != nil {
		return 0, 0, false
	}
	if request.SourceListType != loginWearFamilySourceListMain && request.SourceListType != loginWearFamilySourceListAvatar {
		return 0, 0, false
	}
	if request.DestinationListType != currentSocketListEquipment {
		return 0, 0, false
	}
	if request.MoveCount != 1 || request.SourceInstanceValue <= 0 || request.DestinationInstanceValue != 0 {
		return 0, 0, false
	}
	return request.SourceInstanceValue, request.DestinationSlotIndex, true
}

// noteWornSlotClaimWillUnequip 记下"我们马上要把这件从这一格脱下来"。
// 只在**判定为要执行**（不是吞掉）的形状 b 上调用；见 game_aligned.go。
func (s *Service) noteWornSlotClaimWillUnequip(session *gameSession, body []byte) {
	if session == nil || !mirrorReequipEchoEnabled() {
		return
	}
	request, err := dnfinventory.DecodeMoveItemspaceRequest(body)
	if err != nil {
		return
	}
	session.mirrorReequipItemID = request.DestinationInstanceValue
	session.mirrorReequipSlot = request.DestinationSlotIndex
	session.mirrorReequipAt = s.gameplayNow()
}

// mirrorReequipEchoForOp19 回答"本条 op19 是不是我们刚脱下那件的镜像回穿"，并在命中时
// 打一行可判读的日志（含实际间隔）。命中即消费（清记录），避免一条记录挡掉后续真实操作。
func (s *Service) mirrorReequipEchoForOp19(session *gameSession, body []byte) bool {
	if session == nil || !mirrorReequipEchoEnabled() || session.mirrorReequipAt.IsZero() {
		return false
	}
	itemID, slot, ok := wornSlotClaimIsMirrorShape(body)
	if !ok {
		return false
	}
	if itemID != session.mirrorReequipItemID || slot != session.mirrorReequipSlot {
		return false
	}
	interval := s.gameplayNow().Sub(session.mirrorReequipAt)
	if interval < 0 || interval > mirrorReequipEchoWindow() {
		// 超出窗口：不是镜像回穿（实测下一档是 57.4 s）。记录留着没用，清掉。
		session.mirrorReequipAt = time.Time{}
		s.logPacketEvent("equip-move/mirror-requip",
			"conn_id", session.connID,
			"slot", slot,
			"item", itemID,
			"decision", "apply",
			"reason", "outside-window",
			"interval_ms", interval.Milliseconds(),
			"window_ms", mirrorReequipEchoWindow().Milliseconds())
		return false
	}
	session.mirrorReequipAt = time.Time{}
	s.logPacketEvent("equip-move/mirror-requip",
		"conn_id", session.connID,
		"slot", slot,
		"item", itemID,
		"decision", "swallow",
		"reason", "mirror-requip",
		"interval_ms", interval.Milliseconds(),
		"window_ms", mirrorReequipEchoWindow().Milliseconds())
	return true
}
