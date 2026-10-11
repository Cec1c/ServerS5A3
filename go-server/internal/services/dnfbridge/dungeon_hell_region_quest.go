package dnfbridge

import (
	"context"
	"fmt"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"strconv"
)

// Called after authoritative wave retirement while session.dungeon.mu is held.
// This closes an abyss quest objective, never the dungeon settlement barrier.
func (s *Service) persistCurrentHellRegionQuestCompletionLocked(session *gameSession, runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene) error {
	if session == nil || runtime == nil || session.dungeon.runtime != runtime || !currentDungeonQuestHellPartyCleared(runtime) || !currentHellPartySceneIsHellRoom(runtime, scene) || runtime.hellQuestCompletionSnapshotSent {
		return nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil || repositories.Quest == nil {
		return errDungeonClearMapCompletionUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	if _, found, err := repositories.Quest.Load(ctx, characterID); err != nil {
		return err
	} else if !found {
		runtime.hellQuestCompletionPersisted = true
		runtime.hellQuestCompletionSnapshotSent = true
		return nil
	}
	table, _, err := s.dungeonWorldMap()
	if err != nil {
		return err
	}
	area, found := table.FindAreaByDungeon(runtime.Dungeon.ID)
	if !found {
		return nil
	}
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		return err
	}
	owner, err := dnfquest.NewOwner(repositories)
	if err != nil {
		return err
	}
	if runtime.hellQuestCompletionAt.IsZero() {
		runtime.hellQuestCompletionAt = s.gameplayNow().UTC()
	}
	if runtime.hellQuestCompletionKey == "" {
		runtime.hellQuestCompletionKey = fmt.Sprintf("abyss-clear:char:%d:dungeon:%d:map:%d:seed:%d:at:%d", session.selectedCharacterID, runtime.Dungeon.ID, scene.Map.Map.ID, runtime.Seed, runtime.hellQuestCompletionAt.UnixNano())
	}
	result, err := owner.ApplyClearMapCompletion(ctx, catalog, characterID, dnfquest.ClearMapCompletionInput{
		DungeonID: runtime.Dungeon.ID, MapID: scene.Map.Map.ID, WorldMapAreaID: area.ID, HellPartyCleared: true, HellPartyOnly: true,
		CompletionKey: runtime.hellQuestCompletionKey, CompletedAt: runtime.hellQuestCompletionAt,
	})
	if err != nil {
		return err
	}
	runtime.hellQuestCompletionPersisted = true
	if len(result.Completions) == 0 {
		runtime.hellQuestCompletionSnapshotSent = true
		return nil
	}
	record, found, err := repositories.Quest.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if !found {
		return dnfquest.ErrQuestPersistVerify
	}
	ids := make([]int64, 0, len(result.Completions))
	for _, completion := range result.Completions {
		state, exists := currentDungeonClearMapQuestState(record, completion.QuestID)
		if !exists || state.ProgressValue != 0 {
			return dnfquest.ErrQuestPersistVerify
		}
		ids = append(ids, completion.QuestID)
	}
	if err := s.sendGameUpperRawClass(session, currentActiveQuestSnapshotMsgID, buildCurrentActiveQuestSnapshotBody(record, true), 0); err != nil {
		return err
	}
	runtime.hellQuestCompletionSnapshotSent = true
	s.logGameEvent(session, "game-dungeon-hell-region-quest-persisted", "char_id", session.selectedCharacterID, "dungeon_id", runtime.Dungeon.ID, "map_id", scene.Map.Map.ID, "worldmap_area_id", area.ID, "quest_ids", ids, "completion_key", runtime.hellQuestCompletionKey, "reward_granted", false, "dungeon_settlement_changed", false)
	return nil
}
