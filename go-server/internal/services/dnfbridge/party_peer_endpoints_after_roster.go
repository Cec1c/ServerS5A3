package dnfbridge

import (
	"bytes"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// 本文件实现 M1：**formation（建队/入队/委任队长/成员变更）里，在 op9 名册之后再补发
// 一次 op11**（class0 / `msg_id=11` PARTY_IP_INFO）。
//
// 为什么（跨实现对照 D6）：A21 在同一条 formation 序列里把 0x0B 发了**两次**
// （`S4A21-完整包-20260923\源码\服务端\Server\DfoServer\Network\Handlers\PartyHandler.cs`
// `:2531-2549` = roster 前、`:2571-2592` = roster 后），作者把理由写在 `:2571-2574`：
//
//	The final type-0 roster has now created/replaced every member object. Reapply each
//	recipient's exact bootstrap endpoint bytes so the binding lands on the final objects
//	rather than a temporary pre-roster object.
//
// 86JP 只发一次（`:1482-1561`，与我们的现行为相同），所以这一档是"向更完整的那个
// 参考实现对齐"的单变量实验：**默认 off = 现行为逐字节不变**（日志、包序、body 全不变）。
//
// 边界（硬约束）：
//   - **只挂 formation**：实现点是 `sendRuntimePartySnapshotLocal`（`party_online.go`），
//     它只被 `sendRuntimePartySnapshot` 调用；城镇/选角/回城**场景刷新**走的是另一个
//     函数 `sendRuntimePartyRosterLocal`，那条路径上的 op11 禁令
//     （`party_online.go` "The dynamic UDP endpoints in op11 are a party-formation handshake,
//     not a general scene refresh…"）一个字都不动。
//   - `repeat` 档**不新增包型**：复用同一个 `buildRuntimePartyPeerEndpointInfoForReceiver`，
//     只多发一个 `0x000b`。
const partyPeerEndpointsAfterRosterEnv = "DNFBRIDGE_PARTY_PEER_ENDPOINTS_AFTER_ROSTER"

const (
	partyPeerEndpointsAfterRosterValueOff    = "off"
	partyPeerEndpointsAfterRosterValueRepeat = "repeat"
)

// partyPeerEndpointsAfterRosterMode 是 partyPeerEndpointsAfterRosterEnv 的归一化取值。
type partyPeerEndpointsAfterRosterMode int

const (
	// partyPeerEndpointsAfterRosterOff 是默认档：formation 里 op11 只发一次（现行为）。
	partyPeerEndpointsAfterRosterOff partyPeerEndpointsAfterRosterMode = iota
	// partyPeerEndpointsAfterRosterRepeat 在 op9 之后再补发一次**同一份**端点信息。
	partyPeerEndpointsAfterRosterRepeat
)

func (mode partyPeerEndpointsAfterRosterMode) String() string {
	if mode == partyPeerEndpointsAfterRosterRepeat {
		return partyPeerEndpointsAfterRosterValueRepeat
	}
	return partyPeerEndpointsAfterRosterValueOff
}

func (mode partyPeerEndpointsAfterRosterMode) repeats() bool {
	return mode == partyPeerEndpointsAfterRosterRepeat
}

// readPartyPeerEndpointsAfterRosterMode 解析 DNFBRIDGE_PARTY_PEER_ENDPOINTS_AFTER_ROSTER。
// **默认 off**：空/未设置/非法值都是现行为，只有显式写 `repeat` 才补发第二次 op11。
func readPartyPeerEndpointsAfterRosterMode(value string) partyPeerEndpointsAfterRosterMode {
	if strings.EqualFold(strings.TrimSpace(value), partyPeerEndpointsAfterRosterValueRepeat) {
		return partyPeerEndpointsAfterRosterRepeat
	}
	return partyPeerEndpointsAfterRosterOff
}

// partyPeerEndpointsAfterRoster 返回当前 Service 的归一化档位（零值 options 的
// 测试/探针路径 = off = 现行为）。
func (s *Service) partyPeerEndpointsAfterRoster() partyPeerEndpointsAfterRosterMode {
	if s == nil {
		return partyPeerEndpointsAfterRosterOff
	}
	return s.options.partyPeerEndpointsAfterRoster
}

// sendRuntimePartyPeerEndpointInfo 是 op11 的**唯一出口**（M1 之前它内联在
// `sendRuntimePartySnapshotLocal` 的 beforeFrame 里；抽出来只为让 M1 复用同一份 body 与
// 同一条日志）。
//
// ordering 与该次下发在序列里的位置由 `DNFBRIDGE_PARTY_FORMATION_ORDER` 的档位给出
// （见 party_formation_order.go）：
//   - `vendor`（默认）：`source=managed_runtime_party_snapshot_phase1`、
//     `ordering=all_members_op11_barrier_before_any_op9_then_op153`（op11 是 phase1 barrier）；
//   - `vendor_wire`：同样 phase1 标签（厂商日志实际字节序档）；
//   - `legacy`：`source=runtime_party_snapshot`、`ordering=op153_then_op11_before_op9`（逐字回退）。
//
// `peer_count` 直接取 body 第 0 字节（= 实际写进 op11 的员数），与厂商日志的
// `peer_count=1` 可逐字对照；`include_self` 打出 `DNFBRIDGE_PARTY_PEER_ENDPOINT_INCLUDE_SELF`
// 的档位，让"这一条为什么有/没有自己那一行"在实测日志里直接可判。
func (s *Service) sendRuntimePartyPeerEndpointInfo(
	session *gameSession,
	state alignedcmd.PartyState,
	order partyFormationOrderMode,
) ([]byte, error) {
	return s.sendRuntimePartyPeerEndpointInfoAtStage(session, state, order, partyPeerEndpointStageInitial)
}

// sendRuntimePartyPeerEndpointInfoAtStage keeps the existing packet-log event
// and wire/return behavior while attaching a bounded post-send diagnostic to
// the actual call site. The stage is explicit so the roster repeat cannot be
// mistaken for the initial phase-one formation send.
func (s *Service) sendRuntimePartyPeerEndpointInfoAtStage(
	session *gameSession,
	state alignedcmd.PartyState,
	order partyFormationOrderMode,
	stage partyPeerEndpointStage,
) ([]byte, error) {
	current := s.currentPartyRetirementGuard(session, &state)
	if !current() {
		return nil, nil
	}
	body := s.buildRuntimePartyPeerEndpointInfoForReceiver(session, state)
	peerCount := 0
	if len(body) > 0 {
		peerCount = int(body[0])
	}
	s.logPacketEvent("game-runtime-party-peer-endpoints-send",
		"conn_id", session.connID,
		"char_id", session.selectedCharacterID,
		"party_id", state.PartyID,
		"member_count", len(runtimePartyMembers(state)),
		"peer_count", peerCount,
		"body_len", len(body),
		"msg_id", 0x000b,
		"classification", 0,
		"include_self", s.partyPeerEndpointIncludeSelf().String(),
		"ordering", order.orderingLabel(),
		"source", order.phase1Source())
	sent, err := s.sendRetiredPartyPacket(session, 0x000b, body, &state, current, false)
	if !sent && err == nil {
		return nil, nil
	}
	s.logPartyPeerEndpointSendDiagnostic(session, state, stage, body, err)
	return body, err
}

// sendRuntimePartyPeerEndpointsAfterRoster 是 M1 的动作点：op9 名册已经发出之后，
// 再用**同一个 builder** 生成一次端点信息并补发。
//
// mode 由调用方显式传入（`sendRuntimePartySnapshot` 传 Service 档位，
// `sendRuntimePartySnapshotWithoutPeerEndpointRepeat` 固定传 off）：这样 M2 的重播路径
// 不会因为 M1 打开而多发一帧，两个开关能各自单独判读。
//
// bootstrap 是第一次（roster 前）那份 body。参考实现的措辞是 "reapply each recipient's
// exact bootstrap endpoint bytes"，所以这里把"重建出来的 body 是否与首份逐字节相同"
// 记进日志（`body_matches_bootstrap`）：正常路径下它必须恒为 true；一旦为 false，
// 说明 state/中继在两次下发之间变过，那条日志就是下一次判读的第一手线索。
//
// 发送的是**重建出来的**那份（而不是直接重发 bootstrap 字节）：两者相同时逐字节等价，
// 不同时以"当前权威 state"为准，避免把一份已经过期的端点表再钉回去。
func (s *Service) sendRuntimePartyPeerEndpointsAfterRoster(
	session *gameSession,
	state alignedcmd.PartyState,
	bootstrap []byte,
	mode partyPeerEndpointsAfterRosterMode,
) error {
	if session == nil || !mode.repeats() {
		return nil
	}
	order := s.partyFormationOrder()
	ordering := order.afterRosterOrderingLabel()
	body, err := s.sendRuntimePartyPeerEndpointInfoAtStage(
		session,
		state,
		order,
		partyPeerEndpointStageAfterRosterRepeat,
	)
	if err != nil {
		return err
	}
	s.logPacketEvent("party peer endpoints re-applied after roster",
		"conn_id", session.connID,
		"char_id", session.selectedCharacterID,
		"party_id", state.PartyID,
		"member_count", len(runtimePartyMembers(state)),
		"body_len", len(body),
		"bootstrap_body_len", len(bootstrap),
		"body_matches_bootstrap", bytes.Equal(body, bootstrap),
		"msg_id", 0x000b,
		"classification", 0,
		"ordering", ordering,
		"source", "dnfbridge_party_peer_endpoints_after_roster_repeat")
	return nil
}
