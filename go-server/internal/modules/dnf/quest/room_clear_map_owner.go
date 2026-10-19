package quest

import (
	"context"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type RoomClearMapCommitResult struct {
	CharacterID     string
	Completions     []ClearMapCompletion
	ParentProgress  []RoomClearMapParentProgress
	ChangedFields   []dnfrepo.QuestField
	PostCommitQuest dnfrepo.QuestRecord
	Applied         bool
	Idempotent      bool
}

// ApplyRoomClearMapCompletion commits the room event and its parent progress
// update in one CharacterSettlement transaction. It grants no assets,
// experience, dungeon completion, or reward receipt.
func (o *Owner) ApplyRoomClearMapCompletion(
	ctx context.Context,
	catalog *Catalog,
	characterID string,
	input RoomClearMapInput,
) (RoomClearMapCommitResult, error) {
	if o == nil || o.repositories.CharacterSettlement == nil || catalog == nil {
		return RoomClearMapCommitResult{}, ErrOwnerUnavailable
	}
	characterID = strings.TrimSpace(characterID)
	if characterID == "" {
		return RoomClearMapCommitResult{}, ErrCharacterRequired
	}

	result := RoomClearMapCommitResult{CharacterID: characterID}
	err := o.repositories.CharacterSettlement.WithinCharacterSettlement(
		ctx,
		characterID,
		func(tx dnfrepo.Group) error {
			if tx.Quest == nil {
				return ErrOwnerUnavailable
			}
			record, found, err := tx.Quest.Load(ctx, characterID)
			if err != nil {
				return err
			}
			if !found {
				result.PostCommitQuest = dnfrepo.QuestRecord{CharacterID: characterID}
				return nil
			}
			record = dnfrepo.CloneQuest(record)
			if strings.TrimSpace(record.CharacterID) == "" {
				record.CharacterID = characterID
			}
			if strings.TrimSpace(record.CharacterID) != characterID {
				return ErrQuestPersistVerify
			}

			plan, err := catalog.PlanRoomClearMapCompletion(record, input)
			if err != nil {
				return err
			}
			result.Completions = append([]ClearMapCompletion(nil), plan.Completions...)
			result.ParentProgress = append([]RoomClearMapParentProgress(nil), plan.ParentProgress...)
			result.ChangedFields = append([]dnfrepo.QuestField(nil), plan.ChangedFields...)
			result.PostCommitQuest = dnfrepo.CloneQuest(plan.Record)
			if len(plan.ChangedFields) == 0 {
				result.Idempotent = len(plan.Completions) > 0
				return nil
			}
			if err := dnfrepo.SaveQuestFields(ctx, tx.Quest, plan.Record, plan.ChangedFields...); err != nil {
				return err
			}
			result.Applied = true
			return nil
		},
	)
	if err != nil {
		return RoomClearMapCommitResult{}, err
	}
	return result, nil
}
