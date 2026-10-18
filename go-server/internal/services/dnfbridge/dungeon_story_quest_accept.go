package dnfbridge

import (
	"context"
	"errors"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
)

var errStorySuccessorAcceptUnavailable = errors.New("dnf story successor quest accept is unavailable")

// storySuccessorAcceptOutcome 区分四种结局，调用者据此决定是否继续下一任务入场：
//   - storySuccessorAcceptFailed：真实失败（仓储/上下文/持久化校验）。记录可能未变
//     也可能已写入，但一律不退休旧 runtime、不进后继副本、不扣入场资产，并允许重试。
//   - storySuccessorAcceptIneligible：PVF/角色状态本来就不可接取，记录未变；
//     再次挑战保留当前副本和难度。
//   - storySuccessorAcceptIdempotent：后继任务已经是进行中。
//   - storySuccessorAcceptAccepted：本次真正接取。
type storySuccessorAcceptOutcome int

const (
	storySuccessorAcceptFailed storySuccessorAcceptOutcome = iota
	storySuccessorAcceptIneligible
	storySuccessorAcceptIdempotent
	storySuccessorAcceptAccepted
)

// acceptCurrentStorySuccessorQuest starts the PVF quest-chain successor before a
// story-chain dungeon entry, using exactly the op31 accept preflight
// (Catalog.DecideAccept over the persisted record plus the PVF level, job,
// grow-type, prerequisite and collision rules). Nothing is invented: a successor
// that is already active reports an idempotent acceptance. Already completed
// or ineligible successors leave the record untouched and preserve the current
// dungeon as the retry target.
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
func (s *Service) acceptCurrentStorySuccessorQuest(session *gameSession, questID int64) (storySuccessorAcceptOutcome, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 || questID <= 0 || questID > int64(^uint16(0)) {
		return storySuccessorAcceptFailed, errStorySuccessorAcceptUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil || catalog == nil {
		return storySuccessorAcceptFailed, errStorySuccessorAcceptUnavailable
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil || repositories.Quest == nil {
		return storySuccessorAcceptFailed, errStorySuccessorAcceptUnavailable
	}
	_, _, character, found := s.selectedCharacterForEnter(ctx, session)
	if !found {
		return storySuccessorAcceptFailed, errStorySuccessorAcceptUnavailable
	}
	job, validJob := characterJobByte(character)
	if !validJob || character.Level <= 0 {
		return storySuccessorAcceptFailed, errStorySuccessorAcceptUnavailable
	}
	owner, err := dnfquest.NewOwner(repositories)
	if err != nil {
		return storySuccessorAcceptFailed, err
	}
	eligibility, err := catalog.WithEpicRoadEvent350Eligibility(dnfquest.CharacterEligibility{
		Level:    character.Level,
		Job:      int(job),
		GrowType: characterGrowTypeInt(character),
	}, s.currentEpicRoadActivityAvailable())
	if err != nil {
		return storySuccessorAcceptFailed, err
	}
	result, err := owner.ApplyAccept(ctx, catalog, eligibility, dnfquest.NewQuestIDCommand(alignedcmd.Request{
		AccountID:           s.accountIDForSession(session),
		SelectedCharacterID: session.selectedCharacterID,
	}, "accept_quest", dnfquest.QuestIDRequest{QuestID: uint16(questID)}))
	if err != nil {
		if isStorySuccessorPreflightRefusal(err) {
			s.logGameEvent(session, "game-dungeon-card-story-successor-accept-skipped",
				"char_id", session.selectedCharacterID,
				"quest_id", questID,
				"job", job,
				"level", character.Level,
				"reason", "successor_not_acceptable_under_pvf_db_preflight",
				"quest_mutation", "none",
				"error", err)
			return storySuccessorAcceptIneligible, err
		}
		// 真实存储/上下文/持久化校验失败：记录可能未变也可能已写入，调用者必须停止
		// 本次下一任务入场并允许重试（重试时接取是幂等的）。
		s.logGameEvent(session, "game-dungeon-card-story-successor-accept-skipped",
			"char_id", session.selectedCharacterID,
			"quest_id", questID,
			"job", job,
			"level", character.Level,
			"reason", "successor_accept_storage_or_context_failure_retry_armed",
			"quest_mutation", "unknown_possible_partial_write",
			"error", err)
		return storySuccessorAcceptFailed, err
	}
	outcome := storySuccessorAcceptAccepted
	if result.Idempotent {
		outcome = storySuccessorAcceptIdempotent
	}
	s.logGameEvent(session, "game-dungeon-card-story-successor-accepted",
		"char_id", session.selectedCharacterID,
		"quest_id", questID,
		"idempotent", result.Idempotent,
		"trigger", result.InitTrigger,
		"source", "current_exe_op72_story_next_quest_before_entry")
	return outcome, nil
}

// isStorySuccessorPreflightRefusal 只认 PVF/角色状态本身造成的拒绝：这类拒绝没有写入，
// 也不会因为重试而改变，因此再次挑战回到当前副本。仓储、上下文与持久化校验错误
// 都不在此列，一律按真实失败处理。
func isStorySuccessorPreflightRefusal(err error) bool {
	switch {
	case errors.Is(err, dnfquest.ErrQuestNotAcceptable),
		errors.Is(err, dnfquest.ErrQuestDefinitionMissing),
		errors.Is(err, dnfquest.ErrQuestAcceptEventItemsRequired),
		errors.Is(err, dnfquest.ErrQuestInitialTriggerUnsupported):
		return true
	default:
		return false
	}
}
