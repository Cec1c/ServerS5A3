package dnfbridge

import (
	"context"
	"strconv"
	"strings"
)

// sendCurrentDungeonStoryQuestAfterFreeCardLocked re-projects completion only
// after the card response and its durable reward. In the current EXE (SHA256
// bc613e368e8fd49c50c35163f2ed8a28564fc23342701a5a6c22b839f3bf6b7a),
// class0/op574 at 0x2014370 reads u32 count + (u16 quest, u32 trigger), and its
// trigger-zero branch calls 0x1caaf40 for the native completion UI. The earlier
// snapshot precedes op115/op31 and can be replaced by the settlement UI.
//
// Only a story quest belonging to this run's persisted clear-map completion
// (or its PVF parent) authorizes the refresh. The repository remains read-only:
// the native op34 finish/op31 accept requests still own rewards and successors.
// The caller holds session.dungeon.mu.
func (s *Service) sendCurrentDungeonStoryQuestAfterFreeCardLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
) error {
	if session == nil || runtime == nil || session.dungeon.runtime != runtime ||
		runtime.storyQuestAfterFreeCardSnapshotSent ||
		!runtime.settlementCardSideRewardCommitted[dungeonCardSideFree] ||
		!runtime.clearMapCompletionPhaseAPersisted ||
		len(runtime.clearMapCompletionQuestIDs) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		return err
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Quest == nil {
		return errDungeonClearMapCompletionUnavailable
	}
	record, found, err := repositories.Quest.Load(ctx, strconv.Itoa(int(session.selectedCharacterID)))
	if err != nil {
		return err
	}
	if !found {
		return errDungeonClearMapCompletionUnavailable
	}
	related := make(map[int64]bool)
	for _, id := range runtime.clearMapCompletionQuestIDs {
		definition, exists := catalog.Find(id)
		if !exists {
			continue
		}
		if strings.Trim(definition.Grade, "[] ") == "epic" {
			related[id] = true
		} else if definition.MainQuestID > 0 {
			parent, exists := catalog.Find(definition.MainQuestID)
			if exists && strings.Trim(parent.Grade, "[] ") == "epic" {
				related[parent.ID] = true
			}
		}
	}
	var completedStoryIDs []int64
	for _, row := range currentSelectAckQuestRows(record, true) {
		if row.triggerValue == 0 && related[int64(row.questID)] {
			completedStoryIDs = append(completedStoryIDs, int64(row.questID))
		}
	}
	if len(completedStoryIDs) == 0 {
		// A manual finish before the card must not reopen the old quest page.
		runtime.storyQuestAfterFreeCardSnapshotSent = true
		return nil
	}
	body := buildCurrentActiveQuestSnapshotBody(record, true)
	if err := s.sendGameUpperRawClass(session, currentActiveQuestSnapshotMsgID, body, 0); err != nil {
		return err
	}
	runtime.storyQuestAfterFreeCardSnapshotSent = true
	s.logGameEvent(session, "game-dungeon-story-quest-after-card-snapshot-send",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"quest_ids", completedStoryIDs,
		"msg_id", currentActiveQuestSnapshotMsgID,
		"classification", 0,
		"body_len", len(body),
		"ordering", "free_op71_and_card_reward_committed_then_active_op574",
		"quest_mutation", "none")
	return nil
}
