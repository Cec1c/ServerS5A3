package dnfbridge

import (
	"context"
	"encoding/binary"
	"math"
	"strconv"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
)

const currentSetQuestTriggerRequestBodySize = 6

// currentSetQuestTriggerReplayKey is deliberately scoped to one game TCP
// session. It avoids repeatedly loading and publishing an active quest whose
// persisted trigger has already reached the exact requested state.
type currentSetQuestTriggerReplayKey struct {
	characterID uint16
	questID     uint16
	triggerType byte
	answer      byte
	hasAnswer   bool
}

func newCurrentSetQuestTriggerReplayKey(characterID uint16, request dnfquest.SetTriggerRequest) currentSetQuestTriggerReplayKey {
	return currentSetQuestTriggerReplayKey{
		characterID: characterID,
		questID:     request.QuestID,
		triggerType: request.TriggerType,
		answer:      request.Answer,
		hasAnswer:   request.HasAnswer,
	}
}

func (session *gameSession) currentSetQuestTriggerReplaySuppressed(key currentSetQuestTriggerReplayKey) bool {
	if session == nil {
		return false
	}
	session.questReplay.triggerMu.Lock()
	defer session.questReplay.triggerMu.Unlock()
	_, ok := session.questReplay.triggerNoop[key]
	return ok
}

func (session *gameSession) suppressCurrentSetQuestTriggerReplay(key currentSetQuestTriggerReplayKey) {
	if session == nil {
		return
	}
	session.questReplay.triggerMu.Lock()
	defer session.questReplay.triggerMu.Unlock()
	if session.questReplay.triggerNoop == nil {
		session.questReplay.triggerNoop = make(map[currentSetQuestTriggerReplayKey]struct{}, 1)
	}
	session.questReplay.triggerNoop[key] = struct{}{}
}

// clearCurrentSetQuestTriggerReplayForQuest advances the session-local replay
// gate when op31 has durably activated a new generation of the same quest ID.
// Without this invalidation a repeatable quest can inherit the previous
// generation's terminal op33 key and silently lose its first valid trigger.
func (session *gameSession) clearCurrentSetQuestTriggerReplayForQuest(characterID, questID uint16) {
	if session == nil {
		return
	}
	session.questReplay.triggerMu.Lock()
	defer session.questReplay.triggerMu.Unlock()
	for key := range session.questReplay.triggerNoop {
		if key.characterID == characterID && key.questID == questID {
			delete(session.questReplay.triggerNoop, key)
		}
	}
	for key := range session.questReplay.linkedTriggerValue {
		if key.characterID == characterID && key.questID == questID {
			delete(session.questReplay.linkedTriggerValue, key)
		}
	}
}

func (session *gameSession) linkedTriggerAlreadySent(key currentSetQuestTriggerReplayKey, progress int64) bool {
	session.questReplay.triggerMu.Lock()
	defer session.questReplay.triggerMu.Unlock()
	previous, sent := session.questReplay.linkedTriggerValue[key]
	return sent && previous == progress
}

func (session *gameSession) markLinkedTriggerSent(key currentSetQuestTriggerReplayKey, progress int64) {
	session.questReplay.triggerMu.Lock()
	defer session.questReplay.triggerMu.Unlock()
	if session.questReplay.linkedTriggerValue == nil {
		session.questReplay.linkedTriggerValue = make(map[currentSetQuestTriggerReplayKey]int64)
	}
	session.questReplay.linkedTriggerValue[key] = progress
}

func (s *Service) sendCurrentSetQuestTriggerSuccess(
	session *gameSession,
	request dnfquest.SetTriggerRequest,
	result dnfquest.PlanResult,
	bodySource string,
) error {
	var payload packetWriter
	payload.writeUint16(request.QuestID)
	payload.writeUint32(uint32(result.ProgressValue))
	s.logGameEvent(session, "game-upper-set-quest-trigger-success-send",
		"quest_id", request.QuestID,
		"trigger_type", request.TriggerType,
		"increment", request.IsIncrement,
		"trigger_value", result.ProgressValue,
		"status", result.Status,
		"msg_id", uint16(dnfenum.CmdPacketSetQuestTrigger),
		"classification", dnfproto.DefaultChannelClassification,
		"plain_body_len", 7,
		"body_source", bodySource)
	return s.sendGameUpperSuccess(session, uint16(dnfenum.CmdPacketSetQuestTrigger), payload.bytes())
}

// handleCurrentSetQuestTrigger closes the current NPC/dialogue quest step.
// The client owns which active quest and trigger channel an NPC interaction
// selects; the server owns the durable decrement, exact ACK, and subsequent
// active-quest snapshot. A state-changing request is acknowledged once. Its
// same-session no-op replays are intentionally silent: ACKing each replay and
// resending op574 creates a client/server feedback loop.
func (s *Service) handleCurrentSetQuestTrigger(session *gameSession, body []byte) error {
	if session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	if len(body) != currentSetQuestTriggerRequestBodySize ||
		binary.LittleEndian.Uint16(body[:2]) != uint16(dnfenum.CmdPacketSetQuestTrigger) {
		s.logGameEvent(session, "game-upper-set-quest-trigger-blocked",
			"body_len", len(body),
			"reason", "current_exe_op33_requires_exact_echo_quest_trigger_and_increment")
		return nil
	}
	request, err := dnfquest.DecodeSetTriggerRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-upper-set-quest-trigger-blocked",
			"body_len", len(body),
			"reason", "request_decode_failed",
			"error", err)
		return nil
	}
	replayKey := newCurrentSetQuestTriggerReplayKey(session.selectedCharacterID, request)
	if session.currentSetQuestTriggerReplaySuppressed(replayKey) {
		return nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil || repositories.Quest == nil {
		s.logGameEvent(session, "game-upper-set-quest-trigger-blocked",
			"quest_id", request.QuestID,
			"reason", "character_or_quest_repository_unavailable")
		return nil
	}
	owner, err := dnfquest.NewOwner(repositories)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	if handled, err := s.echoCurrentRaidQuestTrigger(ctx, session, owner, request); handled || err != nil {
		return err
	}
	catalog, catalogErr := s.loadQuestCatalog(ctx)
	if catalogErr != nil {
		s.logGameEvent(session, "game-upper-set-quest-trigger-catalog-unavailable",
			"quest_id", request.QuestID,
			"reason", "ordinary_trigger_fallback",
			"error", catalogErr)
	}
	if catalog != nil {
		if _, question := catalog.QuestionOptionCount(int64(request.QuestID)); question {
			command := dnfquest.NewTriggerCommand(alignedcmd.Request{
				AccountID:           s.accountIDForSession(session),
				SelectedCharacterID: session.selectedCharacterID,
			}, request)
			result, err := owner.ApplyQuestionAnswer(ctx, catalog, command)
			if err != nil {
				s.logGameEvent(session, "game-upper-question-answer-blocked",
					"quest_id", request.QuestID,
					"trigger_type", request.TriggerType,
					"answer", request.Answer,
					"has_answer", request.HasAnswer,
					"reason", "question_answer_rejected",
					"error", err)
				return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketSetQuestTrigger), 22)
			}
			if err := s.sendCurrentSetQuestTriggerSuccess(
				session,
				request,
				result,
				"current_exe_op33_question_answer_persisted",
			); err != nil {
				return err
			}
			if result.StateChanged {
				if err := s.sendCurrentActiveQuestSnapshotForSession(session, "current_exe_op33_question_answer_after_ack"); err != nil {
					return err
				}
			}
			session.suppressCurrentSetQuestTriggerReplay(replayKey)
			s.logGameEvent(session, "game-upper-question-answer-committed",
				"quest_id", request.QuestID,
				"answer", request.Answer,
				"state_changed", result.StateChanged,
				"progress", result.ProgressValue)
			return nil
		}
	}
	if request.TriggerType == 0 && !request.IsIncrement {
		if catalogErr != nil {
			s.logGameEvent(session, "game-upper-set-quest-trigger-linked-progress-skipped",
				"quest_id", request.QuestID,
				"reason", "quest_catalog_unavailable",
				"error", catalogErr)
			characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
			record, found, evidenceErr := repositories.Quest.Load(ctx, characterID)
			if evidenceErr != nil {
				s.logGameEvent(session, "game-upper-set-quest-trigger-linked-progress-blocked",
					"quest_id", request.QuestID,
					"reason", "linked_objective_evidence_load_failed",
					"error", evidenceErr)
				return nil
			}
			if found && currentSetTriggerHasLinkedObjectiveEvidence(record, request.QuestID) {
				return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketSetQuestTrigger), 22)
			}
			s.logGameEvent(session, "game-upper-set-quest-trigger-linked-progress-skipped",
				"quest_id", request.QuestID,
				"reason", "no_persisted_linked_objective_evidence",
				"fallback", "ordinary_set_trigger")
		} else {
			linked, linkedErr := s.applyCurrentTownLinkedProgress(ctx, session, owner, catalog, request)
			if linkedErr != nil {
				s.logGameEvent(session, "game-upper-set-quest-trigger-linked-progress-blocked",
					"quest_id", request.QuestID,
					"reason", "atomic_linked_progress_failed",
					"error", linkedErr)
				return nil
			}
			if linked.Handled {
				progress := linked.ParentProgress
				if linked.ParentQuestID != int64(request.QuestID) {
					progress = 0
				}
				// Reconcile first: a later child transition must remain eligible.
				// Publish an unchanged corrective value once per session, avoiding
				// the op33/op574 feedback loop on subsequent identical callbacks.
				if !linked.Applied && session.linkedTriggerAlreadySent(replayKey, progress) {
					return nil
				}
				result := dnfquest.PlanResult{
					QuestID: int64(request.QuestID), Known: true, StateChanged: linked.Applied,
					Status: "active", TriggerType: request.TriggerType, ProgressValue: progress,
				}
				if err := s.sendCurrentSetQuestTriggerSuccess(
					session,
					request,
					result,
					"current_exe_op33_parent_after_atomic_linked_objective_archive",
				); err != nil {
					return err
				}
				if err := s.sendCurrentLinkedObjectiveSnapshots(
					session,
					linked.PostCommitQuest,
					"current_exe_op33_linked_objective_after_atomic_archive",
				); err != nil {
					return err
				}
				s.logGameEvent(session, "game-upper-set-quest-trigger-linked-progress-committed",
					"quest_id", request.QuestID,
					"parent_quest_id", linked.ParentQuestID,
					"parent_progress", linked.ParentProgress,
					"completed_quest_ids", linked.CompletedQuestIDs,
					"applied", linked.Applied,
					"packet_order", "op33_op574_op356")
				session.markLinkedTriggerSent(replayKey, progress)
				if progress == 0 {
					session.suppressCurrentSetQuestTriggerReplay(replayKey)
				}
				return nil
			}
		}
	}
	result, err := owner.ApplySetTrigger(ctx, dnfquest.NewTriggerCommand(alignedcmd.Request{
		AccountID:           s.accountIDForSession(session),
		SelectedCharacterID: session.selectedCharacterID,
	}, request))
	if err != nil {
		s.logGameEvent(session, "game-upper-set-quest-trigger-blocked",
			"quest_id", request.QuestID,
			"trigger_type", request.TriggerType,
			"increment", request.IsIncrement,
			"reason", "quest_trigger_persist_failed",
			"error", err)
		return nil
	}
	if !result.Known || result.ProgressValue < 0 || result.ProgressValue > math.MaxUint32 {
		s.logGameEvent(session, "game-upper-set-quest-trigger-blocked",
			"quest_id", request.QuestID,
			"known", result.Known,
			"status", result.Status,
			"progress", result.ProgressValue,
			"reason", "quest_trigger_result_not_acknowledgeable")
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketSetQuestTrigger), 22)
	}
	if !result.StateChanged {
		// Clear-map persistence can reach terminal zero before the current EXE
		// emits its first op33. Acknowledge that first request once so the
		// dungeon result handler can cross its transition boundary, without
		// publishing another op574 snapshot.
		if result.ProgressValue == 0 && result.Status == "active" {
			if err := s.sendCurrentSetQuestTriggerSuccess(
				session,
				request,
				result,
				"current_exe_op33_success_after_pre_persisted_active_zero",
			); err != nil {
				return err
			}
			session.suppressCurrentSetQuestTriggerReplay(replayKey)
			return nil
		}
		if result.ProgressValue == 0 {
			session.suppressCurrentSetQuestTriggerReplay(replayKey)
		}
		s.logGameEvent(session, "game-upper-set-quest-trigger-noop-suppressed",
			"quest_id", request.QuestID,
			"trigger_type", request.TriggerType,
			"increment", request.IsIncrement,
			"trigger_value", result.ProgressValue,
			"status", result.Status,
			"reason", "persisted_trigger_state_unchanged_no_ack_or_op574")
		return nil
	}
	if err := s.sendCurrentSetQuestTriggerSuccess(
		session,
		request,
		result,
		"current_exe_op33_success_quest_and_trigger_after_durable_state_change",
	); err != nil {
		return err
	}
	if err := s.sendCurrentActiveQuestSnapshotForSession(session, "current_exe_op33_after_durable_state_change_ack"); err != nil {
		return err
	}
	// A terminal zero trigger is an idempotent endpoint. Cache only that state:
	// nonzero trigger progress can legitimately need another player action with
	// the same request body.
	if result.ProgressValue == 0 {
		session.suppressCurrentSetQuestTriggerReplay(replayKey)
	}
	return nil
}
