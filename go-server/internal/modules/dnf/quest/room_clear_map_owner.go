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
	if err := validateRoomClearMapInput(input); err != nil {
		return RoomClearMapCommitResult{}, err
	}

	result := RoomClearMapCommitResult{CharacterID: characterID}
	linkedIDs, dependencyIDs := catalog.RoomClearMapPlanningIDs(input)
	if len(linkedIDs) == 0 {
		result.PostCommitQuest = dnfrepo.QuestRecord{CharacterID: characterID}
		return result, nil
	}

	err := o.repositories.CharacterSettlement.WithinCharacterSettlement(
		ctx,
		characterID,
		func(tx dnfrepo.Group) error {
			if tx.Quest == nil {
				return ErrOwnerUnavailable
			}
			_, isDelta := tx.Quest.(dnfrepo.QuestDeltaWriter)
			var (
				record dnfrepo.QuestRecord
				found  bool
				err    error
			)
			if isDelta {
				record, found, err = dnfrepo.LoadQuestIDs(ctx, tx.Quest, characterID, dependencyIDs)
			} else {
				record, found, err = tx.Quest.Load(ctx, characterID)
			}
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
			if len(plan.ChangedFields) > 0 {
				if err := dnfrepo.SaveQuestChanges(ctx, tx.Quest, record, plan.Record, plan.ChangedFields...); err != nil {
					return err
				}
				result.Applied = true
			} else {
				result.Idempotent = len(plan.Completions) > 0
			}
			if len(plan.Completions) > 0 && record.PlanningPartial {
				// Linked-objective notification sends full op574 + op356 snapshots.
				// Reload the committed full record only when a completion actually
				// matched (rare), preserving exact wire fidelity without scanning
				// all quests on non-matching rooms.
				fullRecord, fullFound, loadErr := tx.Quest.Load(ctx, characterID)
				if loadErr != nil {
					return loadErr
				}
				if !fullFound || strings.TrimSpace(fullRecord.CharacterID) != characterID || fullRecord.PlanningPartial {
					return ErrQuestPersistVerify
				}
				result.PostCommitQuest = dnfrepo.CloneQuest(fullRecord)
			} else {
				result.PostCommitQuest = dnfrepo.CloneQuest(plan.Record)
			}
			return nil
		},
	)
	if err != nil {
		return RoomClearMapCommitResult{}, err
	}
	return result, nil
}
