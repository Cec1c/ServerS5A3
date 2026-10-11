package dnfbridge

import (
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// 本文件实现「副本进场补发 op11 remotes」——厂商 9.16 `packet_order` 自述里
// `op2-mode1-all` 与 `op3-all` 之间的那一条（上一轮报告 E5、本轮厂商日志①）。
//
// # 厂商原始证据（只读引用 D:\DNF90-test\server\runtime\logs\packet_log.txt）
//
//	:5368 EVENT kind=game-party-dungeon-entry-prepared conn_id=game-000059 … local_slot=0 char_id=1
//	      release=false packet_order=op16-owner-ack-op5-op23-ff-op2-mode0-all-op2-mode1-all-\
//	      op11-remotes-op3-all-op26-leader-op28-then-all-member-barrier
//	:5387 EVENT kind=game-party-dungeon-entry-prepared conn_id=game-000061 … local_slot=1 char_id=2
//	      release=true（同一条 packet_order）
//	:5361 SEND conn=game-000059 msg_id=11 plain_body_len=23
//	      body=01 0200 c0a80065 c0a80065 7530 1c000000 01 00020000 64
//	:5380 SEND conn=game-000061 msg_id=11 plain_body_len=23
//	      body=01 0100 c0a80065 c0a80065 7531 1b000000 01 00020000 64
//	（同样两条 body 与 formation 阶段 :3337/:3340 的 op11 **逐字节相同**）
//
// 即：厂商在"进入副本"这一条序列里**又发了一次 op11**，位置在 op2 mode1 之后、op3 之前，
// 内容是「只含对端那一行」的 22B 行表（packet_order 里记作 `op11-remotes`）。
//
// # 我们的对应位置
//
// `dungeon_navigation_runtime.go` 的 `sendCurrentPreDungeonContextPlayerState`
// （= class0/op2 mode0 场景对象表 + mode1 actor 绑定）成功之后、op27 之前。
// 改动前我们这一步**什么都不发**（op11 只在 formation 阶段发过一次）。
//
// # 形状复用（不新增包型）
//
// 直接用 formation 那一个 builder：`buildRuntimePartyPeerEndpointInfoForReceiver`
// （`party_online.go`），所以同一份 state 下 body 与 formation 那份**逐字节相同**；
// `DNFBRIDGE_PARTY_PEER_ENDPOINT_INCLUDE_SELF=off`（默认）时就是厂商的
// 「u8 对端数 + 22B/对端行」形状（2 人队 = 23B / peer_count=1）。
//
// # 开关
//
//	`DNFBRIDGE_PARTY_DUNGEON_ENTRY_PEER_ENDPOINTS`
//	  - `vendor`（默认，零值）= 在 op2 mode1 之后补发一次 op11 remotes（= 厂商行为）；
//	  - `legacy` = 不发（逐字节回到改动前：包序、body、日志全同）。
//
// 与既有开关的关系：本开关**独立**于 `DNFBRIDGE_PARTY_*` 组（formation 顺序 / op11 形状 /
// M1 / M2）；它只决定"副本进场这条序列里多不多这一条 op11"，不改变 formation 的任何行为。
const partyDungeonEntryPeerEndpointsEnv = "DNFBRIDGE_PARTY_DUNGEON_ENTRY_PEER_ENDPOINTS"

const (
	partyDungeonEntryPeerEndpointsValueVendor = "vendor"
	partyDungeonEntryPeerEndpointsValueLegacy = "legacy"
)

// partyDungeonEntryPeerEndpointsMode 是 partyDungeonEntryPeerEndpointsEnv 的归一化取值。
type partyDungeonEntryPeerEndpointsMode int

const (
	// partyDungeonEntryPeerEndpointsVendor 是默认档（零值）= 厂商行为：进场补发 op11 remotes。
	partyDungeonEntryPeerEndpointsVendor partyDungeonEntryPeerEndpointsMode = iota
	// partyDungeonEntryPeerEndpointsLegacy = 改动前行为：进场不发 op11（逐字节回退）。
	partyDungeonEntryPeerEndpointsLegacy
)

func (mode partyDungeonEntryPeerEndpointsMode) String() string {
	if mode == partyDungeonEntryPeerEndpointsLegacy {
		return partyDungeonEntryPeerEndpointsValueLegacy
	}
	return partyDungeonEntryPeerEndpointsValueVendor
}

// sendsRemotes 表示进场序列里是否补发那一条 op11 remotes。
func (mode partyDungeonEntryPeerEndpointsMode) sendsRemotes() bool {
	return mode != partyDungeonEntryPeerEndpointsLegacy
}

// readPartyDungeonEntryPeerEndpointsMode 解析 DNFBRIDGE_PARTY_DUNGEON_ENTRY_PEER_ENDPOINTS。
// **默认 vendor（= 厂商行为）**：空/未设置/非法值都是 vendor；只有显式写 `legacy` 才不发。
func readPartyDungeonEntryPeerEndpointsMode(value string) partyDungeonEntryPeerEndpointsMode {
	if strings.EqualFold(strings.TrimSpace(value), partyDungeonEntryPeerEndpointsValueLegacy) {
		return partyDungeonEntryPeerEndpointsLegacy
	}
	return partyDungeonEntryPeerEndpointsVendor
}

// partyDungeonEntryPeerEndpoints 返回当前 Service 的归一化档位（零值 options 的
// 测试/探针路径 = vendor = 默认档）。
func (s *Service) partyDungeonEntryPeerEndpoints() partyDungeonEntryPeerEndpointsMode {
	if s == nil {
		return partyDungeonEntryPeerEndpointsVendor
	}
	return s.options.partyDungeonEntryPeerEndpoints
}

// dungeonEntryVendorPacketOrder 是厂商自己在 `game-party-dungeon-entry-prepared` 里写下的
// 顺序串。我们把它作为**对照字段**打进自己的日志（`vendor_packet_order=`），
// 便于两边 packet_log 并排 diff；我们自己的实际顺序另记在 `packet_order=`。
const dungeonEntryVendorPacketOrder = "op16-owner-ack-op5-op23-ff-op2-mode0-all-op2-mode1-all-op11-remotes-op3-all-op26-leader-op28-then-all-member-barrier"

// dungeonEntryPacketOrder 给出我们这一步**实际**发出去的顺序（step 由调用方给出）。
func dungeonEntryPacketOrder(remotesSent bool) string {
	order := "op16_ack_or_follower_suppressed-op5-op2_mode0-op2_mode1"
	if remotesSent {
		order += "-op11_remotes"
	}
	return order + "-op27-op28-op29"
}

// sendDungeonEntryPeerEndpointRemotes 是副本进场序列里的 `op11-remotes` 动作点：
// 在 class0/op2 mode1 之后、op27 之前，给**本会话**补发一条只含对端行的 op11。
//
// 只对"真的在 ≥2 人队伍里"的会话发（`runtimePartyMemberIndex` 命中）：
// 单人副本没有厂商样本（厂商日志里的 `entry-prepared` 事件只在组队时出现），
// 此时记一条 `…-skipped`，不去猜单人队伍的 op11 形状。
func (s *Service) sendDungeonEntryPeerEndpointRemotes(session *gameSession, source string) (bool, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return false, nil
	}
	if !s.partyDungeonEntryPeerEndpoints().sendsRemotes() {
		return false, nil
	}
	state := s.runtimePartyStateForSession(session)
	memberIndex, indexed := runtimePartyMemberIndex(session.selectedCharacterID, state)
	if !indexed {
		s.logGameEvent(session, "game-party-dungeon-entry-peer-endpoints-skipped",
			"source", source,
			"char_id", session.selectedCharacterID,
			"party_id", state.PartyID,
			"member_count", len(runtimePartyMembers(state)),
			"reason", "runtime_party_below_two_members_has_no_remote_rows")
		return false, nil
	}
	body := s.buildRuntimePartyPeerEndpointInfoForReceiver(session, state)
	peerCount := 0
	if len(body) > 0 {
		peerCount = int(body[0])
	}
	expectedBodyLen := 1 + peerCount*currentPartyPeerEndpointSize
	s.logGameEvent(session, "game-party-dungeon-entry-peer-endpoints-send",
		"source", source,
		"char_id", session.selectedCharacterID,
		"party_id", state.PartyID,
		"member_index", memberIndex,
		"member_count", len(runtimePartyMembers(state)),
		"peer_count", peerCount,
		"body_len", len(body),
		"expected_body_len", expectedBodyLen,
		"body_len_matches_row_width", len(body) == expectedBodyLen,
		"msg_id", 0x000b,
		"classification", 0,
		"include_self", s.partyPeerEndpointIncludeSelf().String(),
		"ordering", "op2_mode1_all_then_op11_remotes_then_op27",
		"body_source", "same_builder_as_formation_runtime_party_snapshot_op11_remotes",
		"packet_order", dungeonEntryPacketOrder(true),
		"vendor_packet_order", dungeonEntryVendorPacketOrder,
		"reason", "vendor_re_sends_op11_remotes_inside_the_party_dungeon_entry_sequence")
	return true, s.sendGameUpperRawClassCodec(session, 0x000b, body, 0, true)
}

// logDungeonEntrySequence 在进场序列的 op28 之后留下一条与厂商同名的事件
// （`game-party-dungeon-entry-prepared`），字段与厂商对齐：
// `leader_slot` / `local_slot` / `release` / `packet_order`，另加 `vendor_packet_order`
// 作为对照。**只写日志**，不改变任何包。
//
// 厂商的 `release` 语义：队长那条 `release=false`、队员那条 `release=true`，
// 对应"最后一名成员到达后由它放行 barrier"。我们的等价物是
// `prepareRuntimePartyDungeonEntry`（所有成员先预检/准备，任何成员提交之前）+ 队长提交后
// 再异步驱动队员进场（`enterRuntimePartyFollowers`），所以：
//   - 由队长自己发起的这次（`peer_entry=false`）→ `release=false`；
//   - 由队长 fan-out 驱动的队员那次（`peer_entry=true`）→ `release=true`。
func (s *Service) logDungeonEntrySequence(
	session *gameSession,
	memberIndex byte,
	indexed bool,
	peerEntry bool,
	partyState alignedcmd.PartyState,
	remotesSent bool,
	dungeonID int64,
	mapID int64,
	requestBodyLen int,
) {
	if s == nil || session == nil {
		return
	}
	leaderSlot := byte(0)
	localSlot := byte(0)
	if indexed {
		localSlot = memberIndex
	}
	if _, leaderIndexed := runtimePartyMemberIndex(partyState.UserID, partyState); leaderIndexed {
		leaderSlot = 0
	}
	release := peerEntry
	s.logGameEvent(session, "game-party-dungeon-entry-prepared",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"party_id", partyState.PartyID,
		"leader_char_id", partyState.UserID,
		"leader_slot", leaderSlot,
		"local_slot", localSlot,
		"char_id", session.selectedCharacterID,
		"dungeon_id", dungeonID,
		"map_id", mapID,
		"member_count", len(runtimePartyMembers(partyState)),
		"request_body_len", requestBodyLen,
		"release", release,
		"packet_order", dungeonEntryPacketOrder(remotesSent),
		"vendor_packet_order", dungeonEntryVendorPacketOrder,
		"difference", "we_send_op27_between_op2_mode1_and_op28_and_send_op23_at_dungeon_exit")
}
