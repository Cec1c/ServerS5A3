package dnfbridge

import (
	"os"
	"strings"
)

// 本文件是操作者第 4 条报障里"队员自行退出后城镇内看不到队长"那一半。
//
//	「通关副本后点击返回城镇：队伍虽然还在，但是状态异常；队员没有被带出副本；
//	  队员自行退出后城镇内看不到队长；队长进副本也不能带队员进入了。」
//
// # 现场：通关回城之后**一次城镇同屏都没有发生**
//
// 两次通关回城（leader 16:14:33.417 / member 16:14:45.709）之后，两名角色在
// 城镇 31 里各走各的，全窗口**零条**同屏投影：
//
//	$ awk 'NR>=153600' packet_log.txt | grep -c 'kind=game-town-copresence-actor-state-send'
//	0
//	（同一份日志 147000 行之后 = 13 条，全部发生在这次通关回城之前）
//
// 队长侧只有反复的 `game-upper-select-character-scene-tail-deferred ... char_id=1
// reason=wait_for_current_exe_legacy_type1345_u32_2_scene_ready`，**从来没有**
// `game-town-set-user-area-committed conn_id=game-000003`；队员侧有
// `:153762 game-town-set-user-area-committed conn_id=game-000007 char_id=6 town_id=31 area_id=1`。
//
// # 成因（file:line）：通关回城这条路线**漏了**放弃回城那条路线已有的两个收口
//
// "副本返回城镇不会走 SET_USER_AREA"这件事，本仓已经在**待决回城**那条提交点收过口
// （`dungeon_return_town.go:761-767`，注释就是为这条缺陷写的）：
//
//	s.rebindTownCopresenceAfterDungeonReturn(session, townID, areaID, source+"_copresence_rebind")
//	s.ensureCurrentConfirmedDungeonReturnPlayerState(...)   ← :828-832 里置 townSceneReadyCharacterID
//
// 但**通关回城**走的是另一条提交点 `sendCurrentCompletedDungeonReturnToTown`
// （`dungeon_return_town.go:480`）：它在 `:597-609` 摘掉运行时、`:616` 只调
// `settleRuntimePartyOnDungeonCompletionReturn`，**既没有** rebind 城镇同屏，
// **也没有**把本会话标成"城镇场景就绪"。而上一轮加的同屏重建
// （`party_town_return.go:17 rebuildRuntimePartyTownCoPresenceAfterReturn`）第一道闸门就是
//
//	currentPartyTownReturnSceneReady   party_town_return.go:92
//	  townSceneReadyCharacterID == selectedCharacterID && townPositionSnapshot.CharacterID == ...
//	  && townPositionSnapshot.PositionValid
//
// ⇒ 队长这条会话永远不满足 ⇒ 那个调用**永远**是 no-op（`game-party-town-return-copresence-rebuilt`
// 全日志 0 条）。队员那条 op42（`request_msg_id=42`，`game-dungeon-completed-town-route-committed
// conn_id=game-000007`）走的也是同一个 `sendCurrentCompletedDungeonReturnToTown`，
// 所以**两个人**都没被登记 ⇒ 互相看不见，必须出城再进城（新一次 op36）才会重新投影。
//
// # 修法（全部复用既有原语，不新增机制）
//
// 在通关回城的第一次提交之后（锁外）补三件**既有**动作：
//
//  1. 用这次回城**已经解析出来**的落点（`transition`，坐标来源是客户端 op35 的
//     `current_exe_op35_runtime_origin_snapshot`，不是发明出来的）把本会话标成
//     城镇场景就绪 + 写入城镇位置快照（与 `town_position.go:200-213` 同一形状）；
//  2. 提交本会话的 town actor owner 上下文并调既有
//     `rebindTownCopresenceAfterDungeonReturn`（`online_player_manager.go:604`）——
//     它就是"出副本回城要重新登记城镇同屏"的唯一收口点；
//  3. 再用既有的 `forgetStaleDungeonPartyRunKeyOutsideDungeon`
//     （`dungeon_party_run_key_town_reset.go:110`）作废本会话的副本运行键 ——
//     通关回城**不解散队伍**，所以上一轮那条"解散即清键"在这里不会被触发，
//     留着就会让下一次进图继承上一场的副本上下文（操作者第 4 条的"状态异常"
//     与"队长进副本也不能带队员进入"同源）。清键会连带结束队友投影世代
//     （`dungeon_party_peer_projection_generation.go`）。
//
// # 开关
//
//	`DNFBRIDGE_PARTY_DUNGEON_COMPLETION_RETURN_TOWN_COPRESENCE`
//	  - 默认（空/未设置/非法值）= **on**（修复档）：通关回城登记城镇同屏 + 作废运行键；
//	  - `off` / `0` / `false` / `no` / `legacy` = 逐字节回到改动前（不登记、不清键、
//	    不记本文件的事件；现场即为"回城后互相看不见、要走一段城镇地图"）。
const dungeonPartyCompletionReturnTownCopresenceEnv = "DNFBRIDGE_PARTY_DUNGEON_COMPLETION_RETURN_TOWN_COPRESENCE"

func dungeonPartyCompletionReturnTownCopresenceEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(dungeonPartyCompletionReturnTownCopresenceEnv))) {
	case "0", "off", "false", "no", "legacy":
		return false
	default:
		return true
	}
}

func dungeonPartyCompletionReturnTownCopresenceMode() string {
	if dungeonPartyCompletionReturnTownCopresenceEnabled() {
		return cloneAvatarAppearanceOn
	}
	return cloneAvatarAppearanceOff
}

// publishCompletedDungeonReturnTownPresence 在**通关回城**提交之后（锁外）把本会话
// 登记进它刚回去的那个城镇区域，并作废本会话的副本运行键。
//
// 返回值 = 是否真的登记过（回退档 / 单人 / 落点无效时为 false）。
func (s *Service) publishCompletedDungeonReturnTownPresence(
	session *gameSession,
	transition currentDungeonTownTransition,
	source string,
) bool {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return false
	}
	if !dungeonPartyCompletionReturnTownCopresenceEnabled() {
		return false
	}
	characterID := session.selectedCharacterID
	// transition 的坐标是 i16（`currentDungeonTownTransition.PositionX/Y`），
	// 城镇位置快照是 u16（同一段 i16 位型，见 town_position.go:29-38 与 op35 读法）。
	positionX := uint16(transition.PositionX)
	positionY := uint16(transition.PositionY)
	positionValid := transition.TownID != 0 && transition.PositionSource != "" &&
		transition.PositionSource != "character_repository_stats"
	session.townMu.Lock()
	if session.selectedCharacterID != characterID {
		session.townMu.Unlock()
		return false
	}
	clearCurrentTownSelectorOriginLocked(session)
	if positionValid {
		resetCurrentTownEnterSelectSceneLocked(session)
		session.townSceneReadyCharacterID = characterID
		session.townPositionSnapshot = currentTownPositionSnapshot{
			CharacterID:   characterID,
			TownID:        transition.TownID,
			AreaID:        transition.AreaID,
			PositionX:     positionX,
			PositionY:     positionY,
			MovementCode:  transition.Direction,
			PositionValid: true,
		}
	}
	if currentTownActorOwnerContext(session) == currentSceneObjectContext {
		session.townActorOwnerChannel = currentConnectionTownActorOwnerContext(session)
	}
	session.townMu.Unlock()
	if !positionValid {
		s.logGameEvent(session, "game-dungeon-completed-town-route-town-presence-skipped",
			"source", source,
			"char_id", characterID,
			"town_id", transition.TownID,
			"area_id", transition.AreaID,
			"position_source", transition.PositionSource,
			"reason", "completed_town_route_has_no_client_reported_town_origin_to_register",
			"switch", dungeonPartyCompletionReturnTownCopresenceEnv,
			"mode", dungeonPartyCompletionReturnTownCopresenceMode())
		return false
	}
	// 既有收口点：出副本回城不会走 SET_USER_AREA，必须在这里重新登记城镇同屏。
	s.rebindTownCopresenceAfterDungeonReturn(
		session,
		transition.TownID,
		transition.AreaID,
		source+"_copresence_rebind",
	)
	// 通关回城不解散队伍 ⇒ 上一轮的"解散即清键"不会触发；这里按既有判据
	// （会话已无活跃副本运行时）作废本会话的运行键，并连带结束队友投影世代。
	runKeyCleared := s.forgetStaleDungeonPartyRunKeyOutsideDungeon(
		session,
		source+"_run_key_reset",
	)
	s.logGameEvent(session, "game-dungeon-completed-town-route-town-presence-published",
		"source", source,
		"char_id", characterID,
		"conn_id", session.connID,
		"town_id", transition.TownID,
		"area_id", transition.AreaID,
		"position_x", positionX,
		"position_y", positionY,
		"position_source", transition.PositionSource,
		"stale_run_key_cleared", runKeyCleared,
		"reason", "the_completed_town_route_must_register_the_town_area_like_the_pending_return_does",
		"switch", dungeonPartyCompletionReturnTownCopresenceEnv,
		"mode", dungeonPartyCompletionReturnTownCopresenceMode())
	return true
}
