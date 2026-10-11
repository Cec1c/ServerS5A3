package dnfbridge

import "strings"

// 本文件实现 M2：**客户端一上报 CS op2（`msg_id=2` / 31B SET_UDP_IP_PORT），端口真的
// 变了就把整份队伍信息重播一遍**。
//
// 为什么（跨实现对照 D7）：A21 与 86JP 在 `Handle_SET_UDP_IP_PORT` 里都是
// `TryUpdateMemberP2pPort(...)` → `BroadcastPartyInfo(party)`：
//   - A21 `Network\Handlers\PartyHandler.cs:337-378`（`:360-371` 是重播点），
//   - 86JP `Network\Handlers\PartyHandler.cs:71-110`（`:94-105` 逐字相同）。
//
// 注释写明理由：PartyUdpRelay 在成员不变时会保留既有房间，所以这次刷新只更新
// "自己那一行"的 endpoint，不会轮换已经验证过的中继端口。
// 我们此前只有 `captureCurrentPartyPeerEndpointRegistration`（记录 + 记日志），**不重播**。
//
// 触发条件被刻意收窄为"**端口真的变了**"（`captureCurrentPartyPeerEndpointRegistration`
// 的返回值，含 0 → 非 0 的首次上报）：端口没变时**零重播**，这条有回归钉子。
//
// 与其他开关的关系（单变量纪律）：
//   - **默认 off** = 现行为（只记录 + 记日志，一个字节都不多发）；
//   - 重播走的是既有原语 `sendRuntimePartySnapshot` 一族，**不新造广播**；但重播一律按
//     **单次 op11** 发（对齐参考实现 `BroadcastPartyInfo` 的 `0x99 → 0x0B → 0x09`），
//     因此 M1（`DNFBRIDGE_PARTY_PEER_ENDPOINTS_AFTER_ROSTER=repeat`）在 M2 这条路上不叠加，
//     两个开关可以各自单独判读；
//   - 回城/进城选图收尾窗口（`session.backToVillageEnterSelectPending`）里**不重播**：
//     那条窗口刻意保持安静（`party_online.go` 里 op11 的既有禁令），
//     在它中间再插一轮完整队伍帧会和 `DNFBRIDGE_PARTY_DUNGEON_*` 的时序实验互相打架。
const partyPeerEndpointRebroadcastEnv = "DNFBRIDGE_PARTY_PEER_ENDPOINT_REBROADCAST"

const (
	partyPeerEndpointRebroadcastValueOff = "off"
	partyPeerEndpointRebroadcastValueOn  = "on"
)

// partyPeerEndpointRebroadcastMode 是 partyPeerEndpointRebroadcastEnv 的归一化取值。
type partyPeerEndpointRebroadcastMode int

const (
	// partyPeerEndpointRebroadcastOff 是默认档：收 CS op2 只记录，不重播（现行为）。
	partyPeerEndpointRebroadcastOff partyPeerEndpointRebroadcastMode = iota
	// partyPeerEndpointRebroadcastOn 在端口变化时重播整份队伍信息。
	partyPeerEndpointRebroadcastOn
)

func (mode partyPeerEndpointRebroadcastMode) String() string {
	if mode == partyPeerEndpointRebroadcastOn {
		return partyPeerEndpointRebroadcastValueOn
	}
	return partyPeerEndpointRebroadcastValueOff
}

func (mode partyPeerEndpointRebroadcastMode) enabled() bool {
	return mode == partyPeerEndpointRebroadcastOn
}

// readPartyPeerEndpointRebroadcastMode 解析 DNFBRIDGE_PARTY_PEER_ENDPOINT_REBROADCAST。
// **默认 off**：空/未设置/非法值都是现行为，只有显式写 `on` 才重播。
func readPartyPeerEndpointRebroadcastMode(value string) partyPeerEndpointRebroadcastMode {
	if strings.EqualFold(strings.TrimSpace(value), partyPeerEndpointRebroadcastValueOn) {
		return partyPeerEndpointRebroadcastOn
	}
	return partyPeerEndpointRebroadcastOff
}

// partyPeerEndpointRebroadcast 返回当前 Service 的归一化档位（零值 options 的
// 测试/探针路径 = off = 现行为）。
func (s *Service) partyPeerEndpointRebroadcast() partyPeerEndpointRebroadcastMode {
	if s == nil {
		return partyPeerEndpointRebroadcastOff
	}
	return s.options.partyPeerEndpointRebroadcast
}

// rebroadcastRuntimePartySnapshotAfterPeerEndpointChange 在"这次 CS op2 真的改了端口"之后
// 重播整份队伍信息（参考实现的 `BroadcastPartyInfo` 语义：**每个成员各收一份按收包者定制
// 的名册**，而不是只刷新上报者自己）。
//
// 只在下列前置条件全部成立时动作，任何一条不成立就立刻返回（行为与开关关闭时完全一致）：
//  1. 开关为 `on`；
//  2. 该会话已选定角色，且仍在**一场 ≥2 人的队伍**里（没成形的队伍没有"整份队伍信息"）；
//  3. 不在回城/进城选图收尾窗口里（见文件头）。
//
// 目标集合复用既有原语：先取运行时队伍管理器的快照（`party_manager_runtime.go:391-408`
// 的同一条查询路径），对每个仍在线且 generation 未变的成员，用**该成员自己的** state 调
// `sendRuntimePartySnapshotWithoutPeerEndpointRepeat`；管理器里已经没有这场队伍时只刷新
// 上报者自己，绝不发一份可能已经过期的名册。
func (s *Service) rebroadcastRuntimePartySnapshotAfterPeerEndpointChange(session *gameSession) {
	if session == nil || !s.partyPeerEndpointRebroadcast().enabled() {
		return
	}
	if session.selectedCharacterID == 0 {
		return
	}
	state := runtimePartyStateSnapshot(session)
	if state.PartyID <= 0 || state.PartyID >= int(^uint16(0)) {
		return
	}
	if len(runtimePartyMembers(state)) < 2 {
		return
	}
	registration := currentPartyPeerEndpointSnapshot(session)
	if session.backToVillageEnterSelectPending {
		s.logGameEvent(session, "game-party-peer-endpoint-rebroadcast-skipped",
			"char_id", session.selectedCharacterID,
			"party_id", state.PartyID,
			"udp_port", registration.Port,
			"port_endian", s.partyPeerPortEndian().String(),
			"reason", "back_to_village_enter_select_window")
		return
	}
	targets := 0
	manager := s.runtimePartyManagerForService()
	if snapshot, found := manager.SnapshotByID(uint16(state.PartyID)); found {
		for _, member := range snapshot.Members {
			identity, online := s.onlineGameSessionCharacterSnapshot(member.UserID)
			if !online || identity.session == nil || identity.generation != member.SessionGeneration {
				continue
			}
			if err := s.sendRuntimePartySnapshotWithoutPeerEndpointRepeat(
				identity.session,
				snapshot.StateFor(member.UserID),
			); err != nil {
				s.logGameEvent(session, "game-party-peer-endpoint-rebroadcast-failed",
					"char_id", session.selectedCharacterID,
					"party_id", state.PartyID,
					"member_id", member.UserID,
					"udp_port", registration.Port,
					"error", err)
				return
			}
			targets++
		}
	}
	if targets == 0 {
		if err := s.sendRuntimePartySnapshotWithoutPeerEndpointRepeat(session, state); err != nil {
			s.logGameEvent(session, "game-party-peer-endpoint-rebroadcast-failed",
				"char_id", session.selectedCharacterID,
				"party_id", state.PartyID,
				"member_id", session.selectedCharacterID,
				"udp_port", registration.Port,
				"error", err)
			return
		}
		targets = 1
	}
	s.logGameEvent(session, "game-party-peer-endpoint-rebroadcast",
		"char_id", session.selectedCharacterID,
		"party_id", state.PartyID,
		"member_count", len(runtimePartyMembers(state)),
		"rebroadcast_targets", targets,
		"udp_port", registration.Port,
		"port_endian", s.partyPeerPortEndian().String(),
		"ordering", "op153_then_op11_then_op9_single_endpoint_frame",
		"source", "current_exe_cs_op2_udp_port_changed")
}
