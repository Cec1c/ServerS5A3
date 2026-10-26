package dnfbridge

import (
	"fmt"
	"strconv"

	quest "longheng.io/server/internal/modules/dnf/quest"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// A zero-active op574 can clear the native completed cache. Always rebuild
// op356 afterwards, then expose the newly acceptable quests. No domain writes.
func (s *Service) sendCommittedStorySweepSnapshots(session *gameSession, c repo.CharacterRecord, q repo.QuestRecord, ids []int64, source string) error {
	if session == nil || c.CharacterID != strconv.Itoa(int(session.selectedCharacterID)) || q.CharacterID != c.CharacterID {
		return fmt.Errorf("story sweep snapshot ownership changed")
	}
	if err := s.sendCurrentActiveQuestSnapshotFromCommittedQuest(session, q, source+"_active"); err != nil {
		return err
	}
	if len(ids) > 0 {
		result := quest.FinishCommitResult{AtomicCommitted: true, CharacterID: c.CharacterID, QuestID: ids[0], ResumedStoryQuestIDs: ids, PostCommitCharacter: c, PostCommitQuest: q}
		if err := s.refreshCurrentTownQuestGateScene(session, result); err != nil {
			return err
		}
	}
	if err := s.sendCurrentClearQuestListFromCommittedQuest(session, q, source+"_completed"); err != nil {
		return err
	}
	return s.sendCurrentAcceptableQuestListOnlyForSession(session, source+"_acceptable")
}
