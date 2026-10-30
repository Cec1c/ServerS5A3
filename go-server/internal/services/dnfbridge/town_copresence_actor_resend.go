package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// 本文件处理"城镇同屏 actor 的补发时机"——与 dungeon_party_peer_actor_resend.go
// 同一类缺陷、同一套修法，只是发生在城镇（含**副本门口**这类城镇区域）。
//
// # 报障（操作者原话）
//
// 「在副本门口的两人看不到对方，需要重新进出这个副本门口区域才能看到」。
//
// # 现场证据（2026-09-25 19:2x，香港 packet_log.txt / packet_log.20260925-192600.txt）
//
// 两个会话（同频道 253）先后不到 1 秒跨进同一个城镇区域（38/2 = `new_D_Elvengard_L.map`，
// PVF 里 `[dungeon gate] 2` = 副本门口）：
//
//	packet_log.txt:828 game-town-set-user-area-committed char_id=3 town 38 area 2
//	packet_log.txt:846 game-town-copresence-actor-state-send target_char_id=3 actor_char_id=4
//	packet_log.txt:860 game-town-copresence-actor-state-send target_char_id=4 actor_char_id=3
//	packet_log.txt:880 game-town-set-user-area-committed char_id=4 town 38 area 2
//	packet_log.txt:1177/1211 game-main-finish-loading-state-send（两人的 op37 主状态）
//
// 即：**两人的互相投影（846/860）都发生在自己还没 finish-loading（1177/1211）之前**。
// 城镇投影的唯一触发点是 `handleTownSetUserArea` → `onlinePlayers.EnterArea` →
// `broadcastTownPlayerEnter`，它和 op22/op23/op24 场景切换包在同一个处理里发出；对收包方
// 来说这时候新场景还在加载 ⇒ actor 落进"场景尚未就绪"的窗口。谁先跨区谁就看不到对方，
// 两个人都几乎同时跨区时就是"两人互相看不到"。之后玩家自己再进一次这个区域，对端早已
// 在场且本地场景已就绪，投影就被客户端接受了 ⇒ "重新进出该区域才能看到"。
//
// # 修法
//
// 在收包方自己的 finish-loading（op37 主状态 + op30 + post-state 全部发完，
// 见 scene_finish_loading.go）之后，对本区域内的其它玩家**补发一次**同屏投影
// （op2 mode0/mode1 + op9 记录 + op23 区域状态），与副本那条补发的口径完全一致：
//
//   - **只补城镇**：持有活跃副本运行时会话交给 dungeon_party_peer_actor_resend.go，
//     这里直接返回（两条补发互不重叠、也不重复发）；
//   - **只补同桶对端**：复用 `onlinePlayers.PeersInSameArea`（同城+同区+同频道+同实例）；
//   - **幂等**：同一 (会话, 城镇, 区域, 对端集合) 只补一次；换区域/换对端集合重新允许；
//   - **失败即放行**：任何一步失败只记 `game-town-copresence-actor-resend-failed`，
//     绝不影响 op37 处理链（与既有 `game-town-copresence-actor-state-failed` 同一口径）。
//
// 回退：`DNFBRIDGE_TOWN_COPRESENCE_ACTOR_RESEND=off`（也接受 0/false/no/legacy）⇒
// 一次都不补发，恢复旧行为（逐字节不变）。
const currentTownCopresenceResendEnv = "DNFBRIDGE_TOWN_COPRESENCE_ACTOR_RESEND"

func currentTownCopresenceResendEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(currentTownCopresenceResendEnv)))
	switch value {
	case "0", "off", "false", "no", "legacy":
		return false
	default:
		return true
	}
}

// resendTownCopresencePeerActorsAfterFinishLoading 在收包方 finish-loading 之后补发
// 一次本城镇区域的同屏投影。幂等、只补对端、绝不重发本地自身。
func (s *Service) resendTownCopresencePeerActorsAfterFinishLoading(session *gameSession, source string) {
	if !currentTownCopresenceResendEnabled() {
		return
	}
	if s == nil || s.onlinePlayers == nil || session == nil || session.selectedCharacterID == 0 {
		return
	}
	// 副本场景由 dungeon_party_peer_actor_resend.go 负责。
	if s.sessionHasActiveDungeonRuntime(session) {
		return
	}
	if err := s.sendCurrentTownPartyHPAfterCrossAreaLoading(session); err != nil {
		s.logGameEvent(session, "game-town-cross-area-party-hp-resend-failed",
			"source", source, "char_id", session.selectedCharacterID, "error", err)
		return
	}
	peers := s.onlinePlayers.PeersInSameArea(session.selectedCharacterID)
	if len(peers) == 0 {
		return
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].CharacterID < peers[j].CharacterID })
	townID, areaID := s.currentTownCopresenceResendArea(session, peers[0])
	if !s.claimTownCopresenceActorResend(session, townID, areaID, peers) {
		return
	}
	resent := make([]uint16, 0, len(peers))
	for i := range peers {
		peer := peers[i]
		if peer.Session == nil || peer.CharacterID == session.selectedCharacterID {
			continue
		}
		if err := s.sendTownRemoteActorState(
			session,
			peer,
			"town_copresence_actor_state_after_finish_loading",
		); err != nil {
			s.releaseTownCopresenceActorResend(session)
			s.logGameEvent(session, "game-town-copresence-actor-resend-failed",
				"source", source,
				"char_id", session.selectedCharacterID,
				"peer_char_id", peer.CharacterID,
				"error", err)
			return
		}
		areaBody := buildCurrentTownUserAreaNotificationBody(
			currentSceneActorObjectKey(peer.CharacterID),
			peer.TownID,
			peer.AreaID,
			peer.PositionX,
			peer.PositionY,
			peer.Direction,
			peer.AreaState,
		)
		_ = s.sendCurrentSceneFixedClass0Packet(
			session,
			currentTownUserAreaNotificationMsgID,
			areaBody,
			"town_copresence_enter_0x0017_after_finish_loading",
		)
		resent = append(resent, peer.CharacterID)
	}
	if len(resent) == 0 {
		s.releaseTownCopresenceActorResend(session)
		return
	}
	s.logGameEvent(session, "game-town-copresence-actor-resend",
		"source", source,
		"char_id", session.selectedCharacterID,
		"town_id", townID,
		"area_id", areaID,
		"resent_peer_char_ids", resent,
		"trigger", "receiver_finish_loading",
		"reason", "town copresence actor projection landed before the receiving client finished its scene rebuild")
}

// currentTownCopresenceResendArea 取本次补发所在城镇区域的 (town, area)。
// 存在感分桶是权威（对端与本会话必然同桶），这里的 town/area 只用于幂等键与日志：
// 优先用对端的存在记录（不查库），拿不到再退回本会话角色记录的持久化位置。
func (s *Service) currentTownCopresenceResendArea(
	session *gameSession,
	peer onlinePlayerInfo,
) (byte, byte) {
	if peer.TownID != 0 || peer.AreaID != 0 {
		return peer.TownID, peer.AreaID
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	_, _, character, ok := s.selectedCharacterForEnter(ctx, session)
	if !ok {
		return 0, 0
	}
	townID, areaID, err := currentSceneTransitionLocation(character, true)
	if err != nil {
		return 0, 0
	}
	return townID, areaID
}

// claimTownCopresenceActorResend 返回 true = 这次调用占用了补发名额。
// 幂等键 = 角色 + 城镇/区域 + 对端集合：同一批人只补一次，换区域或换人后重新允许。
func (s *Service) claimTownCopresenceActorResend(
	session *gameSession,
	townID, areaID byte,
	peers []onlinePlayerInfo,
) bool {
	if session == nil || len(peers) == 0 {
		return false
	}
	ids := make([]string, 0, len(peers))
	for i := range peers {
		ids = append(ids, strconv.Itoa(int(peers[i].CharacterID)))
	}
	key := fmt.Sprintf("%d/%d/%d/%s", session.selectedCharacterID, townID, areaID, strings.Join(ids, ","))
	session.townCopresenceResendMu.Lock()
	defer session.townCopresenceResendMu.Unlock()
	if session.townCopresenceResendKey == key {
		return false
	}
	session.townCopresenceResendKey = key
	return true
}

func (s *Service) releaseTownCopresenceActorResend(session *gameSession) {
	if session == nil {
		return
	}
	session.townCopresenceResendMu.Lock()
	session.townCopresenceResendKey = ""
	session.townCopresenceResendMu.Unlock()
}

// publishTownPresenceAfterSeriaRoomReturn 把"从**私有**赛丽亚房间回到城镇"这件事同步进
// 存在感分桶（op1425 回答之后的落点）。
//
// # 为什么需要它（参考实现口径）
//
// 厂商 9.16 参考实现的日志里，同一动作有两条成对事件（D:\DNF90-test\...\packet_log.txt）：
//
//	:2207 game-town-presence-suppressed source=town_set_user_area_private_seria_room
//	      char_id=2 town_id=38 area_id=1 reason=private_seria_room retired_peer_count=0
//	:16056 game-town-presence-published source=prev_village_return_from_private_seria_room
//	      char_id=1 town_id=38 area_id=2
//
// 即：**进私有房间时把存在感撤出共享桶，回答 op1425 之后按落点重新发布**。
// 我们这一侧用的是"每角色一份实例键"（`townAreaInstanceForMapPath` ⇒ Instance=角色 ID），
// 进房间时换桶是对的，但 `handleCurrentPrevVillage` **从不回写存在感**：如果客户端之后
// 不发 op36（或那次 op36 被拒/丢失），服务端的存在感就永远停在私有房间里 ⇒ 别人看不到他，
// 他也拿不到同屏投影，只有"重新进出该区域"（一次新的 op36）才会被重新登记。
// 这里按厂商同一条口径补齐：用**刚回答给客户端的那个落点**重新登记存在感。
//
// 回退：`DNFBRIDGE_TOWN_SERIA_EXIT_PRESENCE_PUBLISH=off`（也接受 0/false/no/legacy）。
const currentTownSeriaExitPresenceEnv = "DNFBRIDGE_TOWN_SERIA_EXIT_PRESENCE_PUBLISH"

// currentChannelReconnectTownPresenceEnv 是**频道重连进城**那条路由收尾发布存在感的独立回退开关。
//
// 单独一个开关（而不是复用上面那条）的原因：两条路由是各自独立修复的，任何一个出问题
// 都应该能只回退自己，不让另一条已经线上验证过的行为跟着变。
const currentChannelReconnectTownPresenceEnv = "DNFBRIDGE_CHANNEL_RECONNECT_TOWN_PRESENCE_PUBLISH"

// 两条路由各自的 `source` 字段值。参考实现的日志里就是这两个字面量
// （D:\DNF90-test\server\runtime\logs\packet_log.txt:2760/3146/16452 与 :16056），
// 复测时 `grep -a game-town-presence-published` 能直接与厂商那份并排 diff。
const (
	townSeriaExitPresenceSource        = "prev_village_return_from_private_seria_room"
	channelReconnectTownPresenceSource = "channel_reconnect_town_actor_route_complete"
)

func currentTownPresencePublishDisabled(envName string) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(envName)))
	switch value {
	case "0", "off", "false", "no", "legacy":
		return true
	default:
		return false
	}
}

func currentTownSeriaExitPresencePublishEnabled() bool {
	return !currentTownPresencePublishDisabled(currentTownSeriaExitPresenceEnv)
}

func currentChannelReconnectTownPresencePublishEnabled() bool {
	return !currentTownPresencePublishDisabled(currentChannelReconnectTownPresenceEnv)
}

func (s *Service) publishTownPresenceAfterSeriaRoomReturn(
	session *gameSession,
	townID byte,
	areaID byte,
	positionX uint16,
	positionY uint16,
	direction byte,
	areaState byte,
) {
	if !currentTownSeriaExitPresencePublishEnabled() {
		return
	}
	s.publishTownPresenceInArea(
		session,
		townID,
		areaID,
		positionX,
		positionY,
		direction,
		areaState,
		townSeriaExitPresenceSource,
	)
}

// publishTownPresenceInArea 是"按落点重新登记城镇存在感"的**唯一**实现：把这条会话的
// 在线存在从旧桶里摘掉（旧桶对端收 op6 或队友的 op23 区域变更）、登记进新桶、并做一次
// 双向同屏投影，最后记一条 `game-town-presence-published`。
//
// 为什么必须由**路由自己**发布（而不是等客户端的 op36 SET_USER_AREA）：
// `PeerInSameArea` 是城镇内一切点对点交互的硬闸门（邀请、查看信息、交易、选择器 fan-out、
// 整队进副本）。一个玩家只要"人在城里、但服务端区域表里没有他"，邀请就会被静默吞掉
// （客户端表现为"组不上队 / 点了没反应"）。所以每一条**服务端自己把角色放进城镇**的路由
// （登录私房、op36、出副本回城、赛丽亚出房门、频道重连进城）都必须自己收口发布存在感，
// 不能假设客户端随后一定会补一次 op36。
//
// 字段口径：`area_instance != 0` = 落点是"每个角色一份"的私有房间（赛丽亚房间），
// 此时存在只落在本人专属实例桶里、共享桶里没有他 —— 等价于厂商参考实现那条
// `game-town-presence-suppressed reason=private_seria_room`，只是我们把它表达在
// `area_instance` 这个既有字段里，不额外多一个事件名。
func (s *Service) publishTownPresenceInArea(
	session *gameSession,
	townID byte,
	areaID byte,
	positionX uint16,
	positionY uint16,
	direction byte,
	areaState byte,
	source string,
) {
	if s == nil || s.onlinePlayers == nil || session == nil || session.selectedCharacterID == 0 {
		return
	}
	if s.sessionHasActiveDungeonRuntime(session) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID, _, character, hasCharacter := s.selectedCharacterForEnter(ctx, session)
	if !hasCharacter {
		s.logGameEvent(session, "game-town-presence-published-deferred",
			"char_id", session.selectedCharacterID,
			"town_id", townID,
			"area_id", areaID,
			"source", source,
			"reason", "character_unavailable")
		return
	}
	areaInstance := uint32(0)
	mapPath := ""
	if area, found := s.townArea(int64(townID), int64(areaID)); found {
		mapPath = area.MapPath
		var err error
		areaInstance, err = s.currentTownAreaInstance(ctx, session, character, area.MapPath)
		if err != nil {
			s.logGameEvent(session, "game-town-presence-guild-hall-rejected", "source", source, "error", err)
			s.retireCurrentGuildHallPresence(session)
			return
		}
	}
	if isPVPChannel(session.channel) && townID == 10 && areaID == 0 {
		areaInstance = uint32(characterID)
	}
	if isPVPChannel(session.channel) {
		if err := s.sendCurrentPVPDailyState(session); err != nil {
			s.logGameEvent(session, "决斗战绩初始化失败", "error", err)
		}
		if err := s.sendCurrentPVPDirectory(session); err != nil {
			s.logGameEvent(session, "决斗房间目录发送失败", "error", err)
		}
	}
	raidBattle := ""
	playerInfo := &onlinePlayerInfo{
		CharacterID:        characterID,
		AccountID:          character.AccountID,
		Name:               character.Name,
		Job:                byte(numericCharacterStat(character.Job)),
		GrowType:           characterGrowTypeByte(character),
		Level:              byte(character.Level),
		TownID:             townID,
		AreaID:             areaID,
		ChannelID:          currentTownPresenceChannelID(session),
		Instance:           areaInstance,
		RaidBattleInstance: raidBattle,
		PositionX:          positionX,
		PositionY:          positionY,
		Direction:          direction,
		AreaState:          areaState,
		Session:            session,
	}
	partyStateBefore := runtimePartyStateSnapshot(session)
	raidInstance, raidBattle, admitted, releaseRaid := s.lockCurrentAntonWaitingAreaScope(session, townID, areaID)
	if !admitted {
		return
	}
	if raidInstance != 0 {
		playerInfo.Instance = raidInstance
	}
	playerInfo.RaidBattleInstance = raidBattle
	_, oldOthers := s.onlinePlayers.LeaveArea(characterID)
	newOthers := s.onlinePlayers.EnterArea(playerInfo)
	releaseRaid()
	if len(oldOthers) > 0 {
		s.broadcastTownPlayerAreaChange(playerInfo, oldOthers, partyStateBefore)
	}
	s.broadcastTownPlayerEnter(playerInfo, newOthers)
	s.logGameEvent(session, "game-town-presence-published",
		"source", source,
		"char_id", characterID,
		"town_id", townID,
		"area_id", areaID,
		"area_key_channel", playerInfo.ChannelID,
		"area_instance", areaInstance,
		"map_path", mapPath,
		"position_x", positionX,
		"position_y", positionY,
		"direction", direction,
		"area_state", areaState,
		"same_area_peers", len(newOthers),
		"sequence", "remote_mode0_then_mode1_then_op9_then_op23_both_directions")
}
