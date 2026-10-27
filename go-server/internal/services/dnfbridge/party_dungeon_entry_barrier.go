package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件实现「全队都走到 op28 之后再统一放行 op800+op29」这一条进场屏障。
//
// # 现场缺陷（操作者 2026-09-25 21:07 双开组队进图）
//
// 两名队员的读条不同步：官方客户端要"两个人都加载完才一起进图"，我们的客户端
// 读条"先不显示、然后一下跳到满"。客户端 trace（`90CN_trace.log`，pid 区分）：
//
//	pid=31416（队长 char3）21:07:50.268 lua enter_dungeon armed stage=op28
//	                        21:07:50.269 lua enter_dungeon armed stage=op29
//	                        21:07:52.531 lua enter_dungeon confirmed
//	pid=43840（队员 char4）21:07:50.392 lua enter_dungeon armed stage=op28
//	                        21:07:50.393 lua enter_dungeon armed stage=op29
//	                        21:07:50.811 lua enter_dungeon confirmed
//
// 服务端侧（`/opt/dnf90/logs/packet_log.txt`，21:00 后当前版本）两个人的进场序列
// 是**串行**的：队长整条跑完（:2426 op28 → :2430 op29 → :2490 落位 → :2496 落位完
// → :2497 op36 → :2500 op9 房间槽位表），队员那条才从 :2505 开始（:2524 op28 →
// :2528 op29）。原因是 `handleDungeonSelectUpperPlanned` 把队员的 fan-out 放在
// **队长整条序列的最后**（`enterRuntimePartyFollowers`）。
//
// # 厂商对照（只读引用 D:\DNF90-test\server\runtime\logs\packet_log.txt）
//
//	:5368 10:23:12.4176856 game-party-dungeon-entry-prepared conn=game-000059 local_slot=0 release=false
//	:5387 10:23:12.4176856 game-party-dungeon-entry-prepared conn=game-000061 local_slot=1 release=true
//	:5388 10:23:12.4176856 game-party-dungeon-entry-barrier-released conn=game-000061 party_id=2
//	      member_count=2 packet_order=all-members-through-op28-before-any-op800-op29
//	:5389 10:23:12.4176856 SEND conn=game-000061 msg_id=800 plain_body_len=7
//	      hex=002003170000008a46c88c6f0000000002010000020000
//	:5390 10:23:12.4176856 SEND conn=game-000061 msg_id=29 plain_body_len=107
//	:5393 10:23:12.4176856 SEND conn=game-000059 msg_id=800 plain_body_len=7
//	      hex=002003170000004e9e33d3e50300000002010000020000
//	:5394 10:23:12.4176856 SEND conn=game-000059 msg_id=29 plain_body_len=107
//
// 即：厂商把两名成员的 op28（含 op2/op11/op27/op28 全套）**都发完之后**，才用
// `msg_id=800` + `msg_id=29` 一起放行两个人。两条 op800 的 body **逐字节相同**：
// `02 01 00 00 02 00 00`。
//
// # op800 是什么（可读，不是猜的）
//
// op800 = `currentTownActorSceneSnapshotMsgID`（初始城镇那条
// `initial_town_actor_scene_snapshot_current_exe_sub_1D6C6F0`），reader 见
// `buildCurrentTownActorSceneSnapshotBody`：
//
//	u8 actor_count, actor_count * { u16 existing_actor_key, u8 child_count }
//
// 城镇样本 `:2605` conn59 body=`01 01 00 00`（count=1, key=char1=1, child=0）、
// `:2989` conn61 body=`01 02 00 00`（key=char2=2）。副本 barrier 那条
// `02 01 00 00 02 00 00` = count=2 → {key=1,child=0}, {key=2,child=0} —— 就是把
// **本房间这一队人的 actor key 全列一遍**（object key 就是 char id，
// `currentSceneActorObjectKey`）。所以它天然是"这一场的人都在场景里了"的就绪信号，
// 而且对两个收包者内容相同。我们只复用同一个 msg id / 同一个 builder 族，不新增包型。
//
// # 开关
//
//	`DNFBRIDGE_PARTY_DUNGEON_ENTRY_BARRIER`
//	  - `vendor`（默认，零值）= 厂商行为：所有成员走到 op28 → barrier 放行 →
//	    每个成员各收 op800 + op29；
//	  - `legacy` = 改动前行为（队长整条序列跑完，再异步驱动队员进场；不发 op800）。
//
// 与既有开关的关系：本开关只决定"op28 与 op29 之间等不等人、以及 op29 之前多不多
// 那一条 op800"。它不改变 op11 remotes 的插入位置
// （`DNFBRIDGE_PARTY_DUNGEON_ENTRY_PEER_ENDPOINTS`）、op36 fatigue、op9 房间槽位表
// （`DNFBRIDGE_PARTY_DUNGEON_START_MAP_FATIGUE` / `…_ROOM_SLOT_TABLE`）的任何语义。
const partyDungeonEntryBarrierEnv = "DNFBRIDGE_PARTY_DUNGEON_ENTRY_BARRIER"

const (
	partyDungeonEntryBarrierValueVendor = "vendor"
	partyDungeonEntryBarrierValueLegacy = "legacy"
)

// partyDungeonEntryBarrierMode 是 partyDungeonEntryBarrierEnv 的归一化取值。
type partyDungeonEntryBarrierMode int

const (
	// partyDungeonEntryBarrierVendor 是默认档（零值）= 厂商行为：全队 op28 之后统一放行。
	partyDungeonEntryBarrierVendor partyDungeonEntryBarrierMode = iota
	// partyDungeonEntryBarrierLegacy = 改动前行为（逐字节回退）。
	partyDungeonEntryBarrierLegacy
)

func (mode partyDungeonEntryBarrierMode) String() string {
	if mode == partyDungeonEntryBarrierLegacy {
		return partyDungeonEntryBarrierValueLegacy
	}
	return partyDungeonEntryBarrierValueVendor
}

// holds 表示是否在 op28 与 op29 之间等全部成员。
func (mode partyDungeonEntryBarrierMode) holds() bool {
	return mode != partyDungeonEntryBarrierLegacy
}

// readPartyDungeonEntryBarrierMode 解析 DNFBRIDGE_PARTY_DUNGEON_ENTRY_BARRIER。
// **默认 vendor（= 厂商行为）**：空/未设置/非法值都是 vendor；只有显式写 `legacy` 才回退。
func readPartyDungeonEntryBarrierMode(value string) partyDungeonEntryBarrierMode {
	if strings.EqualFold(strings.TrimSpace(value), partyDungeonEntryBarrierValueLegacy) {
		return partyDungeonEntryBarrierLegacy
	}
	return partyDungeonEntryBarrierVendor
}

// partyDungeonEntryBarrier 返回当前 Service 的归一化档位（零值 options 的测试/探针
// 路径 = vendor = 默认档）。
func (s *Service) partyDungeonEntryBarrier() partyDungeonEntryBarrierMode {
	if s == nil {
		return partyDungeonEntryBarrierVendor
	}
	return s.options.partyDungeonEntryBarrier
}

// currentDungeonEntryActorSceneSnapshotMsgID 就是城镇那条场景 actor 快照的 msg id
// （0x0320 = 800）。厂商在副本 barrier 放行处用的是同一个 id 与同一个 reader。
const currentDungeonEntryActorSceneSnapshotMsgID uint16 = currentTownActorSceneSnapshotMsgID

// dungeonEntryBarrierVendorPacketOrder 是厂商自己在 barrier 放行事件里写下的顺序串。
const dungeonEntryBarrierVendorPacketOrder = "all-members-through-op28-before-any-op800-op29"

// dungeonEntryBarrierVendorBodyHex 是厂商两条 op800 的 body（两收包者逐字节相同）。
const dungeonEntryBarrierVendorBodyHex = "02010000020000"

// buildDungeonEntryActorSceneSnapshotBody 按 `buildCurrentTownActorSceneSnapshotBody`
// 的同一个 reader 组装副本 barrier 那一条 op800：
//
//	u8 actor_count, actor_count * { u16 actor_key, u8 child_count(=0) }
//
// 厂商夹具 objectKeys=[1,2] ⇒ `02 01 00 00 02 00 00`（回归用例逐字节断言）。
func buildDungeonEntryActorSceneSnapshotBody(objectKeys []uint16) []byte {
	body := make([]byte, 1+3*len(objectKeys))
	body[0] = byte(len(objectKeys))
	for index, key := range objectKeys {
		binary.LittleEndian.PutUint16(body[1+3*index:3+3*index], key)
	}
	return body
}

// partyDungeonEntryActorObjectKeys 按队伍槽位顺序列出本队成员的副本 actor key。
// object key 与 char id 同值（`currentSceneActorObjectKey`），与厂商夹具里的
// `key=1`/`key=2` 对齐。
func partyDungeonEntryActorObjectKeys(state alignedcmd.PartyState) []uint16 {
	members := runtimePartyMembers(state)
	keys := make([]uint16, 0, len(members))
	for _, member := range members {
		if member.UserID == 0 {
			continue
		}
		keys = append(keys, currentSceneActorObjectKey(member.UserID))
	}
	return keys
}

// sendPartyDungeonEntryActorSceneSnapshot 在 barrier 放行之后、op29 之前，给**本会话**
// 发一条 class0/op800 actor 场景快照（厂商 `all-members-through-op28-before-any-op800-op29`）。
//
// 只对 ≥2 人队伍发：厂商的 op800 副本样本只出现在组队 barrier 里，单人副本没有夹具
// （单人那条 op800 是初始城镇的 4 字节版本，走的是另一条路径）。
func (s *Service) sendPartyDungeonEntryActorSceneSnapshot(session *gameSession, source string) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	if !s.partyDungeonEntryBarrier().holds() {
		return nil
	}
	state := s.runtimePartyStateForSession(session)
	members := runtimePartyMembers(state)
	if state.PartyID <= 0 || len(members) < 2 {
		s.logGameEvent(session, "game-party-dungeon-entry-actor-scene-snapshot-skipped",
			"source", source,
			"char_id", session.selectedCharacterID,
			"party_id", state.PartyID,
			"member_count", len(members),
			"reason", "party_below_two_members_has_no_dungeon_op800_sample")
		return nil
	}
	keys := partyDungeonEntryActorObjectKeys(state)
	body := buildDungeonEntryActorSceneSnapshotBody(keys)
	s.logGameEvent(session, "game-party-dungeon-entry-actor-scene-snapshot-send",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", session.selectedCharacterID,
		"party_id", state.PartyID,
		"msg_id", currentDungeonEntryActorSceneSnapshotMsgID,
		"classification", 0,
		"actor_count", len(keys),
		"actor_keys", formatDungeonEntryActorKeys(keys),
		"body_len", len(body),
		"body_source", "current_exe_sub_1D6C6F0_scene_snapshot_shared_with_initial_town",
		"vendor_body_hex", dungeonEntryBarrierVendorBodyHex,
		"packet_order", dungeonEntryBarrierVendorPacketOrder,
		"reason", "vendor_releases_op800_before_op29_for_every_member_after_all_members_reached_op28")
	return s.sendCurrentSceneFixedClass0Packet(
		session,
		currentDungeonEntryActorSceneSnapshotMsgID,
		body,
		"party_dungeon_entry_actor_scene_snapshot_after_all_member_op28_barrier",
	)
}

// formatDungeonEntryActorKeys 只用于日志（`1,2` 这种形状），不影响任何字节。
func formatDungeonEntryActorKeys(keys []uint16) string {
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, strconv.FormatUint(uint64(key), 10))
	}
	return strings.Join(parts, ",")
}

// releasePartyDungeonEntryAfterOp28Barrier 是 barrier 的"最后一名成员放行"点：
//
//  1. 把每个队员各自驱动到 op28（各自会话事件循环上执行；跑完就 early-return）；
//  2. 记一条厂商同名事件 `game-party-dungeon-entry-barrier-released`；
//  3. 然后**才**给队长、再给每个队员下发 op800 + op29 及其后的尾段。
//
// 失败放行路径（不卡人）：任一队员的阶段一失败（会话掉线 / 事件循环超时）都**不**中断
// 整队进图——只记一条 `game-party-dungeon-entry-barrier-member-failed`，其余成员照常放行。
// 这也是改动前的语义（原来的 `enterRuntimePartyFollowers` 失败会把 error 抛给队长，
// 那种"队长进不去"的形态不是本开关要引入的）。
func (s *Service) releasePartyDungeonEntryAfterOp28Barrier(
	leader *gameSession,
	body []byte,
	followers []*runtimePartyPreparedDungeonEntry,
	partyState alignedcmd.PartyState,
	leaderRuntime *runtimeDungeonState,
	leaderScene worldmap.DungeonRoomScene,
	request dungeoncmd.SelectDungeonRequest,
	leaderRemotesSent bool,
) error {
	staged := make([]*runtimePartyPreparedDungeonEntry, 0, len(followers))
	for _, member := range followers {
		if member == nil || member.session == nil || member.runtime == nil {
			continue
		}
		if err := s.stagePartyDungeonEntryMemberAtOp28(member, body); err != nil {
			s.logGameEvent(leader, "game-party-dungeon-entry-barrier-member-failed",
				"conn_id", leader.connID,
				"channel_id", leader.channel.ID,
				"party_id", partyState.PartyID,
				"leader_char_id", leader.selectedCharacterID,
				"member_char_id", member.session.selectedCharacterID,
				"stage", "op28",
				"error", err)
			continue
		}
		staged = append(staged, member)
	}
	s.logGameEvent(leader, "game-party-dungeon-entry-barrier-released",
		"conn_id", leader.connID,
		"channel_id", leader.channel.ID,
		"party_id", partyState.PartyID,
		"leader_char_id", leader.selectedCharacterID,
		"member_count", len(runtimePartyMembers(partyState)),
		"staged_member_count", len(staged),
		"leader_remotes_sent", leaderRemotesSent,
		"packet_order", dungeonEntryBarrierVendorPacketOrder,
		"vendor_packet_order", dungeonEntryBarrierVendorPacketOrder,
		"reason", "vendor_releases_op800_op29_only_after_every_member_reached_op28")

	if err := s.sendPartyDungeonEntryTailAfterOp28(
		leader,
		leaderRuntime,
		leaderScene,
		nil,
		request,
		nil,
		nil,
		alignedcmd.PartyState{},
		false,
	); err != nil {
		return err
	}
	for _, member := range staged {
		if err := s.releasePartyDungeonEntryMemberAfterOp28Barrier(member, body); err != nil {
			s.logGameEvent(leader, "game-party-dungeon-entry-barrier-member-failed",
				"conn_id", leader.connID,
				"channel_id", leader.channel.ID,
				"party_id", partyState.PartyID,
				"leader_char_id", leader.selectedCharacterID,
				"member_char_id", member.session.selectedCharacterID,
				"stage", "after_op28",
				"error", err)
		}
	}
	return nil
}

// stagePartyDungeonEntryMemberAtOp28 把一条已经预检/提交过的队员记录驱动到 op28 为止。
// 队员自己的 `handleDungeonSelectUpperPlanned` 会在 op28 之后 early-return
// （`game-party-dungeon-entry-member-staged-at-op28`）。
func (s *Service) stagePartyDungeonEntryMemberAtOp28(
	member *runtimePartyPreparedDungeonEntry,
	body []byte,
) error {
	if member == nil || member.session == nil || member.op28Staged {
		return nil
	}
	follower := member.session
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	payload := append([]byte(nil), body...)
	return s.callGameSession(ctx, follower, "runtime-party-dungeon-entry-op28-barrier", func() error {
		return s.handleDungeonSelectUpperPlanned(follower, payload, member, false, nil)
	})
}

// releasePartyDungeonEntryMemberAfterOp28Barrier 放行一条队员记录的 op28 之后尾段。
//
// 仍然走 `handleDungeonSelectUpperPlanned` 这一个入口：`member.op28Staged` 已经为 true，
// 函数开头的重入分支会直接跳到 op28 之后的尾段（不会再发一遍 …op2/op11/op27/op28），
// 这样"进图序列"始终只有一个入口，不会出现两条各自演化的放行路径。
func (s *Service) releasePartyDungeonEntryMemberAfterOp28Barrier(
	member *runtimePartyPreparedDungeonEntry,
	body []byte,
) error {
	if member == nil || member.session == nil || member.runtime == nil {
		return nil
	}
	follower := member.session
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	payload := append([]byte(nil), body...)
	return s.callGameSession(ctx, follower, "runtime-party-dungeon-entry-after-op28-barrier", func() error {
		return s.handleDungeonSelectUpperPlanned(follower, payload, member, false, nil)
	})
}
