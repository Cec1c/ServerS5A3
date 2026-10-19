package dnfbridge

import (
	"context"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func currentSetTriggerHasLinkedObjectiveEvidence(record dnfrepo.QuestRecord, questID uint16) bool {
	id := int64(questID)
	state, found := record.States[id]
	if !found {
		state, found = record.Progress[id]
	}
	if found {
		switch normalizeCurrentQuestType(state.Extra["quest_type"]) {
		case "quest clear", "clear quest":
			return true
		}
		if strings.TrimSpace(state.Extra["main_quest_id"]) != "" {
			return true
		}
	}
	for _, states := range []map[int64]dnfrepo.QuestState{record.States, record.Progress} {
		for _, child := range states {
			if strings.TrimSpace(child.Extra["main_quest_id"]) == strconv.FormatUint(uint64(questID), 10) {
				return true
			}
		}
	}
	return false
}

func normalizeCurrentQuestType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "[")
	value = strings.TrimSuffix(value, "]")
	value = strings.ReplaceAll(value, "_", " ")
	return strings.Join(strings.Fields(value), " ")
}

func (s *Service) sendCurrentLinkedObjectiveSnapshots(
	session *gameSession,
	record dnfrepo.QuestRecord,
	source string,
) error {
	if err := s.sendCurrentActiveQuestSnapshotFromCommittedQuest(session, record, source+"_active"); err != nil {
		return err
	}
	return s.sendCurrentClearQuestListFromCommittedQuest(session, record, source+"_completed")
}

func (s *Service) applyCurrentTownLinkedProgress(
	ctx context.Context,
	session *gameSession,
	owner *dnfquest.Owner,
	catalog *dnfquest.Catalog,
	request dnfquest.SetTriggerRequest,
) (dnfquest.LinkedObjectiveCommitResult, error) {
	if request.TriggerType != 0 || request.IsIncrement {
		return dnfquest.LinkedObjectiveCommitResult{}, nil
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	return owner.ApplyTownLinkedProgress(ctx, catalog, characterID, int64(request.QuestID), time.Now().UTC())
}

func (s *Service) handleCurrentLinkedObjectiveFinish(
	ctx context.Context,
	session *gameSession,
	request dnfquest.FinishQuestRequest,
	repositories dnfrepo.Group,
	catalog *dnfquest.Catalog,
) (bool, error) {
	if catalog == nil || !catalog.IsLinkedObjective(int64(request.QuestID)) {
		return false, nil
	}
	replayKey := newCurrentFinishQuestReplayKey(session.selectedCharacterID, request)
	if session.currentFinishQuestAnswered(replayKey) {
		s.logGameEvent(session, "game-upper-linked-objective-finish-replay-suppressed",
			"quest_id", request.QuestID,
			"reason", "same_session_callback_already_closed")
		return true, nil
	}
	owner, err := dnfquest.NewOwner(repositories)
	if err != nil {
		return true, err
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	result, err := owner.ApplyLinkedObjectiveFinish(
		ctx,
		catalog,
		characterID,
		int64(request.QuestID),
		time.Now().UTC(),
	)
	if err != nil {
		return true, err
	}
	s.logGameEvent(session, "game-upper-linked-objective-finish-callback-close",
		"quest_id", request.QuestID,
		"detail", 22,
		"applied", result.Applied,
		"parent_quest_id", result.ParentQuestID,
		"parent_progress", result.ParentProgress,
		"completed_quest_ids", result.CompletedQuestIDs,
		"reward_mutation", "none")
	if err := s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketFinishQuest), 22); err != nil {
		return true, err
	}
	if !result.Applied {
		return true, nil
	}
	session.markCurrentFinishQuestAnswered(replayKey)
	return true, s.sendCurrentLinkedObjectiveSnapshots(
		session,
		result.PostCommitQuest,
		"current_exe_op34_linked_objective_after_atomic_archive",
	)
}
