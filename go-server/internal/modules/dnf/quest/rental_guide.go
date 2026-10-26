package quest

import (
	"context"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	// RentalGuideQuestID is the accepted guide quest advanced by a successful
	// Lucky Star equipment rental.
	RentalGuideQuestID   int64 = 8581
	rentalGuideQuestPath       = "n_quest/common/chn_system_10_starpoint_guide.qst"
)

// AdvanceRentalGuideQuest advances only the exact accepted 8581 definition.
// Other quest types, already completed/zero rows and unrelated states remain
// unchanged. The caller owns the surrounding asset transaction.
func AdvanceRentalGuideQuest(
	ctx context.Context,
	quests dnfrepo.QuestRepository,
	characterID string,
	catalog *Catalog,
	updatedAt time.Time,
) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	characterID = strings.TrimSpace(characterID)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if quests == nil || characterID == "" || catalog == nil {
		return false, nil
	}
	definition, found := catalog.Find(RentalGuideQuestID)
	if !found || !isRentalGuideDefinition(definition) {
		return false, nil
	}
	record, found, err := quests.Load(ctx, characterID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	record = dnfrepo.CloneQuest(record)
	state, field, found := mutableQuestState(&record, RentalGuideQuestID)
	if !found || !isActiveQuestStatus(state.Status) || state.ProgressValue != 1 {
		return false, nil
	}
	now := updatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	state.ProgressValue = 0
	state.UpdatedAt = now
	switch field {
	case dnfrepo.QuestFieldStates:
		record.States[RentalGuideQuestID] = state
	case dnfrepo.QuestFieldProgress:
		record.Progress[RentalGuideQuestID] = state
	default:
		return false, nil
	}
	record.CharacterID = characterID
	record.UpdatedAt = now
	if err := dnfrepo.SaveQuestFields(ctx, quests, record, field); err != nil {
		return false, err
	}
	return true, nil
}

func isRentalGuideDefinition(definition Definition) bool {
	return definition.ID == RentalGuideQuestID &&
		strings.TrimSpace(definition.Path) == rentalGuideQuestPath &&
		normalizeQuestTag(definition.Type) == "custom quest" &&
		definition.SubType == 0 &&
		len(definition.IntData) == 3 &&
		definition.IntData[0] == 1 &&
		definition.IntData[1] == 0 &&
		definition.IntData[2] == 1
}

func isRentalGuideQuestState(state dnfrepo.QuestState) bool {
	if state.Extra == nil {
		return false
	}
	return strings.TrimSpace(state.Extra["pvf_path"]) == rentalGuideQuestPath &&
		normalizeQuestTag(state.Extra["quest_type"]) == "custom quest"
}
