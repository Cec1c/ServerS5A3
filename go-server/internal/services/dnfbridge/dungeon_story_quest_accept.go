package dnfbridge

import (
	"context"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
)

// acceptCurrentStorySuccessorQuest starts the PVF quest-chain successor before a
// story-chain dungeon entry, using exactly the op31 accept preflight
// (Catalog.DecideAccept over the persisted record plus the PVF level, job,
// grow-type, prerequisite and collision rules). Nothing is invented: a successor
// that is already active, already completed, or not yet eligible leaves the
// persisted quest record untouched and reports false, so the entry keeps its
// previous target.
//
// Why the entry needs it: the settlement dialog's 开始下个任务 button is a
// story-chain entry, and the current client waits for the server-driven entry
// instead of sending op16. In the real Script.pvf a quest dungeon carries its
// quest as a maze quest connection (dungeon 6 猛毒雷鸣废墟 maze 1 =
// [0 3147 -1], dungeon 3 幽暗密林 maze 2 = [0 3146 -1]), and dungeon maze
// selection only picks that maze while the quest is active; otherwise it falls
// back to the dungeon's non-quest maze. Entering the successor dungeon without
// starting the successor quest therefore lands in the non-quest maze, where the
// chain cannot progress (DOC-118: 传说中的白化变异哥布林 -> 猛毒雷鸣废墟).
func (s *Service) acceptCurrentStorySuccessorQuest(session *gameSession, questID int64) bool {
	if s == nil || session == nil || session.selectedCharacterID == 0 || questID <= 0 || questID > int64(^uint16(0)) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil || catalog == nil {
		return false
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil || repositories.Quest == nil {
		return false
	}
	_, _, character, found := s.selectedCharacterForEnter(ctx, session)
	if !found {
		return false
	}
	job, validJob := characterJobByte(character)
	if !validJob || character.Level <= 0 {
		return false
	}
	owner, err := dnfquest.NewOwner(repositories)
	if err != nil {
		return false
	}
	eligibility, err := catalog.WithEpicRoadEvent350Eligibility(dnfquest.CharacterEligibility{
		Level:    character.Level,
		Job:      int(job),
		GrowType: characterGrowTypeInt(character),
	}, s.currentEpicRoadActivityAvailable())
	if err != nil {
		return false
	}
	result, err := owner.ApplyAccept(ctx, catalog, eligibility, dnfquest.NewQuestIDCommand(alignedcmd.Request{
		AccountID:           s.accountIDForSession(session),
		SelectedCharacterID: session.selectedCharacterID,
	}, "accept_quest", dnfquest.QuestIDRequest{QuestID: uint16(questID)}))
	if err != nil {
		s.logGameEvent(session, "game-dungeon-card-story-successor-accept-skipped",
			"char_id", session.selectedCharacterID,
			"quest_id", questID,
			"job", job,
			"level", character.Level,
			"reason", "successor_not_acceptable_under_pvf_db_preflight",
			"quest_mutation", "none",
			"error", err)
		return false
	}
	s.logGameEvent(session, "game-dungeon-card-story-successor-accepted",
		"char_id", session.selectedCharacterID,
		"quest_id", questID,
		"idempotent", result.Idempotent,
		"trigger", result.InitTrigger,
		"source", "current_exe_op72_story_next_quest_before_entry")
	return true
}
