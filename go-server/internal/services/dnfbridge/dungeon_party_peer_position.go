package dnfbridge

import (
	"fmt"
	"strings"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 2026-09-25 副本身位上报取证结论（本轮，packet_log.20260925-043259.txt 之后的
// `/opt/dnf90/logs/packet_log.txt`，频道 10，双开实测 04:3x~04:5x）：
//
//	① **副本内客户端完全不上报坐标**（既没有 op35，也没有 op36）。
//	   副本窗口（`game-dungeon-party-instance-created` 行 1579 ~ `…-back-to-village-committed`
//	   行 2025）里的**全部** client→server 包只有：
//	   op16 SELECT_DUNGEON / op37 FINISH_LOADING / op39 DIE_MONSTER×4 / op123 CHARACTER_STATISTIC /
//	   op42 GIVEUP_GAME / op593 SECURITY_STATUS×4 / op417 ACHIEVEMENT_TRIGGER×3 /
//	   op506 SAVE_QUEST_NOTIFY / op209 UDP_CHARACTERISTIC / op197 SAVE_GAME_OPTION_1 / op477 INFORM_NOTICE。
//	   **没有 op35、没有 op36、没有任何位置/区域包**。
//	② 全会话 RECV `msg_id=35` = **0**；RECV `msg_id=36` = 19，且 19 条与
//	   `game-town-set-user-area-transition-send` **一一对应**（RECV 行 N ↔ 事件行 N+2），
//	   map_path 全是城镇（west_coast / CentralPark / new_seria_room / new_death_valley /
//	   StormPass / new_Elvengard / new_D_Elvengard_L）。前三个会话（02:55/03:29/04:32 日志）
//	   同样是 RECV op35 = 0、op36 = 14/18/20 全为城镇过图。
//	⇒ 因此 `handleTownSetUserArea` 的**副本分支**在实践中永远不会被走到；把它做实是为了
//	  "客户端一旦在副本里开始发 op36"这一变体留有唯一正确的挂钩，而不是当前故障的修复。
//	  真正在副本里带坐标的上报是 **op45（MOVE_MAP）**，见 handleDungeonMoveMapPlanned 的注释。
//
// partyDungeonPeerPositionEnv 是"副本内把**队友**的位移转发给同场队员"的开关。
//
//	off（默认）：一个字节都不发，与现行为逐字节相同。
//	peer：      收到队友 op35 坐标上报时，用城镇里已被客户端消费成功的 **class0/noti0x16**
//	            （`buildCurrentTownUserPositionNotificationBody`：u16 actor_key, u16 x, u16 y,
//	            u8 direction, u16 movement_rate）发给**同场其他队员**，actor_key = 队友的
//	            场景 actor key（= 队友角色 id），owner 保持副本场景上下文 0，**绝不发给上报者本人**。
//
// 为什么是这条形状（现场证据，2026-09-25 04:05，频道 10）：
//   - 城镇里队友会动，靠的就是 noti0x16：`game-upper-current-scene-fixed-send
//     owner=town_copresence_move_0x0016 msg_id=22`（body `00160019000000…0004003d04f900056400`
//     = key=4 x=1085 y=249 dir=5 rate=100），客户端 trace 里队友会正常行走；
//   - 副本里**一条队友位移都没有**：整场日志 0 条 op22/op120/op23 发给副本内的队友投影
//     （op120/op23 已在第七批按实测无效移除）⇒ 操作者看到的就是"看得到队长但队长不动、
//     任何动作都不同步"；
//   - 坐标真源已经存在：`runtime.recordPartyDungeonActorPosition()` 把每个成员最近一次
//     op35 上报的坐标记进队伍级共享实例（`dungeonPartyInstance.positions`），
//     但第七批只记录、不外发（见 town_position.go 的注释）。
//
// 默认关闭的理由：操作者的客户端 trace 是这条包的**唯一**验收手段，而"客户端会不会把
// noti0x16 应用到**本地** actor（= 自己把自己拽走）"还没有证据 —— 上一轮 op22 复测就出现过
// "收包方自己的模型消失"。所以先做成单变量 A/B，由操作者一次 2 分钟的动作定死。
const partyDungeonPeerPositionEnv = "DNFBRIDGE_PARTY_DUNGEON_PEER_POSITION"

const (
	partyDungeonPeerPositionValueOff  = "off"
	partyDungeonPeerPositionValuePeer = "peer"
)

// partyDungeonPeerPositionMode 是 partyDungeonPeerPositionEnv 的归一化取值。
type partyDungeonPeerPositionMode int

const (
	// partyDungeonPeerPositionDisabled 是默认档：不发任何副本内队友位移。
	partyDungeonPeerPositionDisabled partyDungeonPeerPositionMode = iota
	// partyDungeonPeerPositionPeer 只把队友位移转发给其他队员。
	partyDungeonPeerPositionPeer
)

func (mode partyDungeonPeerPositionMode) String() string {
	if mode == partyDungeonPeerPositionPeer {
		return partyDungeonPeerPositionValuePeer
	}
	return partyDungeonPeerPositionValueOff
}

func (mode partyDungeonPeerPositionMode) enabled() bool {
	return mode == partyDungeonPeerPositionPeer
}

// readPartyDungeonPeerPositionMode 解析 DNFBRIDGE_PARTY_DUNGEON_PEER_POSITION。
// **默认开启**：空/未设置 = `peer`（转发开启），这样不带这条 env 的部署也能拿到修复；
// 只有显式写 `off` 才关闭，线上可用 env 一键回退。
func readPartyDungeonPeerPositionMode(value string) partyDungeonPeerPositionMode {
	if strings.EqualFold(strings.TrimSpace(value), partyDungeonPeerPositionValueOff) {
		return partyDungeonPeerPositionDisabled
	}
	return partyDungeonPeerPositionPeer
}

// dungeonPartyPeerPositionSnapshot 是"某个会话最近一次上报的副本坐标"的一份只读快照。
// 读它只锁**被读的那一个**会话的 dungeon.mu，调用方不会同时持两个会话的 dungeon 锁
// （两个会话互等锁是本仓库已记录的跨会话死锁，绝不能引入）。
type dungeonPartyPeerPositionSnapshot struct {
	CharacterID uint16
	X           uint16
	Y           uint16
	Direction   byte
	Valid       bool
	RoomX       int64
	RoomY       int64
	MapID       int64
}

func (s *Service) dungeonPartyPeerPositionSnapshot(session *gameSession) (dungeonPartyPeerPositionSnapshot, bool) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return dungeonPartyPeerPositionSnapshot{}, false
	}
	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	if runtime == nil || !runtime.actorPositionValid {
		session.dungeon.mu.Unlock()
		return dungeonPartyPeerPositionSnapshot{}, false
	}
	var roomX, roomY, mapID int64
	if scene, ok := runtime.Session.Scene(); ok {
		roomX = scene.Coordinate.X
		roomY = scene.Coordinate.Y
		mapID = scene.Map.Map.ID
	}
	snapshot := dungeonPartyPeerPositionSnapshot{
		CharacterID: session.selectedCharacterID,
		X:           runtime.actorPositionX,
		Y:           runtime.actorPositionY,
		Direction:   runtime.actorDirection,
		Valid:       true,
		RoomX:       roomX,
		RoomY:       roomY,
		MapID:       mapID,
	}
	session.dungeon.mu.Unlock()
	return snapshot, true
}

// currentDungeonRoomScene 取副本运行时当前房间场景（只读、调用方已持 session.dungeon.mu）。
func currentDungeonRoomScene(runtime *runtimeDungeonState) (worldmap.DungeonRoomScene, bool) {
	if runtime == nil || runtime.Session == nil {
		return worldmap.DungeonRoomScene{}, false
	}
	return runtime.Session.Scene()
}

// broadcastDungeonPartyPeerPosition 把**上报者最新坐标**转发给同一场副本里的其他队员。
//
// 只发其他队员，绝不发给上报者本人（本地 actor 的坐标由客户端自己拥有；把 noti0x16 回给
// 本人正是上一轮"收包方自己的模型消失"的风险点）。owner 用副本场景上下文 0，与队友 actor
// 的 mode0/mode1/op9 同一个上下文。
func (s *Service) broadcastDungeonPartyPeerPosition(session *gameSession, source string) int {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return 0
	}
	if !s.options.partyDungeonPeerPosition.enabled() {
		return 0
	}
	mover, ok := s.dungeonPartyPeerPositionSnapshot(session)
	if !ok || !mover.Valid {
		return 0
	}
	state := runtimePartyStateSnapshot(session)
	members := runtimePartyMembers(state)
	if state.PartyID <= 0 || len(members) < 2 {
		return 0
	}
	ownKey, ready := s.currentDungeonPartyRunKey(session)
	if !ready {
		return 0
	}
	body := buildCurrentTownUserPositionNotificationBody(
		currentSceneActorObjectKey(mover.CharacterID),
		mover.X,
		mover.Y,
		mover.Direction,
	)
	sent := 0
	for _, member := range members {
		if member.UserID == 0 || member.UserID == session.selectedCharacterID {
			continue
		}
		peer, online := s.onlineGameSession(member.UserID)
		if !online || peer == nil || peer == session {
			continue
		}
		peerKey, peerReady := s.currentDungeonPartyRunKey(peer)
		if !peerReady || peerKey != ownKey {
			continue
		}
		peerSnapshot, peerHasPosition := s.dungeonPartyPeerPositionSnapshot(peer)
		if !peerHasPosition {
			continue
		}
		if err := s.sendCurrentSceneFixedClass0Packet(
			peer,
			currentTownUserPositionNotificationMsgID,
			body,
			"dungeon_party_peer_move_0x0016",
		); err != nil {
			s.logGameEvent(peer, "game-dungeon-party-peer-position-failed",
				"source", source,
				"mover_char_id", mover.CharacterID,
				"receiver_char_id", peer.selectedCharacterID,
				"error", err)
			continue
		}
		sent++
		s.logGameEvent(peer, "game-dungeon-party-peer-position-send",
			"source", source,
			"mover_char_id", mover.CharacterID,
			"receiver_char_id", peer.selectedCharacterID,
			"actor_object_key", currentSceneActorObjectKey(mover.CharacterID),
			"position_x", mover.X,
			"position_y", mover.Y,
			"direction", mover.Direction,
			"mover_room", dungeonRoomCoordinate(mover.RoomX, mover.RoomY),
			"receiver_room", dungeonRoomCoordinate(peerSnapshot.RoomX, peerSnapshot.RoomY), "party_id", state.PartyID,
			"msg_id", currentTownUserPositionNotificationMsgID,
			"owner_channel", currentSceneObjectContext,
			"body_len", len(body),
			"body_source", "current_exe_sub_1D83990_op22_user_position_proven_in_town_copresence",
			"env", partyDungeonPeerPositionEnv,
			"mode", s.options.partyDungeonPeerPosition.String())
	}
	return sent
}

func dungeonRoomCoordinate(x, y int64) string {
	return fmt.Sprintf("%d:%d", x, y)
}

// recordDungeonReportedActorPosition 把一条**副本内**坐标上报写进本会话运行时，并同步进
// 队伍级共享实例（`recordPartyDungeonActorPosition` ⇒ 队友 actor 投影/补发能拿到位置）。
// 调用方需持有 session.dungeon.mu（锁序与 recordPartyDungeonActorPosition 一致：
// session.dungeon.mu → instance.mu）。
func recordDungeonReportedActorPosition(
	runtime *runtimeDungeonState,
	x, y uint16,
	direction byte,
) bool {
	if runtime == nil {
		return false
	}
	runtime.actorPositionX = x
	runtime.actorPositionY = y
	runtime.actorDirection = direction
	runtime.actorPositionValid = true
	runtime.recordPartyDungeonActorPosition()
	return true
}

// handleDungeonActiveSetUserAreaReport 是 `handleTownSetUserArea` 的**副本分支**：
// 副本运行时还挂着时收到客户端 op36（SET_USER_AREA，16 字节明文 body）。
//
// 语义边界（不猜）：
//   - 城镇过图提交仍然被拒绝，旧信号 `game-town-set-user-area-blocked
//     reason=active_dungeon_runtime_rejects_town_area_request` 一字未动；
//   - 副本内 op36 的 (town_id, area_id) 语义**没有当前客户端样本**（实测副本内一条都没有），
//     所以这里只把 body[2:6] 的 u16 坐标当成"本会话在副本里的位置"记录，并用同一形状
//     （noti0x16，owner=0，只发其他队员、绝不回发本人）转发给同场队员；
//   - 坐标为 (0,0) 的上报不携带任何位置信息 ⇒ 只记日志、不覆盖已有坐标、不转发。
//
// 这条分支线上零效果（副本内收不到 op36），保留它是为了"客户端变体开始在副本里发
// op36"时不必再改代码；线上判读串 `game-town-set-user-area-dungeon-report`。
func (s *Service) handleDungeonActiveSetUserAreaReport(session *gameSession, request currentTownSetUserAreaRequest) {
	if s == nil || session == nil {
		return
	}
	s.logTownSetUserAreaBlocked(session, request, "active_dungeon_runtime_rejects_town_area_request")

	x := uint16(request.PositionX)
	y := uint16(request.PositionY)
	recorded := false
	if x != 0 || y != 0 {
		session.dungeon.mu.Lock()
		recorded = recordDungeonReportedActorPosition(session.dungeon.runtime, x, y, request.Direction)
		session.dungeon.mu.Unlock()
	}
	s.logGameEvent(session, "game-town-set-user-area-dungeon-report",
		"char_id", session.selectedCharacterID,
		"town_id", request.TownID,
		"area_id", request.AreaID,
		"position_x", request.PositionX,
		"position_y", request.PositionY,
		"direction", request.Direction,
		"position_recorded", recorded,
		"body_len", currentTownSetUserAreaBodySize,
		"body_source", "current_exe_op36_plaintext_16_byte_body",
		"note", "dungeon_context_op36_has_no_live_sample_yet_variant_hook_only")
	if !recorded {
		return
	}
	s.broadcastDungeonPartyPeerPosition(session, "current_exe_op36_dungeon_area_report")
}
