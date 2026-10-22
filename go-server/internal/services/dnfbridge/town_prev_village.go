package dnfbridge

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	// Official 24.9's CMD name table stores PREV_VILLAGE at 0x57c00fc,
	// index 1423 from base 0x57beac0. Live test1 sends a bodyless 1423;
	// the generated older enum calls it SUMMER_2017 and silently drops it.
	official249PrevVillageCmdID         uint16 = 1423
	currentTownPrevVillageTownStat             = "prev_village_town_id"
	currentTownPrevVillageAreaStat             = "prev_village_area_id"
	currentTownPrevVillagePosXStat             = "prev_village_pos_x"
	currentTownPrevVillagePosYStat             = "prev_village_pos_y"
	currentTownPrevVillageDirectionStat        = "prev_village_direction"
)

func townMapPathLooksLikeSeria(mapPath string) bool {
	normalizedPath := strings.ToLower(strings.ReplaceAll(mapPath, "\\", "/"))
	// These current-PVF raid maps import Common/(R)Gate_Seria.map. Their
	// filenames omit "seria", but they still require per-character privacy.
	for _, room := range []string{
		"town/battleship/gate_battleship.map",
		"town/jelva_raid/gate_jelva.map",
		// Town98/0 resolves to this logical path. Both (R)/(F) resources
		// import Common/(R)/(F)Gate_Seria.map in the current PVF.
		"gcontents/180621_fiendwar/town/gate_fiend.map",
	} {
		if normalizedPath == room || normalizedPath == "map/"+room {
			return true
		}
	}
	return strings.Contains(normalizedPath, "seria")
}

func currentTownPrevVillageOriginLocked(
	session *gameSession,
	characterID uint16,
	character dnfrepo.CharacterRecord,
) (currentTownPositionSnapshot, bool) {
	if session == nil || characterID == 0 {
		return currentTownPositionSnapshot{}, false
	}
	townID, areaID, err := currentSceneTransitionLocation(character, true)
	if err != nil {
		return currentTownPositionSnapshot{}, false
	}
	if snapshot := session.townPositionSnapshot; snapshot.CharacterID == characterID &&
		snapshot.TownID == townID &&
		snapshot.AreaID == areaID &&
		snapshot.PositionValid {
		return snapshot, true
	}
	posX, okX := character.Stats["pos_x"]
	posY, okY := character.Stats["pos_y"]
	direction, okDirection := character.Stats["direction"]
	if !okX || !okY || !okDirection ||
		posX < 0 || posX > int64(^uint16(0)) ||
		posY < 0 || posY > int64(^uint16(0)) ||
		direction < 0 || direction > int64(^byte(0)) {
		return currentTownPositionSnapshot{}, false
	}
	return currentTownPositionSnapshot{
		CharacterID:   characterID,
		TownID:        townID,
		AreaID:        areaID,
		PositionX:     uint16(posX),
		PositionY:     uint16(posY),
		MovementCode:  byte(direction),
		PositionValid: true,
	}, true
}

func currentTownPrevVillageOriginFromPersistedStats(
	characterID uint16,
	character dnfrepo.CharacterRecord,
) (currentTownPositionSnapshot, bool) {
	if characterID == 0 || character.Stats == nil {
		return currentTownPositionSnapshot{}, false
	}
	townID, okTown := character.Stats[currentTownPrevVillageTownStat]
	areaID, okArea := character.Stats[currentTownPrevVillageAreaStat]
	posX, okX := character.Stats[currentTownPrevVillagePosXStat]
	posY, okY := character.Stats[currentTownPrevVillagePosYStat]
	direction, okDirection := character.Stats[currentTownPrevVillageDirectionStat]
	if !okTown || !okArea || !okX || !okY || !okDirection ||
		townID < 0 || townID > int64(^byte(0)) ||
		areaID < 0 || areaID > int64(^byte(0)) ||
		posX < 0 || posX > int64(^uint16(0)) ||
		posY < 0 || posY > int64(^uint16(0)) ||
		direction < 0 || direction > int64(^byte(0)) {
		return currentTownPositionSnapshot{}, false
	}
	return currentTownPositionSnapshot{
		CharacterID:   characterID,
		TownID:        byte(townID),
		AreaID:        byte(areaID),
		PositionX:     uint16(posX),
		PositionY:     uint16(posY),
		MovementCode:  byte(direction),
		PositionValid: true,
	}, true
}

func currentTownPrevVillageApplyOriginStats(stats map[string]int64, origin currentTownPositionSnapshot) bool {
	if stats == nil || origin.CharacterID == 0 || !origin.PositionValid {
		return false
	}
	values := map[string]int64{
		currentTownPrevVillageTownStat:      int64(origin.TownID),
		currentTownPrevVillageAreaStat:      int64(origin.AreaID),
		currentTownPrevVillagePosXStat:      int64(origin.PositionX),
		currentTownPrevVillagePosYStat:      int64(origin.PositionY),
		currentTownPrevVillageDirectionStat: int64(origin.MovementCode),
	}
	changed := false
	for key, value := range values {
		if current, ok := stats[key]; !ok || current != value {
			stats[key] = value
			changed = true
		}
	}
	return changed
}

func (s *Service) currentTownPrevVillageOriginMapPath(origin currentTownPositionSnapshot) (string, bool) {
	if s == nil || origin.CharacterID == 0 || !origin.PositionValid {
		return "", false
	}
	area, found := s.townArea(int64(origin.TownID), int64(origin.AreaID))
	if !found || area.MapPath == "" {
		return "", false
	}
	return area.MapPath, true
}

func (s *Service) currentTownPrevVillageOriginIsPersistable(origin currentTownPositionSnapshot) (string, bool) {
	mapPath, ok := s.currentTownPrevVillageOriginMapPath(origin)
	if !ok || townMapPathLooksLikeSeria(mapPath) {
		return mapPath, false
	}
	s.townCatalogMu.RLock()
	table := s.townCatalog
	s.townCatalogMu.RUnlock()
	town, found := table.Find(int64(origin.TownID))
	if !found || town.DisableSavePoint || currentTownIsEpicRoadReturnOrigin(town) {
		return mapPath, false
	}
	return mapPath, true
}

// A temporary event town is not the return origin for the Seria room. Keep
// the character's already verified normal town, rather than binding the
// temporary town as the next bottom-door destination. No absent origin is
// synthesized; the normal PVF room-door fallback remains available.
func (s *Service) currentTownSeriaReturnOriginLocked(session *gameSession, characterID uint16, character dnfrepo.CharacterRecord) (currentTownPositionSnapshot, bool) {
	if origin, ok := currentTownPrevVillageOriginLocked(session, characterID, character); ok {
		if _, persistable := s.currentTownPrevVillageOriginIsPersistable(origin); persistable {
			return origin, true
		}
	}
	if session == nil || characterID == 0 {
		return currentTownPositionSnapshot{}, false
	}
	if origin := session.townPrevVillageSnapshot; origin.CharacterID == characterID {
		if _, persistable := s.currentTownPrevVillageOriginIsPersistable(origin); persistable {
			return origin, true
		}
	}
	if origin, ok := currentTownPrevVillageOriginFromPersistedStats(characterID, character); ok {
		if _, persistable := s.currentTownPrevVillageOriginIsPersistable(origin); persistable {
			return origin, true
		}
	}
	return currentTownPositionSnapshot{}, false
}

func (s *Service) handleCurrentPrevVillage(session *gameSession, requestBody []byte) error {
	if session == nil {
		return nil
	}
	if len(requestBody) != 0 {
		s.logCurrentPrevVillageBlocked(session, currentTownPositionSnapshot{}, "current_exe_op1425_body_boundary_mismatch")
		return nil
	}
	characterID := session.selectedCharacterID
	if characterID == 0 {
		s.logCurrentPrevVillageBlocked(session, currentTownPositionSnapshot{}, "selected_character_missing")
		return nil
	}
	session.dungeon.mu.Lock()
	hasDungeonRuntime := session.dungeon.runtime != nil
	session.dungeon.mu.Unlock()
	if hasDungeonRuntime {
		s.logCurrentPrevVillageBlocked(session, currentTownPositionSnapshot{}, "active_dungeon_runtime_rejects_prev_village")
		return nil
	}
	session.townMu.Lock()
	readyCharacterID := session.townSceneReadyCharacterID
	origin := session.townPrevVillageSnapshot
	entrySnapshot := session.townSeriaRoomEntrySnapshot
	session.townMu.Unlock()
	if readyCharacterID != characterID {
		s.logCurrentPrevVillageBlocked(session, origin, "town_scene_player_state_not_finalized")
		return nil
	}

	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil {
		s.logCurrentPrevVillageBlocked(session, origin, "character_repository_unavailable")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterKey := strconv.FormatUint(uint64(characterID), 10)
	character, found, err := repositories.Character.Load(ctx, characterKey)
	if err != nil {
		return err
	}
	if !found || character.Stats == nil {
		s.logCurrentPrevVillageBlocked(session, origin, "selected_character_location_missing")
		return nil
	}
	if origin.CharacterID != characterID || !origin.PositionValid || s.currentTownEpicRoadReturnOrigin(origin) {
		if persistedOrigin, ok := currentTownPrevVillageOriginFromPersistedStats(characterID, character); ok && !s.currentTownEpicRoadReturnOrigin(persistedOrigin) {
			origin = persistedOrigin
			session.townMu.Lock()
			session.townPrevVillageSnapshot = origin
			session.townMu.Unlock()
			s.logTownPrevVillageOriginBound(session, origin, "current_exe_op1425_restored_from_persisted_prev_village")
		}
	}
	// 原点缺失不再立刻失败关闭：只要落点能从运行时 PVF 解析出来（来源城镇的门，或退一步
	// 房间所在城镇的外门口），就有权威落点；两者都没有时才按旧口径失败关闭。
	originMissing := origin.CharacterID != characterID || !origin.PositionValid || s.currentTownEpicRoadReturnOrigin(origin)
	currentTownID, currentAreaID, err := currentSceneTransitionLocation(character, true)
	if err != nil {
		s.logCurrentPrevVillageBlocked(session, origin, err.Error())
		return nil
	}
	currentArea, found := s.townArea(int64(currentTownID), int64(currentAreaID))
	if !found || !townMapPathLooksLikeSeria(currentArea.MapPath) {
		s.logCurrentPrevVillageBlocked(session, origin, "current_area_is_not_seria_room")
		return nil
	}
	// 落点选择（三档，判据与逐字段 PVF 证据见 town_seria_room_door.go）：
	//
	//	默认档 entry_town：回到**进来时所在的那个城镇/区域**。坐标 = 该来源城镇 PVF 里
	//	  "通往这间房"的触发点矩形中心；来源城镇没有这条触发点时退回玩家进门时站的那一点。
	//	  现场（香港 packet_log.txt:403 来源=西海岸 40/0，:492 却答了艾尔文防线 38/0）：
	//	  上一轮口径只认"房间所挂城镇"，把来源城镇丢掉了，所以从西海岸进的房间出来落到艾尔文防线。
	//	room_town_door 档：房间所在城镇的固定外门口（上一轮口径）。登录直接落在房间时
	//	  没有"进门来源"，这时仍然只能用这一档——它同时避免了落到副本门口（38/2）。
	//	prev_village 档：一次都不解析 PVF 门口，逐字节恢复更旧行为。
	landing := origin
	landingSource := "prev_village_origin"
	doorResolveReason := ""
	entryResolveReason := ""
	var door currentSeriaRoomDoor
	doorFound := false
	// landingAuthoritative 表示 landing 已由运行时 PVF（或来源城镇的真实位置）推导出来，
	// 不是"缺失的原点"；下面那条 originMissing 失败关闭只对纯原点回退生效。
	landingAuthoritative := false
	entryUsed := false
	switch currentTownSeriaExitDoorMode() {
	case currentTownSeriaExitModePrevVillage:
		// 回退档：不解析门口。
	case currentTownSeriaExitModeRoomTownDoor:
		door, doorFound, doorResolveReason = s.currentSeriaRoomOuterDoorLocation(
			int64(currentTownID), int64(currentAreaID))
		if doorFound {
			landing = currentTownSeriaRoomDoorLanding(characterID, door, origin.MovementCode)
			landingSource = "seria_room_outer_door"
			landingAuthoritative = true
		}
	default:
		entryOrigin, entryOK, entryReason := s.currentTownSeriaRoomEntryOriginFrom(characterID, entrySnapshot)
		entryResolveReason = entryReason
		if entryOK {
			door, doorFound, doorResolveReason = s.currentSeriaRoomReturnDoorLocation(
				int64(currentTownID),
				int64(currentAreaID),
				int64(entryOrigin.TownID),
				int64(entryOrigin.AreaID),
			)
			if doorFound {
				landing = currentTownSeriaRoomDoorLanding(characterID, door, entryOrigin.MovementCode)
				landingSource = "seria_room_entry_town_door"
			} else {
				// 来源城镇的 PVF 里没有"通往这间房"的触发点：退回玩家进门时站的那一点
				//（他在该城镇里的真实位置）。绝不落到房间所挂的那个城镇。
				landing = entryOrigin
				landingSource = "seria_room_entry_town_position"
			}
			entryUsed = true
			landingAuthoritative = true
		} else {
			// 没有真正的"进门来源"（例如登录直接落在房间）：沿用上一轮口径。
			door, doorFound, doorResolveReason = s.currentSeriaRoomOuterDoorLocation(
				int64(currentTownID), int64(currentAreaID))
			if doorFound {
				landing = currentTownSeriaRoomDoorLanding(characterID, door, origin.MovementCode)
				landingSource = "seria_room_outer_door"
				landingAuthoritative = true
			}
		}
	}
	origin = landing
	if !landingAuthoritative && originMissing {
		s.logCurrentPrevVillageBlocked(session, origin, "prev_village_origin_missing")
		return nil
	}
	targetArea, found := s.townArea(int64(origin.TownID), int64(origin.AreaID))
	if !found {
		s.logCurrentPrevVillageBlocked(session, origin, "prev_village_target_area_missing_from_runtime_pvf")
		return nil
	}
	if _, scopeErr := s.currentTownAreaInstance(ctx, session, character, targetArea.MapPath); scopeErr != nil {
		if !errors.Is(scopeErr, dnfguild.ErrNotMember) {
			return scopeErr
		}
		// Membership may have changed after login/entering Seria. Resolve its
		// real outer door before any location write or scene packet is sent.
		door, doorFound, doorResolveReason = s.currentSeriaRoomOuterDoorLocation(int64(currentTownID), int64(currentAreaID))
		if !doorFound {
			return scopeErr
		}
		origin = currentTownSeriaRoomDoorLanding(characterID, door, origin.MovementCode)
		targetArea, found = s.townArea(int64(origin.TownID), int64(origin.AreaID))
		if !found || townMapPathLooksLikeGuildHall(targetArea.MapPath) {
			return scopeErr
		}
		landingSource = "seria_room_outer_door_after_guild_membership_loss"
	}
	areaState, ok := character.Stats["area_state"]
	if !ok || areaState < 0 || areaState > int64(^byte(0)) {
		s.logCurrentPrevVillageBlocked(session, origin, "selected_character_area_state_missing_or_invalid")
		return nil
	}
	direction := origin.MovementCode

	previous := dnfrepo.CloneCharacter(character)
	character = dnfrepo.CloneCharacter(character)
	character.Stats["town_id"] = int64(origin.TownID)
	character.Stats["area_id"] = int64(origin.AreaID)
	character.Stats["pos_x"] = int64(origin.PositionX)
	character.Stats["pos_y"] = int64(origin.PositionY)
	character.Stats["direction"] = int64(direction)
	character.UpdatedAt = time.Now().UTC()
	if err := dnfrepo.SaveCharacterFields(ctx, repositories.Character, character, dnfrepo.CharacterFieldStats); err != nil {
		return err
	}

	row := currentSceneTransitionRow{
		ObjectOrResourceKey: currentSceneActorObjectKey(characterID),
		Value1:              origin.PositionX,
		Value2:              origin.PositionY,
		Value3:              byte(direction),
		Value4:              byte(areaState),
	}
	response, err := buildCurrentSceneTransitionBody(origin.TownID, origin.AreaID, []currentSceneTransitionRow{row})
	if err != nil {
		return err
	}
	rollbackLocation := func(writeErr error) error {
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), createWriteTimeout)
		rollbackErr := dnfrepo.SaveCharacterFields(rollbackCtx, repositories.Character, previous, dnfrepo.CharacterFieldStats)
		rollbackCancel()
		if rollbackErr != nil {
			s.logGameEvent(session, "game-town-prev-village-rollback-failed",
				"char_id", characterID,
				"town_id", origin.TownID,
				"area_id", origin.AreaID,
				"write_error", writeErr,
				"rollback_error", rollbackErr)
		}
		return writeErr
	}
	s.logGameEvent(session, "game-town-prev-village-op24-send",
		"char_id", characterID,
		"request_msg_id", uint16(1425),
		"response_msg_id", currentSceneTransitionMsgID,
		"town_id", origin.TownID,
		"area_id", origin.AreaID,
		"position_x", origin.PositionX,
		"position_y", origin.PositionY,
		"direction", int64(direction),
		"area_state", areaState,
		"map_path", targetArea.MapPath,
		"body_len", len(response),
		// landing_source 是这一条回答的落点真源：
		//   seria_room_entry_town_door   = 进来时所在城镇里"通往本房间"的 PVF 触发点中心（默认档）；
		//   seria_room_entry_town_position = 来源城镇里没有该触发点，退回进门时站的那一点；
		//   seria_room_outer_door        = 房间所在城镇的固定外门口（无进门来源时的上一轮口径）；
		//   prev_village_origin          = 更旧口径（登录/进城时绑定的原点）。
		// door_trigger_* 是那条门在 PVF 里的原始矩形，door_trigger_section 是它的声明形式
		// （town_movable_area 六元组 / town_movable_area_with_animation 带开门动画的传送门）。
		"landing_source", landingSource,
		"seria_exit_mode", currentTownSeriaExitDoorModeName(),
		"seria_door_resolve_reason", doorResolveReason,
		"seria_entry_resolve_reason", entryResolveReason,
		"entry_used", entryUsed,
		"entry_town_id", entrySnapshot.TownID,
		"entry_area_id", entrySnapshot.AreaID,
		"entry_position_x", entrySnapshot.PositionX,
		"entry_position_y", entrySnapshot.PositionY,
		"entry_position_valid", entrySnapshot.PositionValid,
		"door_town_id", door.TownID,
		"door_area_id", door.AreaID,
		"door_map_path", door.MapPath,
		"door_trigger_x", door.TriggerX,
		"door_trigger_y", door.TriggerY,
		"door_trigger_width", door.TriggerW,
		"door_trigger_height", door.TriggerH,
		"door_trigger_section", door.TriggerSection,
		"door_resolved", doorFound,
		"body_source", "current_exe_sub_1D901D0_typed_from_legacy_op1425_prev_village")
	if err := s.sendGameUpperRawClass(session, currentSceneTransitionMsgID, response, 0); err != nil {
		return rollbackLocation(err)
	}

	session.townMu.Lock()
	setCurrentTownPositionSceneLocked(session, characterID, origin.TownID, origin.AreaID)
	session.townPrevVillageSnapshot = currentTownPositionSnapshot{}
	// 出房间即消费掉"进门来源"：下一次进房必须重新绑定，避免陈旧来源被后来的 op1425 复用
	//（例如出房后又去了别的城镇，再被直接送到另一间赛丽亚房间时）。
	session.townSeriaRoomEntrySnapshot = currentTownPositionSnapshot{}
	session.initialTownRouteCharacterID = 0
	session.initialTownRouteStage = currentInitialTownRouteIdle
	session.initialTownLocationNotificationsSent = false
	session.initialTownQuestSnapshotsSent = false
	session.currentFinishLoadingStateSent = false
	session.currentFinishLoadingCompletionSent = false
	session.postFinishLoadingPlayerStateSent = false
	session.initialTownLegacySceneReadyAccepted = false
	session.initialTownAdventureOverheadRefreshSent = false
	session.initialTownCombatPowerAffixesSent = false
	session.enterSelectDungeonSent = false
	session.enterSelectDungeonAckSent = false
	session.enterSelectDungeonContextSent = false
	session.townSceneReadyCharacterID = characterID
	session.townMu.Unlock()
	// 从私有赛丽亚房间回到城镇：按刚回答给客户端的落点重新发布存在感
	// （厂商参考实现同一条口径，见 publishTownPresenceAfterSeriaRoomReturn 的注释）。
	s.publishTownPresenceAfterSeriaRoomReturn(
		session,
		origin.TownID,
		origin.AreaID,
		origin.PositionX,
		origin.PositionY,
		byte(direction),
		byte(areaState),
	)
	s.logGameEvent(session, "game-town-prev-village-committed",
		"char_id", characterID,
		"town_id", origin.TownID,
		"area_id", origin.AreaID,
		"position_x", origin.PositionX,
		"position_y", origin.PositionY,
		"map_path", targetArea.MapPath,
		"landing_source", landingSource,
		"finish_loading_state_rearmed", true)
	return nil
}

// currentTownSeriaRoomDoorLanding 把一条 PVF 门口投影成落点快照。
func currentTownSeriaRoomDoorLanding(
	characterID uint16,
	door currentSeriaRoomDoor,
	direction byte,
) currentTownPositionSnapshot {
	if direction == 0 {
		direction = newCharacterInitialDirection
	}
	return currentTownPositionSnapshot{
		CharacterID:   characterID,
		TownID:        door.TownID,
		AreaID:        door.AreaID,
		PositionX:     door.PositionX,
		PositionY:     door.PositionY,
		MovementCode:  direction,
		PositionValid: true,
	}
}

// currentTownSeriaRoomEntryOriginFrom 校验"这一次是从哪个 (城镇, 区域, 坐标) 走进这间房的"。
//
// 它与 townPrevVillageSnapshot 是两件事：后者在**登录直接落在房间**时来自"持久化上一位置"
// （initial_town_login_owner.go 的 character_list_login_existing_persisted_prev_village），
// 那可能是副本门口，并不是真正的进门来源；只有 op36 进房那条路径（town_move.go，
// 日志里的 source=current_exe_op36_enter_seria_room）绑定的来源才是"我从哪个城镇走门进来的"。
// 因此出口落点只认这一份快照，缺了就明确回落上一轮口径，绝不拿"持久化上一位置"顶上。
func (s *Service) currentTownSeriaRoomEntryOriginFrom(
	characterID uint16,
	entry currentTownPositionSnapshot,
) (currentTownPositionSnapshot, bool, string) {
	if characterID == 0 {
		return currentTownPositionSnapshot{}, false, "selected_character_missing"
	}
	if entry.CharacterID != characterID || !entry.PositionValid {
		return currentTownPositionSnapshot{}, false, "no_seria_room_entry_origin_for_this_character"
	}
	mapPath, ok := s.currentTownPrevVillageOriginMapPath(entry)
	if !ok {
		return currentTownPositionSnapshot{}, false, "seria_room_entry_origin_area_missing_from_runtime_pvf"
	}
	if townMapPathLooksLikeSeria(mapPath) {
		return currentTownPositionSnapshot{}, false, "seria_room_entry_origin_is_itself_a_seria_room"
	}
	if s.currentTownEpicRoadReturnOrigin(entry) {
		return currentTownPositionSnapshot{}, false, "seria_room_entry_origin_is_epic_road_excursion"
	}
	// 朝向兜底：进门来源没有朝向时用城镇房间的既有初值，避免把 direction=0 写进 op24 与落库。
	if entry.MovementCode == 0 {
		entry.MovementCode = newCharacterInitialDirection
	}
	return entry, true, ""
}

func (s *Service) logCurrentPrevVillageBlocked(session *gameSession, origin currentTownPositionSnapshot, reason string) {
	s.logGameEvent(session, "game-town-prev-village-blocked",
		"char_id", session.selectedCharacterID,
		"origin_character_id", origin.CharacterID,
		"origin_town_id", origin.TownID,
		"origin_area_id", origin.AreaID,
		"origin_position_x", origin.PositionX,
		"origin_position_y", origin.PositionY,
		"origin_position_valid", origin.PositionValid,
		"reason", reason)
}

func (s *Service) logTownPrevVillageOriginBound(session *gameSession, origin currentTownPositionSnapshot, source string) {
	s.logGameEvent(session, "game-town-prev-village-origin-bound",
		"char_id", session.selectedCharacterID,
		"origin_town_id", origin.TownID,
		"origin_area_id", origin.AreaID,
		"origin_position_x", origin.PositionX,
		"origin_position_y", origin.PositionY,
		"origin_position_valid", origin.PositionValid,
		"source", source)
}

func (s *Service) logTownPrevVillageOriginBindFailed(session *gameSession, characterID uint16, reason string) {
	s.logGameEvent(session, "game-town-prev-village-origin-bind-failed",
		"char_id", characterID,
		"reason", reason)
}
