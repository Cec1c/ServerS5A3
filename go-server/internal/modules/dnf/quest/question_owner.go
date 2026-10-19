package quest

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	questionAnswerExtraKey        = "question_answer_id"
	questionOptionCountExtraKey   = "question_option_count"
	questionAnswerMetadataKey     = "question_answer_v1"
	questionAnswerMetadataVersion = "1"
)

var (
	ErrQuestionAnswerRequired = errors.New("question answer is required")
	ErrQuestionAnswerInvalid  = errors.New("question answer is invalid")
	ErrQuestionAnswerConflict = errors.New("question answer conflicts with persisted choice")
)

// QuestionOptionCount returns the parsed selectable-answer count and whether
// the quest definition is a question quest.
func (c *Catalog) QuestionOptionCount(questID int64) (int, bool) {
	if c == nil {
		return 0, false
	}
	definition, ok := c.Find(questID)
	if !ok || normalizeQuestTag(definition.Type) != "question" {
		return 0, false
	}
	return definition.QuestionOptionCount, true
}

// ApplyQuestionAnswer persists the raw client answer for an active question
// quest. The first valid answer is durable. An identical retry is idempotent;
// a conflicting answer never replaces the confirmed choice.
func (o *Owner) ApplyQuestionAnswer(ctx context.Context, catalog *Catalog, cmd Command) (PlanResult, error) {
	if o == nil || o.repositories.CharacterSettlement == nil {
		return PlanResult{}, dnfrepo.ErrCharacterSettlementTransactionUnavailable
	}
	if o.characters == nil || o.quests == nil {
		return PlanResult{}, ErrOwnerUnavailable
	}
	if cmd.SelectedCharacterID == 0 {
		return PlanResult{}, ErrCharacterRequired
	}
	if cmd.QuestID == 0 {
		return PlanResult{}, ErrQuestIDRequired
	}
	if catalog == nil {
		return PlanResult{}, ErrQuestDefinitionMissing
	}
	optionCount, question := catalog.QuestionOptionCount(int64(cmd.QuestID))
	if !question {
		return PlanResult{}, ErrQuestDefinitionMissing
	}
	candidate := PlanResult{
		Operation:   cmd.Operation,
		QuestID:     int64(cmd.QuestID),
		Known:       true,
		Status:      "active",
		TriggerType: 0,
	}
	if cmd.TriggerType != 0 {
		return candidate, ErrQuestionAnswerInvalid
	}
	if !cmd.HasAnswer {
		return candidate, ErrQuestionAnswerRequired
	}
	if optionCount <= 0 || int(cmd.Answer) >= optionCount {
		return candidate, ErrQuestionAnswerInvalid
	}

	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	accountID := strings.TrimSpace(cmd.AccountID)
	err := o.repositories.CharacterSettlement.WithinCharacterSettlement(ctx, characterID, func(tx dnfrepo.Group) error {
		if tx.Character == nil || tx.Quest == nil {
			return dnfrepo.ErrCharacterSettlementTransactionUnavailable
		}
		character, found, err := tx.Character.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || strings.TrimSpace(character.CharacterID) != characterID ||
			accountID == "" || strings.TrimSpace(character.AccountID) != accountID {
			return ErrCharacterNotFound
		}
		record, found, err := tx.Quest.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || strings.TrimSpace(record.CharacterID) != characterID {
			return ErrQuestNotActive
		}
		record = dnfrepo.CloneQuest(record)
		state, field, known := mutableQuestState(&record, int64(cmd.QuestID))
		if !known || !isActiveQuestStatus(state.Status) {
			return ErrQuestNotActive
		}
		if existing, ok := questionAnswerFromState(state); ok {
			if existing != cmd.Answer {
				return ErrQuestionAnswerConflict
			}
			if state.ProgressValue == 0 && state.TriggerType == 0 {
				candidate.AccountID = character.AccountID
				candidate.CharacterID = characterID
				candidate.ProgressValue = 0
				return nil
			}
		}

		state.TriggerType = 0
		state.ProgressValue = 0
		state.UpdatedAt = time.Now()
		if state.Extra == nil {
			state.Extra = make(map[string]string, 4)
		}
		state.Extra[questionAnswerExtraKey] = strconv.Itoa(int(cmd.Answer))
		state.Extra[questionOptionCountExtraKey] = strconv.Itoa(optionCount)
		state.Extra[questionAnswerMetadataKey] = questionAnswerMetadataVersion
		switch field {
		case dnfrepo.QuestFieldStates:
			record.States[int64(cmd.QuestID)] = state
		case dnfrepo.QuestFieldProgress:
			record.Progress[int64(cmd.QuestID)] = state
		default:
			return ErrQuestNotActive
		}
		record.UpdatedAt = state.UpdatedAt
		if err := dnfrepo.SaveQuestFields(ctx, tx.Quest, record, field); err != nil {
			return err
		}
		candidate.AccountID = character.AccountID
		candidate.CharacterID = characterID
		candidate.StateChanged = true
		candidate.ProgressValue = 0
		return nil
	})
	if err != nil {
		return PlanResult{}, err
	}
	return candidate, nil
}

func questionAnswerPrerequisiteSatisfied(catalog *Catalog, record dnfrepo.QuestRecord, values []int64) bool {
	if len(values) != 2 || values[0] <= 0 || values[1] < 0 || values[1] > 255 {
		return false
	}
	state, known := questStateFor(record, values[0])
	if !known || normalizeStatus(state.Status) != "completed" {
		return false
	}
	if catalog == nil {
		return false
	}
	definition, known := catalog.Find(values[0])
	if !known || normalizeQuestTag(definition.Type) != "question" {
		return false
	}
	answer, ok := questionAnswerFromState(state)
	return ok && int64(answer) == values[1] && int(answer) < definition.QuestionOptionCount
}

func questionAnswerFromState(state dnfrepo.QuestState) (byte, bool) {
	raw, exists := state.Extra[questionAnswerExtraKey]
	if !exists {
		return 0, false
	}
	value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 8)
	return byte(value), err == nil
}
