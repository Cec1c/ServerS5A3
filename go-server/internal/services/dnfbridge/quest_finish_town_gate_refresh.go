package dnfbridge

import (
	"sort"
	"strconv"

	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 复用受限的op22/op23/op24同图投影，同步当前地点和名册，保留真实坐标。
// 该投影不代表完整地图重载；空574清空入口完成集合的问题由其后的真实356恢复。
// 不执行op36过图事务，不写位置或放宽任务门槛。
func (s *Service) refreshCurrentTownQuestGateScene(session *gameSession, result dnfquest.FinishCommitResult) error {
	if session == nil || !result.AtomicCommitted || result.Replayed || result.QuestID <= 0 ||
		result.CharacterID != strconv.Itoa(int(session.selectedCharacterID)) ||
		result.PostCommitQuest.CharacterID != result.CharacterID ||
		!currentTownGateQuestCompleted(result.PostCommitQuest, result.QuestID) {
		return nil
	}
	session.dungeon.mu.Lock()
	inDungeon := session.dungeon.runtime != nil
	session.dungeon.mu.Unlock()
	if inDungeon {
		return nil
	}
	// 同图投影的组队名册/摆摊状态尚未原生验收，暂不对这些场景发送。
	if runtimePartyStateSnapshot(session).PartyID > 0 {
		return nil
	}
	s.expertJobStoreMu.Lock()
	hasStore := s.expertJobStores[session.selectedCharacterID] != nil || s.expertJobVisitors[session] != 0
	s.expertJobStoreMu.Unlock()
	if hasStore {
		return nil
	}
	areaState, validAreaState := result.PostCommitCharacter.Stats["area_state"]
	if !validAreaState || areaState < 0 || areaState > 255 {
		return nil
	}
	session.townMu.Lock()
	snapshot := session.townPositionSnapshot
	ready := session.townSceneReadyCharacterID == session.selectedCharacterID
	session.townMu.Unlock()
	if !ready || !snapshot.PositionValid || snapshot.CharacterID != session.selectedCharacterID {
		return nil
	}
	if result.PostCommitCharacter.CharacterID != result.CharacterID ||
		result.PostCommitCharacter.Stats["town_id"] != int64(snapshot.TownID) ||
		result.PostCommitCharacter.Stats["area_id"] != int64(snapshot.AreaID) {
		return nil
	}
	s.expertJobStoreMu.Lock()
	for _, store := range s.expertJobStores {
		if store != nil && store.TownID == snapshot.TownID && store.AreaID == snapshot.AreaID &&
			store.ChannelID == currentTownPresenceChannelID(session) {
			hasStore = true
			break
		}
	}
	s.expertJobStoreMu.Unlock()
	if hasStore {
		return nil
	}
	area, found := s.townArea(int64(snapshot.TownID), int64(snapshot.AreaID))
	if !found {
		return nil
	}
	reader, available, _ := s.currentSeriaRoomDoorMapReader()
	if !available {
		return nil
	}
	parsed, found := reader(area)
	if !found || !currentTownMapExitUsesQuest(parsed, result.QuestID) {
		return nil
	}
	// 地图读取期间可能切换选角或场景；只允许刷新原来的归属和坐标。
	session.townMu.Lock()
	defer session.townMu.Unlock()
	if session.townSceneReadyCharacterID != snapshot.CharacterID ||
		session.selectedCharacterID != snapshot.CharacterID ||
		session.townPositionSnapshot != snapshot {
		return nil
	}
	self := currentSceneTransitionRow{
		ObjectOrResourceKey: currentSceneActorObjectKey(snapshot.CharacterID),
		Value1:              snapshot.PositionX, Value2: snapshot.PositionY,
		Value3: snapshot.MovementCode,
		Value4: byte(areaState),
	}
	rows := make([]currentSceneTransitionRow, 0, 1)
	instance, battle, admitted, release := s.lockCurrentAntonWaitingAreaScope(session, snapshot.TownID, snapshot.AreaID)
	if !admitted || instance != 0 || battle != "" {
		release()
		return nil
	}
	release()
	if s.onlinePlayers != nil {
		players := s.onlinePlayers.GetAreaScopePlayers(snapshot.TownID, snapshot.AreaID,
			currentTownPresenceChannelID(session), townAreaInstanceForMapPath(area.MapPath, snapshot.CharacterID), battle)
		sort.Slice(players, func(i, j int) bool { return players[i].CharacterID < players[j].CharacterID })
		for _, player := range players {
			if player.CharacterID == 0 || player.CharacterID == snapshot.CharacterID {
				continue
			}
			rows = append(rows, currentSceneTransitionRow{
				ObjectOrResourceKey: currentSceneActorObjectKey(player.CharacterID),
				Value1:              player.PositionX, Value2: player.PositionY,
				Value3: player.Direction, Value4: player.AreaState,
			})
		}
	}
	rows = append(rows, self) // 当前原生读者把最后一行绑定为自己和镜头。
	body, err := buildCurrentSceneTransitionBody(snapshot.TownID, snapshot.AreaID, rows)
	if err != nil {
		return err
	}
	s.logGameEvent(session, "game-upper-finish-quest-town-gate-refresh",
		"说明", "当前地图出口引用了刚完成的任务，保持坐标原地刷新出口场景",
		"quest_id", result.QuestID, "char_id", snapshot.CharacterID,
		"town_id", snapshot.TownID, "area_id", snapshot.AreaID,
		"position_x", snapshot.PositionX, "position_y", snapshot.PositionY,
		"map_path", area.MapPath, "row_count", len(rows))
	if err := s.sendCurrentTownLocationNotifications(session, snapshot.CharacterID,
		snapshot.TownID, snapshot.AreaID, self, "quest_finish_current_map_gate_refresh"); err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(session, currentSceneTransitionMsgID, body, 0); err != nil {
		return err
	}
	// 与原生城镇过图相同，允许当前地图重新加载后的 finish-loading 收尾。
	// 不重置选角引导、地理归属和任务 ACK 的幂等状态。
	session.currentFinishLoadingStateSent = false
	session.currentFinishLoadingCompletionSent = false
	session.postFinishLoadingPlayerStateSent = false
	return nil
}

func currentTownGateQuestCompleted(record dnfrepo.QuestRecord, questID int64) bool {
	state, found := record.States[questID]
	if !found {
		state, found = record.Progress[questID]
	}
	if !found {
		return false
	}
	switch normalizeDungeonQuestStatus(state.Status) {
	case "complete", "completed", "cleared", "finished", "done":
		return true
	}
	return false
}

func currentTownMapExitUsesQuest(parsed worldmap.Map, questID int64) bool {
	if questID <= 0 {
		return false
	}
	for _, section := range parsed.SourceSections {
		if currentSeriaRoomDoorSectionKey(section.Name) != "quest condition" ||
			(!currentSeriaRoomDoorScopeContains(section.Scope, "town movable area") &&
				!currentSeriaRoomDoorScopeContains(section.Scope, "town movable area with animation")) {
			continue
		}
		for _, value := range currentSeriaRoomDoorInts(section) {
			if value == questID {
				return true
			}
		}
	}
	return false
}
