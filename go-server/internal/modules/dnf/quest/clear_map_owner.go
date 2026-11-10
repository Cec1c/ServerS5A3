package quest

import (
	"context"
	"strconv"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

type ClearMapPersistResult struct {
	CharacterID   string
	Completions   []ClearMapCompletion
	ChangedFields []dnfrepo.QuestField
	Idempotent    bool
}

// ApplyClearMapCompletion persists and verifies the quest-only Phase-A marker.
// It grants no reward. The authoritative op117 owner may continue to op115/op31
// only after this method returns success.
func (o *Owner) ApplyClearMapCompletion(
	ctx context.Context,
	catalog *Catalog,
	characterID string,
	input ClearMapCompletionInput,
) (ClearMapPersistResult, error) {
	if o == nil || o.characters == nil || o.quests == nil {
		return ClearMapPersistResult{}, ErrOwnerUnavailable
	}
	characterID = strings.TrimSpace(characterID)
	if characterID == "" {
		return ClearMapPersistResult{}, ErrCharacterRequired
	}
	if input.WeddingPartnerID != "" {
		unit, ok := o.repositories.CharacterTrade.(dnfrepo.WeddingQuestUnitOfWork)
		if !ok {
			return ClearMapPersistResult{}, ErrOwnerUnavailable
		}
		ids := []string{characterID, input.WeddingPartnerID}
		if _, err := dnfrepo.WeddingPairScope(ids); err != nil {
			return ClearMapPersistResult{}, err
		}
		ids, _ = dnfrepo.CharacterInventoryGroupIDs(ids)
		var result ClearMapPersistResult
		err := unit.WithinWeddingQuests(ctx, ids, func(characters dnfrepo.CharacterRepository, quests dnfrepo.QuestRepository) error {
			if characters == nil || quests == nil {
				return ErrOwnerUnavailable
			}
			// Clear and cancellation acquire the same pair in the same order.
			// All relationship/quest reads and the eventual verified save below
			// stay inside this one transaction, with no base-repository fallback.
			for _, id := range ids {
				if record, found, err := characters.Load(ctx, id); err != nil {
					return err
				} else if found && record.CharacterID != id {
					return ErrCharacterNotFound
				}
			}
			scoped := *o
			scoped.characters, scoped.quests = characters, quests
			scoped.repositories.Character, scoped.repositories.Quest = characters, quests
			scoped.repositories.CharacterTrade = nil
			var err error
			result, err = scoped.applyClearMapCompletion(ctx, catalog, characterID, input, true)
			return err
		})
		if err != nil {
			return ClearMapPersistResult{}, err
		}
		return result, nil
	}
	return o.applyClearMapCompletion(ctx, catalog, characterID, input, false)
}

func (o *Owner) applyClearMapCompletion(
	ctx context.Context,
	catalog *Catalog,
	characterID string,
	input ClearMapCompletionInput,
	pairLocked bool,
) (ClearMapPersistResult, error) {
	if _, ok, err := dnfrepo.LoadCharacterIdentity(ctx, o.characters, characterID); err != nil {
		return ClearMapPersistResult{}, err
	} else if !ok {
		return ClearMapPersistResult{}, ErrCharacterNotFound
	}
	if input.WeddingPartnerID != "" {
		character, found, err := o.characters.Load(ctx, characterID)
		if err != nil {
			return ClearMapPersistResult{}, err
		}
		state, stateErr := wedding.Read(character)
		if !found || stateErr != nil || strconv.FormatInt(state.PartnerID, 10) != input.WeddingPartnerID {
			// A stale/wrong partner cannot authorize a read outside the locked
			// pair. Preserve the ordinary no-match clear behavior in that case.
			input.WeddingPartnerID = ""
		} else {
			eligibility, err := WithWeddingEligibility(ctx, o.characters, character, CharacterEligibility{})
			if err != nil {
				return ClearMapPersistResult{}, err
			}
			if eligibility.weddingPartner != input.WeddingPartnerID {
				input.WeddingPartnerID = ""
			}
		}
	}
	record, ok, err := o.quests.Load(ctx, characterID)
	if err != nil {
		return ClearMapPersistResult{}, err
	}
	if !ok {
		record = dnfrepo.QuestRecord{CharacterID: characterID}
	} else {
		record = dnfrepo.CloneQuest(record)
	}
	if strings.TrimSpace(record.CharacterID) == "" {
		record.CharacterID = characterID
	}
	plan, err := catalog.PlanClearMapCompletion(record, input)
	if err != nil {
		return ClearMapPersistResult{}, err
	}
	if err := plan.ValidateCharacter(characterID); err != nil {
		return ClearMapPersistResult{}, err
	}
	result := ClearMapPersistResult{
		CharacterID:   characterID,
		Completions:   append([]ClearMapCompletion(nil), plan.Completions...),
		ChangedFields: append([]dnfrepo.QuestField(nil), plan.ChangedFields...),
		Idempotent:    len(plan.Completions) > 0 && len(plan.ChangedFields) == 0,
	}
	if len(plan.ChangedFields) == 0 {
		return result, nil
	}
	if _, delta := o.quests.(dnfrepo.QuestDeltaWriter); !delta && !pairLocked &&
		(clearMapRecordHasWeddingPreparation(record) || clearMapRecordHasWeddingPreparation(plan.Record)) {
		// An opaque/legacy store cannot safely replace a stale whole group
		// containing preparation tasks after concurrent cancellation. A real
		// delta writer preserves unchanged tasks; the shared pair transaction
		// makes the explicit full-record fallback safe for wedding clears.
		return ClearMapPersistResult{}, ErrOwnerUnavailable
	}
	if err := dnfrepo.SaveQuestChanges(ctx, o.quests, record, plan.Record, plan.ChangedFields...); err != nil {
		return ClearMapPersistResult{}, err
	}
	persisted, ok, err := o.quests.Load(ctx, characterID)
	if err != nil {
		return ClearMapPersistResult{}, err
	}
	if !ok {
		return ClearMapPersistResult{}, ErrQuestPersistVerify
	}
	for _, completion := range result.Completions {
		state, known := questStateFor(persisted, completion.QuestID)
		if !known || (!isActiveQuestStatus(state.Status) && !isCompletedQuestStatus(state.Status)) || state.ProgressValue != completion.RemainingTrigger {
			return ClearMapPersistResult{}, ErrQuestPersistVerify
		}
		if !completion.Idempotent && (state.Extra["completion_key"] != input.CompletionKey || completion.RemainingTrigger == 0 && state.Extra["reward_state"] != clearMapRewardPending) {
			if !isCompletedQuestStatus(state.Status) || state.Extra["reward_state"] != finishRewardGranted ||
				state.Extra["completion_key"] != input.CompletionKey {
				return ClearMapPersistResult{}, ErrQuestPersistVerify
			}
		}
	}
	return result, nil
}

func clearMapRecordHasWeddingPreparation(record dnfrepo.QuestRecord) bool {
	for _, id := range []int64{20737, 20738} {
		if _, found := record.States[id]; found {
			return true
		}
		if _, found := record.Progress[id]; found {
			return true
		}
	}
	return false
}
