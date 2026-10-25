package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/guild"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentTownSetUserAreaBodySize = 16

type currentTownSetUserAreaRequest struct {
	TownID       byte
	AreaID       byte
	PositionX    int16
	PositionY    int16
	Direction    byte
	OpaqueU16A   uint16
	OpaqueU16B   uint16
	OpaqueU32    uint32
	OpaqueTailU8 byte
}

func parseCurrentTownSetUserAreaRequest(body []byte) (currentTownSetUserAreaRequest, error) {
	if len(body) != currentTownSetUserAreaBodySize {
		return currentTownSetUserAreaRequest{}, fmt.Errorf("current town op36 body length %d, want %d", len(body), currentTownSetUserAreaBodySize)
	}
	return currentTownSetUserAreaRequest{
		TownID:       body[0],
		AreaID:       body[1],
		PositionX:    int16(binary.LittleEndian.Uint16(body[2:4])),
		PositionY:    int16(binary.LittleEndian.Uint16(body[4:6])),
		Direction:    body[6],
		OpaqueU16A:   binary.LittleEndian.Uint16(body[7:9]),
		OpaqueU16B:   binary.LittleEndian.Uint16(body[9:11]),
		OpaqueU32:    binary.LittleEndian.Uint32(body[11:15]),
		OpaqueTailU8: body[15],
	}, nil
}

func (s *Service) handleTownSetUserArea(session *gameSession, body []byte) error {
	s.pvpMu.Lock()
	inPVPRoom := session != nil && session.pvpRoom != nil
	s.pvpMu.Unlock()
	if inPVPRoom {
		return nil
	}
	return s.handleTownSetUserAreaWithQuestNotice(session, body, true)
}

// 原生 op36 请求需要任务提示；药剂、单人传送和团本内部移动由各自流程负责反馈，
// 复用移动逻辑时关闭此提示，避免给其他命令额外发送 op36 失败包。
func (s *Service) handleTownSetUserAreaWithQuestNotice(session *gameSession, body []byte, notifyQuestFailure bool) error {
	return s.handleTownSetUserAreaWithCommit(session, body, notifyQuestFailure, nil)
}

func (s *Service) handleTownSetUserAreaAfterGuildTeleport(session *gameSession, body []byte, destination guild.TeleportLocation) error {
	return s.handleTownSetUserAreaWithCommit(session, body, false, &destination)
}

func (s *Service) handleTownSetUserAreaWithCommit(session *gameSession, body []byte, notifyQuestFailure bool, committed *guild.TeleportLocation) error {
	if session == nil {
		return nil
	}
	request, err := parseCurrentTownSetUserAreaRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-town-set-user-area-blocked",
			"body_len", len(body),
			"reason", "current_exe_op36_writer_boundary_mismatch",
			"error", err)
		return nil
	}
	if session.selectedCharacterID == 0 {
		s.logTownSetUserAreaBlocked(session, request, "selected_character_missing")
		return nil
	}
	fplan, err := s.prepareCurrentFiendWarMove(session, request)
	if err != nil {
		s.logTownSetUserAreaBlocked(session, request, "超时空区域移动被拒绝")
		return nil
	}
	defer s.cancelCurrentFiendWarMove(fplan)
	session.dungeon.mu.Lock()
	pendingDungeonReturn := session.dungeon.runtime != nil &&
		session.dungeon.runtime.Session != nil &&
		session.dungeon.runtime.townReturnPending
	session.dungeon.mu.Unlock()
	if !pendingDungeonReturn {
		session.townMu.Lock()
		townSceneReadyCharacterID := session.townSceneReadyCharacterID
		session.townMu.Unlock()
		if townSceneReadyCharacterID != session.selectedCharacterID {
			s.logTownSetUserAreaBlocked(session, request, "town_scene_player_state_not_finalized")
			return nil
		}
	}
	refreshQuestsAfterDungeonReturn := pendingDungeonReturn
	if err := s.commitPendingDungeonReturnForSceneRequest(session, "current_exe_op36_town_area_request"); err != nil {
		return err
	}
	session.dungeon.mu.Lock()
	hasDungeonRuntime := session.dungeon.runtime != nil
	session.dungeon.mu.Unlock()
	if hasDungeonRuntime {
		// 城镇过图提交仍然被拒绝（旧信号与旧语义一字未动），但副本运行时还挂着时收到的
		// op36 是**副本内**唯一一条带"区域 + 坐标"的上报候选 ⇒ 交给副本分支记录并转发。
		// 现场实测（2026-09-25 04:3x~04:5x，见 dungeon_party_peer_position.go 的取证注释）
		// 副本内一条 op35/op36 都没有，所以这条分支线上零效果、只为将来客户端变体留挂钩。
		s.handleDungeonActiveSetUserAreaReport(session, request)
		return nil
	}

	if isPVPChannel(session.channel) && request.TownID != 10 {
		return nil
	}
	// 操作者清单第 36 条（赛丽亚房间右边门应弹选择地图）的默认关闭实验档。
	// 开关关闭时这里只做一次环境变量比较就返回：不读库、不记日志、不发字节，
	// 逐字节等于改动前。判据与取证见 town_seria_right_door_select.go。
	// 位置要求：必须在 `session.townMu` 之外调用（sendEnterSelectDungeonState 自己会取这把锁）。
	if seriaRightDoorSelectEnabled() {
		handled, err := s.tryHandleSeriaRoomRightDoorSelect(session, request)
		if err != nil {
			return err
		}
		if handled {
			return nil
		}
	}

	session.townMu.Lock()
	completedTailOwner := captureCurrentCompletedTownSceneTailOwnerLocked(session)
	returnRaidGateAfterScopeLoss := false
	defer func() {
		session.townMu.Unlock()
		if returnRaidGateAfterScopeLoss {
			if err := s.returnCurrentAntonMemberToGate(session); err != nil {
				s.logWarn("raid waiting scope loss gate return failed", "error", err)
			}
		}
	}()
	teleportArrayRequest := request.LooksLikeTeleportArraySelection()
	townMapStationRequest := request.LooksLikeTownMapStationSelection()
	townTransportRequest := request.LooksLikeTownTransportSelection()
	townSeriaRoomPortalRequest := false
	townCrossTownPortalRequest := false
	area, found := s.townArea(int64(request.TownID), int64(request.AreaID))
	if !found {
		s.logTownSetUserAreaBlocked(session, request, "target_town_area_missing_from_runtime_pvf")
		return nil
	}
	raidInstance, raidBattle, admitted, releaseRaid := s.lockCurrentAntonWaitingAreaScope(session, request.TownID, request.AreaID)
	if !admitted {
		s.logTownSetUserAreaBlocked(session, request, "anton_waiting_area_requires_current_owned_raid_membership")
		return nil
	}
	releaseRaid()
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil {
		s.logTownSetUserAreaBlocked(session, request, "character_repository_unavailable")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	character, found, err := repositories.Character.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if !found || character.Stats == nil {
		s.logTownSetUserAreaBlocked(session, request, "selected_character_location_missing")
		return nil
	}
	if committed != nil && guild.CharacterTeleportLocation(character) != *committed {
		// A later scene change already won; never replay an old committed trip.
		return nil
	}
	areaInstance, err := s.currentTownAreaInstance(ctx, session, character, area.MapPath)
	if err != nil {
		s.logTownSetUserAreaBlocked(session, request, "guild_hall_requires_current_owned_membership")
		return s.rejectCurrentGuildHallMove(session, request)
	}
	if raidInstance != 0 {
		areaInstance = raidInstance
	}
	if isCurrentFiendWarChannel(session) {
		character = dnfrepo.CloneCharacter(character)
		character.Stats["town_id"] = int64(session.townPositionSnapshot.TownID)
		character.Stats["area_id"] = int64(session.townPositionSnapshot.AreaID)
	}
	currentTown, ok := character.Stats["town_id"]
	if !ok {
		s.logTownSetUserAreaBlocked(session, request, "selected_character_town_id_missing")
		return nil
	}
	if s.options.currentClientVersion == "official-24.9" && request.TownID == 50 && currentTown != 50 && !s.currentEpicRoadActivityAvailable() {
		s.logTownSetUserAreaBlocked(session, request, "current_epic_road_activity_unavailable")
		return nil
	}
	townCrossTownPortalRequest = request.LooksLikeTownCrossTownPortalSelection(currentTown)
	townSeriaRoomPortalRequest = request.LooksLikeTownSeriaRoomPortalSelection(currentTown, area.MapPath)
	townTransportRequest = townTransportRequest || townCrossTownPortalRequest || townSeriaRoomPortalRequest
	// The current NPC enter-town writer carries the destination NPC ID in
	// its u32 field. Admit only an actual PVF NPC connection and landing;
	// the existing level and completed-quest checks below still apply.
	if currentTown != int64(request.TownID) && !townTransportRequest {
		townTransportRequest = s.currentTownNPCEnterTownRequest(session, &request, character, area)
	}
	if currentTown != int64(request.TownID) && !townTransportRequest {
		s.logTownSetUserAreaBlocked(session, request, "cross_town_route_not_proven_by_current_pvf_topology")
		return nil
	}
	// 瞬间移动药剂那条链路（town_teleport.go）在提交期间把这个标志置位：道具传送的
	// 目标是"其他地区的任意位置"，区域门槛（[need level]/[need quest]）是给走门进图
	// 用的，套在道具上会让药水被静默拒绝（药水不消耗、客户端也收不到回应）。
	// 开关 DNFBRIDGE_TELEPORT_POTION_AREA_GATES=strict 可回退成按 PVF 门槛拒绝。
	relaxAreaGates := session.townTeleportPotionLandingRelaxed
	if area.MinLevel > int64(character.Level) && !relaxAreaGates {
		s.logTownSetUserAreaBlocked(session, request, "target_area_min_level_not_met")
		return nil
	}
	areaState, ok := character.Stats["area_state"]
	if !ok || areaState < 0 || areaState > int64(^byte(0)) {
		s.logTownSetUserAreaBlocked(session, request, "selected_character_area_state_missing_or_invalid")
		return nil
	}
	if len(area.NeedQuests) > 0 && !relaxAreaGates {
		needQuestsCompleted, missingQuestID, questErr := s.townAreaQuestRequirement(ctx, repositories, characterID, area.NeedQuests)
		if questErr != nil {
			s.logGameEvent(session, "game-town-set-user-area-need-quest-check-failed",
				"char_id", session.selectedCharacterID,
				"town_id", request.TownID,
				"area_id", request.AreaID,
				"target_need_quests", area.NeedQuests,
				"town_transport_request", townTransportRequest,
				"error", questErr)
		}
		if !needQuestsCompleted {
			s.logTownSetUserAreaBlocked(session, request, "target_area_has_pvf_quest_requirement_without_completed_quest_owner")
			if questErr == nil && notifyQuestFailure {
				return s.sendTownAreaQuestRequirementFailure(session, request, missingQuestID)
			}
			return nil
		}
	}
	prevVillageOrigin := currentTownPositionSnapshot{}
	prevVillageOriginBound := false
	if townMapPathLooksLikeSeria(area.MapPath) {
		if origin, ok := s.currentTownSeriaReturnOriginLocked(session, session.selectedCharacterID, character); ok {
			prevVillageOrigin = origin
			prevVillageOriginBound = true
			session.townPrevVillageSnapshot = origin
			// 这才是"我从哪个城镇走门进来的"：出房间时按它回到来源城镇/区域。
			session.townSeriaRoomEntrySnapshot = origin
			s.logTownPrevVillageOriginBound(session, origin, "current_exe_op36_enter_seria_room")
		} else {
			// 连来源都解析不出来：明确清空，绝不留下上一次进房的陈旧来源给后面的 op1425 用。
			session.townSeriaRoomEntrySnapshot = currentTownPositionSnapshot{}
			s.logTownPrevVillageOriginBindFailed(session, session.selectedCharacterID, "current_location_or_position_unavailable_before_enter_seria_room")
		}
	} else {
		// 走到任何非赛丽亚房间的区域都会作废"进门来源"，避免陈旧来源被下一次出房复用。
		session.townSeriaRoomEntrySnapshot = currentTownPositionSnapshot{}
	}

	previous := dnfrepo.CloneCharacter(character)
	character = dnfrepo.CloneCharacter(character)
	if prevVillageOriginBound {
		currentTownPrevVillageApplyOriginStats(character.Stats, prevVillageOrigin)
	}
	character.Stats["town_id"] = int64(request.TownID)
	character.Stats["area_id"] = int64(request.AreaID)
	character.Stats["pos_x"] = int64(request.PositionX)
	character.Stats["pos_y"] = int64(request.PositionY)
	character.Stats["direction"] = int64(request.Direction)
	character.UpdatedAt = time.Now().UTC()
	if committed == nil && !isPVPChannel(session.channel) && !isCurrentFiendWarChannel(session) {
		if err := dnfrepo.SaveCharacterFields(ctx, repositories.Character, character, dnfrepo.CharacterFieldStats); err != nil {
			return err
		}
	}

	selfRow := currentSceneTransitionRow{
		ObjectOrResourceKey: currentSceneActorObjectKey(session.selectedCharacterID),
		Value1:              uint16(request.PositionX),
		Value2:              uint16(request.PositionY),
		Value3:              request.Direction,
		Value4:              byte(areaState),
	}
	transitionRows := make([]currentSceneTransitionRow, 0, 1)
	if s.onlinePlayers != nil {
		// op24 名册只列**本频道**的同城同区玩家：跨频道角色不在本地客户端场景里，
		// 列进去就等于把别的频道的人塞进本地画面（"10 线见到 121 线"）。
		presenceChannelID := currentTownPresenceChannelID(session)
		areaPlayers := s.onlinePlayers.GetAreaScopePlayers(request.TownID, request.AreaID, presenceChannelID, areaInstance, raidBattle)
		sort.Slice(areaPlayers, func(i, j int) bool {
			return areaPlayers[i].CharacterID < areaPlayers[j].CharacterID
		})
		for i := range areaPlayers {
			if areaPlayers[i].CharacterID == 0 ||
				areaPlayers[i].CharacterID == session.selectedCharacterID {
				continue
			}
			transitionRows = append(transitionRows, currentSceneTransitionRow{
				ObjectOrResourceKey: currentSceneActorObjectKey(areaPlayers[i].CharacterID),
				Value1:              areaPlayers[i].PositionX,
				Value2:              areaPlayers[i].PositionY,
				Value3:              areaPlayers[i].Direction,
				Value4:              areaPlayers[i].AreaState,
			})
		}
	}
	// Keep the selected actor last. The current EXE's op24 parser binds the
	// final row as the camera/selected actor while earlier rows form the peer
	// roster, matching the full-area roster role used by 86JP after it sends
	// peer appearance and user-info records.
	transitionRows = append(transitionRows, selfRow)
	response, err := buildCurrentSceneTransitionBody(request.TownID, request.AreaID, transitionRows)
	if err != nil {
		return err
	}
	rollbackLocation := func(writeErr error) error {
		if committed != nil || isPVPChannel(session.channel) || isCurrentFiendWarChannel(session) {
			return writeErr
		}
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), createWriteTimeout)
		rollbackErr := dnfrepo.SaveCharacterFields(rollbackCtx, repositories.Character, previous, dnfrepo.CharacterFieldStats)
		rollbackCancel()
		if rollbackErr != nil {
			s.logGameEvent(session, "game-town-set-user-area-rollback-failed",
				"char_id", session.selectedCharacterID,
				"town_id", request.TownID,
				"area_id", request.AreaID,
				"write_error", writeErr,
				"rollback_error", rollbackErr)
		}
		return writeErr
	}
	s.logGameEvent(session, "game-town-set-user-area-transition-send",
		"char_id", session.selectedCharacterID,
		"request_msg_id", uint16(dnfenum.CmdPacketSetUserArea),
		"response_msg_ids", []uint16{
			currentTownUserPositionNotificationMsgID,
			currentTownUserAreaNotificationMsgID,
			currentClearQuestListMsgID,
			currentSceneTransitionMsgID,
		},
		"town_id", request.TownID,
		"area_id", request.AreaID,
		"position_x", request.PositionX,
		"position_y", request.PositionY,
		"direction", request.Direction,
		"area_state", areaState,
		"map_path", area.MapPath,
		"teleport_array_request", teleportArrayRequest,
		"town_map_station_request", townMapStationRequest,
		"town_seria_room_portal_request", townSeriaRoomPortalRequest,
		"town_cross_town_portal_request", townCrossTownPortalRequest,
		"town_transport_request", townTransportRequest,
		"target_need_quests", area.NeedQuests,
		"area_gates_relaxed", relaxAreaGates,
		"opaque_u16_a", request.OpaqueU16A,
		"opaque_u16_b", request.OpaqueU16B,
		"opaque_u32", request.OpaqueU32,
		"opaque_tail_u8", request.OpaqueTailU8,
		"row_count", len(transitionRows),
		"peer_row_count", len(transitionRows)-1,
		"op24_body_len", len(response),
		"sequence", "op22_user_position_then_op23_user_area_then_optional_op356_completed_quest_state_then_op24_area_users",
		"body_source", "current_exe_op22_sub_1D83990_op23_sub_1D89590_op356_sub_1D58470_op24_sub_1D901D0")
	if err := s.applyCurrentFiendWarMove(fplan); err != nil {
		return rollbackLocation(err)
	}
	if err := s.sendCurrentTownLocationNotifications(
		session,
		session.selectedCharacterID,
		request.TownID,
		request.AreaID,
		selfRow,
		"current_exe_op36_town_area_transition",
	); err != nil {
		return rollbackLocation(err)
	}
	// Initial town bootstrap installs the completed-quest bitmap before op24.
	// Ordinary town transitions need the same order because op24 constructs the
	// destination scene; a later op356 cannot restore an omitted quest gate.
	if _, err := s.sendCurrentPersistedClearQuestListIfCompleted(
		ctx,
		session,
		repositories,
		"current_exe_op36_after_op23_before_op24_seed_completed_quest_scene_state",
	); err != nil {
		return rollbackLocation(err)
	}
	if request.TownID == 10 && request.AreaID == 4 {
		if err := s.sendCurrentPVPDojoSkillsBeforeTransition(ctx, session, character); err != nil {
			return rollbackLocation(err)
		}
	}
	if err := s.sendGameUpperRawClass(session, currentSceneTransitionMsgID, response, 0); err != nil {
		return rollbackLocation(err)
	}
	finalRaidInstance, finalRaidBattle, finalAdmitted, releaseFinalRaid := s.lockCurrentAntonWaitingAreaScope(session, request.TownID, request.AreaID)
	if !finalAdmitted || finalRaidInstance != raidInstance || (raidBattle != "" && finalRaidBattle != raidBattle) {
		releaseFinalRaid()
		_ = rollbackLocation(nil)
		returnRaidGateAfterScopeLoss = raidInstance != 0
		s.logTownSetUserAreaBlocked(session, request, "raid_scope_changed_before_scene_commit")
		return nil
	}
	releaseFinalRaid()
	raidBattle = finalRaidBattle
	setCurrentTownPositionSceneLocked(session, session.selectedCharacterID, request.TownID, request.AreaID)
	session.initialTownRouteCharacterID = 0
	session.initialTownRouteStage = currentInitialTownRouteIdle
	session.initialTownLocationNotificationsSent = false
	session.initialTownQuestSnapshotsSent = false
	session.setFinishLoadingGates(finishLoadingGates{})
	session.initialTownLegacySceneReadyAccepted = false
	session.initialTownAdventureOverheadRefreshSent = false
	session.initialTownCombatPowerAffixesSent = false
	session.enterSelectDungeonSent = false
	session.enterSelectDungeonAckSent = false
	session.enterSelectDungeonContextSent = false
	session.townTransportEnterSelectPending = townTransportRequest
	// 布防同时记时刻：挂账只覆盖"紧跟 op36 的那条场景收尾 op15"，窗口见
	// game_enter_select_dungeon.go 的 currentTownTransportEnterSelectWindow。
	// 只有真的布防时才写（普通城镇过图不布防，也就没有"挂账时刻"可言）。
	if townTransportRequest {
		session.townTransportEnterSelectArmedAt = time.Now()
	}
	session.townSceneReadyCharacterID = session.selectedCharacterID

	// Register player in the new area and broadcast to others.
	var newAreaOthers []onlinePlayerInfo
	if s.onlinePlayers != nil {
		characterID := session.selectedCharacterID
		partyStateBeforeTownMove := runtimePartyStateSnapshot(session)
		// 赛丽亚的房间在客户端是"每个角色一份"的私有房间（现场报障：别人能跟进来看见），
		// 所以进入它时把区域键标记成该角色独占的实例；普通城镇区域仍为共享（Instance=0）。

		playerInfo := &onlinePlayerInfo{
			CharacterID: characterID,
			AccountID:   character.AccountID,
			Name:        character.Name,
			Job:         byte(numericCharacterStat(character.Job)),
			GrowType:    characterGrowTypeByte(character),
			Level:       byte(character.Level),
			TownID:      request.TownID,
			AreaID:      request.AreaID,
			// 同屏分桶键的频道分量：由 EnterArea 从 Session 再确认一次
			// （resolvePresenceChannelID），这里显式写出便于阅读与日志核对。
			ChannelID:          currentTownPresenceChannelID(session),
			Instance:           areaInstance,
			RaidBattleInstance: raidBattle,
			PositionX:          uint16(request.PositionX),
			PositionY:          uint16(request.PositionY),
			Direction:          request.Direction,
			AreaState:          byte(areaState),
			Session:            session,
		}
		if areaInstance != 0 {
			s.logGameEvent(session, "game-town-private-area-entered",
				"char_id", characterID,
				"town_id", request.TownID,
				"area_id", request.AreaID,
				"map_path", area.MapPath,
				"area_key_channel", currentTownPresenceChannelID(session),
				"area_instance", areaInstance,
				"reason", "per-character private room keyed away from the shared town area")
		}
		// A player store is scoped to one concrete town scene. Close it before
		// removing the owner's old-area presence so every old peer receives the
		// native close notification and stale visitors are detached.
		s.closeCurrentExpertJobStoreSession(session, true)
		finalRaidInstance, finalRaidBattle, finalAdmitted, releaseCommitRaid := s.lockCurrentAntonWaitingAreaScope(session, request.TownID, request.AreaID)
		if !finalAdmitted || finalRaidInstance != raidInstance || finalRaidBattle != raidBattle {
			releaseCommitRaid()
			_ = rollbackLocation(nil)
			returnRaidGateAfterScopeLoss = raidInstance != 0
			return nil
		}
		// Leave the old area. Ordinary peers receive op6. Party peers retain the
		// same actor identity and receive only its op23 area change, because op6
		// also removes that member from the current client's party manager.
		_, oldOthers := s.onlinePlayers.LeaveArea(characterID)
		// Enter new area.
		newOthers := s.onlinePlayers.EnterArea(playerInfo)
		releaseCommitRaid()
		if len(oldOthers) > 0 {
			s.broadcastTownPlayerAreaChange(playerInfo, oldOthers, partyStateBeforeTownMove)
		}
		newAreaOthers = append(newAreaOthers, newOthers...)
		s.promoteResidentGameSession(session, characterID)
		s.broadcastTownPlayerEnter(playerInfo, newOthers)
		s.replayCurrentExpertJobStores(session, request.TownID, request.AreaID)
	}
	// SET_USER_AREA rebuilds the current EXE's scene-owned party manager. Peer
	// actor projections alone are not sufficient: whichever member crosses the
	// town boundary last can otherwise retain only the selected actor row and
	// lose its own leader/member binding. Replay only the authoritative op9
	// roster after the new-area actors have been installed. The op11 endpoint
	// exchange belongs to party formation and must not be repeated here.
	partyStateAfterTownMove := s.townCopresenceActorPartyState(session)
	if partyStateAfterTownMove.PartyID > 0 {
		if err := s.sendTownRemotePartyActors(session, partyStateAfterTownMove); err != nil {
			return err
		}
		if err := s.sendRuntimePartyRosterLocal(session, partyStateAfterTownMove, "town_set_user_area_local_roster_rebind"); err != nil {
			return err
		}
		// Existing same-area party clients have just received the mover's new
		// mode0/mode1 actor. Refresh their selected local roster as the final
		// step so the HUD/minimap keeps the authoritative slot mapping.
		for i := range newAreaOthers {
			peer := newAreaOthers[i].Session
			if peer == nil || peer == session ||
				!containsRuntimePartyMember(runtimePartyMembers(partyStateAfterTownMove), newAreaOthers[i].CharacterID) {
				continue
			}
			peerState := s.townCopresenceActorPartyState(peer)
			if peerState.PartyID != partyStateAfterTownMove.PartyID {
				continue
			}
			if err := s.sendRuntimePartyRosterLocal(peer, partyStateAfterTownMove, "town_set_user_area_same_area_peer_roster_rebind"); err != nil {
				return err
			}
		}
	}

	s.logGameEvent(session, "game-town-set-user-area-committed",
		"char_id", session.selectedCharacterID,
		"town_id", request.TownID,
		"area_id", request.AreaID,
		// area_key_channel / area_instance 是这份在线存在实际落进的分桶键的另两个分量：
		// 复测时用它对比两个客户端的 channel_id，即可判定是否同桶（同频道才应互相可见）。
		"area_key_channel", currentTownPresenceChannelID(session),
		"area_instance", areaInstance,
		"raid_battle_instance", raidBattle,
		"position_x", request.PositionX,
		"position_y", request.PositionY,
		"map_path", area.MapPath,
		"teleport_array_request", teleportArrayRequest,
		"town_map_station_request", townMapStationRequest,
		"town_seria_room_portal_request", townSeriaRoomPortalRequest,
		"town_cross_town_portal_request", townCrossTownPortalRequest,
		"town_transport_request", townTransportRequest,
		"target_need_quests", area.NeedQuests,
		"finish_loading_state_rearmed", true,
		"quest_refresh_after_dungeon_return", refreshQuestsAfterDungeonReturn)
	// The actor-bound inventory bootstrap owns the normal cold-login op898.
	// Keep the first client-authored op36 as a one-shot fallback for routes that
	// bypassed that bootstrap; by this point the current NoPack class0 handler
	// sub_1E7FAD0 can also see the inventory-panel owner. Keep this optional:
	// town movement must not fail merely because a minimal test runtime has no
	// premium/PVF catalog.
	if err := s.sendCurrentCrystalContractTownUIReadyState(session); err != nil {
		s.logGameEvent(session, "game-crystal-contract-town-area-state-deferred",
			"char_id", session.selectedCharacterID,
			"town_id", request.TownID,
			"area_id", request.AreaID,
			"reason", "runtime_pvf_or_repository_state_unavailable",
			"error", err)
	}
	if refreshQuestsAfterDungeonReturn {
		if err := s.sendCurrentAcceptableQuestListForSession(session, "current_exe_op36_after_dungeon_return_committed"); err != nil {
			return err
		}
	}
	if fplan != nil {
		fplan.confirmed = true
		if fplan.instance != "" {
			copy := *fplan
			session.fiendWarPendingMove = &copy
			// Preserve the destination already committed through op24; automatic
			// encounter admission must not wait for a new movement in that room.
			session.townPositionSnapshot.PositionX = uint16(request.PositionX)
			session.townPositionSnapshot.PositionY = uint16(request.PositionY)
			session.townPositionSnapshot.MovementCode = request.Direction
			session.townPositionSnapshot.PositionValid = true
		}
		s.queueCurrentFiendWarEncounterFrom(session, currentFiendWarAdmissionPhaseTownArea)
	}
	finishCurrentCompletedTownSceneTailAfterAreaCommitLocked(session, completedTailOwner)
	return nil
}

func (s *Service) townAreaNeedQuestsCompleted(
	ctx context.Context,
	repositories dnfrepo.Group,
	characterID string,
	needQuests []int64,
) (bool, error) {
	completed, _, err := s.townAreaQuestRequirement(ctx, repositories, characterID, needQuests)
	return completed, err
}

// 同一次任务读取同时给出准入结果和首个未完成任务，避免提示与拒绝使用不同快照。
func (s *Service) townAreaQuestRequirement(ctx context.Context, repositories dnfrepo.Group, characterID string, needQuests []int64) (bool, int64, error) {
	if len(needQuests) == 0 {
		return true, 0, nil
	}
	if repositories.Quest == nil {
		return false, 0, dnfrepo.ErrRepoMissing
	}
	record, found, err := repositories.Quest.Load(ctx, characterID)
	if err != nil {
		return false, 0, err
	}
	if !found || record.CharacterID != characterID {
		for _, questID := range needQuests {
			if questID > 0 {
				return false, questID, nil
			}
		}
		return false, 0, nil
	}
	for _, questID := range needQuests {
		if questID <= 0 {
			continue
		}
		if !townQuestStateCompleted(record, questID) {
			return false, questID, nil
		}
	}
	return true, 0, nil
}

func townQuestStateCompleted(record dnfrepo.QuestRecord, questID int64) bool {
	completed := false
	if state, ok := record.Progress[questID]; ok {
		completed = isTownQuestCompletedStatus(state.Status)
	}
	if state, ok := record.States[questID]; ok {
		completed = isTownQuestCompletedStatus(state.Status)
	}
	return completed
}

func isTownQuestCompletedStatus(status string) bool {
	switch normalizeDungeonQuestStatus(status) {
	case "complete", "completed", "cleared", "finished", "done":
		return true
	default:
		return false
	}
}

// LooksLikeTeleportArraySelection identifies the current client's town
// teleport-array selection shape observed from the live 23.4.15.0 EXE. The
// destination itself is still validated against the runtime PVF town catalog;
// this only prevents server-side quest/topology guards from rejecting a
// client-visible teleport destination before op24 can be emitted.
func (request currentTownSetUserAreaRequest) LooksLikeTeleportArraySelection() bool {
	return request.OpaqueTailU8 == 5 && request.OpaqueU32 == 0 && request.OpaqueU16A != 0
}

// LooksLikeTownMapStationSelection identifies the current EXE world-map /
// town transfer-station op36 shape observed live from West Coast:
// town=40, area=0, opaqueA=40, opaqueB=1, opaqueU32=0, tail=0. It is distinct
// from ordinary route-click bodies whose opaque fields are zero.
func (request currentTownSetUserAreaRequest) LooksLikeTownMapStationSelection() bool {
	return request.OpaqueTailU8 == 0 &&
		request.OpaqueU32 == 0 &&
		request.OpaqueU16A != 0 &&
		request.OpaqueU16B != 0
}

// LooksLikeTownSeriaRoomPortalSelection identifies the current EXE town
// in-scene Seria-room portal shape observed live from West Coast:
// source town=40, target town=38/area=1, opaqueA=40, opaqueB=0, opaqueU32=0,
// tail=0. The target area is still resolved from runtime PVF and must be a
// Seria-room map; this only admits the client's explicit portal request across
// town ownership boundaries.
func (request currentTownSetUserAreaRequest) LooksLikeTownSeriaRoomPortalSelection(currentTown int64, targetMapPath string) bool {
	if currentTown <= 0 {
		return false
	}
	return request.LooksLikeTownCrossTownPortalSelection(currentTown) &&
		townMapPathLooksLikeSeria(targetMapPath)
}

// LooksLikeTownCrossTownPortalSelection identifies the current EXE in-scene
// cross-town portal shape observed from West Coast to HendonMyre:
// target town=39/area=0, opaqueA=current town 40, opaqueB=0, opaqueU32=0,
// tail=0. The destination is still resolved from the runtime PVF town catalog;
// this only admits a client-emitted portal body whose source-town ownership is
// explicit in the request.
func (request currentTownSetUserAreaRequest) LooksLikeTownCrossTownPortalSelection(currentTown int64) bool {
	if currentTown <= 0 {
		return false
	}
	return request.OpaqueTailU8 == 0 &&
		request.OpaqueU32 == 0 &&
		request.OpaqueU16A != 0 &&
		int64(request.OpaqueU16A) == currentTown &&
		request.OpaqueU16B == 0 &&
		int64(request.TownID) != currentTown
}

func (request currentTownSetUserAreaRequest) LooksLikeTownTransportSelection() bool {
	return request.LooksLikeTeleportArraySelection() || request.LooksLikeTownMapStationSelection()
}

// townAreaInstanceForMapPath 决定这次城镇区域提交是不是"每个角色一份"的私有房间。
//
// 返回 0 = 共享城镇区域（默认，同屏玩家照旧互相可见）；非 0 = 该角色独占的区域实例键。
// 判据直接复用工程里已有的赛丽亚房间地图识别（townMapPathLooksLikeSeria），不新造一套规则：
// 线上封包实测该房间的地图路径是 Cataclysm/Town/Elvengard/new_seria_room.map，
// 而客户端把它当私有房间——报障「其他玩家可以进入别人的赛丽亚房间」正是因为服务端
// 原先只用 (TownID, AreaID) 做区域键。
func townAreaInstanceForMapPath(mapPath string, characterID uint16) uint32 {
	if characterID == 0 || !townMapPathLooksLikeSeria(mapPath) {
		return 0
	}
	return uint32(characterID)
}

// 当前本机客户端（SHA256 80def469…）的 class1/op36 处理函数 0x1fd08d0：
// 通用分发器先读 success/error；error=7 时再读 u8 town、u8 area，
// 用客户端已有的区域任务表填充文案 32025「想去%s要完成[%s]任务」。
// 只反馈已确认的任务门槛失败；查询错误不冒充任务未完成，拒绝不改变位置或任务账。
func (s *Service) sendTownAreaQuestRequirementFailure(session *gameSession, request currentTownSetUserAreaRequest, missingQuestID int64) error {
	// 这版客户端 error=7 的地区名只按 town 查，38/5 会误导为“艾尔文防线”。
	// official-24.9 用独立系统文字反馈；旧配置保留原生 error=7 消费契约。
	if s.options.currentClientVersion == "official-24.9" {
		return s.sendTownQuestTextFailure(session, request, missingQuestID)
	}
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketSetUserArea),
		[]byte{0, 7, request.TownID, request.AreaID}, dnfproto.DefaultChannelClassification); err != nil {
		return err
	}
	s.logGameEvent(session, "game-town-set-user-area-quest-notice",
		"char_id", session.selectedCharacterID,
		"town_id", request.TownID,
		"area_id", request.AreaID,
		"message", "前置任务未完成，已发送客户端原生区域准入提示")
	return nil
}

func (s *Service) logTownSetUserAreaBlocked(session *gameSession, request currentTownSetUserAreaRequest, reason string) {
	s.logGameEvent(session, "game-town-set-user-area-blocked",
		"char_id", session.selectedCharacterID,
		"town_id", request.TownID,
		"area_id", request.AreaID,
		"position_x", request.PositionX,
		"position_y", request.PositionY,
		"direction", request.Direction,
		"opaque_u16_a", request.OpaqueU16A,
		"opaque_u16_b", request.OpaqueU16B,
		"opaque_u32", request.OpaqueU32,
		"opaque_tail_u8", request.OpaqueTailU8,
		"reason", reason)
}
