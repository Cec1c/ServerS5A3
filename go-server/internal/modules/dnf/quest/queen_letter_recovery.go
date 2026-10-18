package quest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	QueenLetterSourceQuestID   int64 = 3226
	QueenLetterParentQuestID   int64 = 3249
	QueenLetterMeetQuestID     int64 = 3609
	QueenLetterDeliveryQuestID int64 = 3610
	QueenLetterItemID          int64 = 10164786

	QueenLetterRecoveryReceiptKey = "queen_letter_recovery_receipt_v1"
	queenLetterRecoveryVersion    = 1
)

var (
	ErrQueenLetterRecoveryUnavailable        = errors.New("queen letter recovery transaction is unavailable")
	ErrQueenLetterRecoveryDefinitionMismatch = errors.New("queen letter source relation does not match the supported PVF definition")
)

// QueenLetterRecoveryInput is the exact owner scope for the one confirmed
// source relation. Allocator applies the PVF-backed item row to the
// transaction-scoped inventory clone.
type QueenLetterRecoveryInput struct {
	AccountID    string
	CharacterID  string
	RecoveredAt  time.Time
	AllocateItem FinishItemAllocator
}

// QueenLetterRecoveryResult is detached from the transaction. When Recovered
// is true, Item identifies the exact inventory row whose op14 update must be
// sent before the active-quest snapshot.
type QueenLetterRecoveryResult struct {
	Recovered           bool
	PostCommitQuest     dnfrepo.QuestRecord
	PostCommitInventory dnfrepo.InventoryRecord
	Item                FinishCommittedItem
}

type queenLetterRecoveryReceipt struct {
	Version         int    `json:"version"`
	CharacterID     string `json:"character_id"`
	SourceQuestID   int64  `json:"source_quest_id"`
	ParentQuestID   int64  `json:"parent_quest_id"`
	DeliveryQuestID int64  `json:"delivery_quest_id"`
	ItemID          int64  `json:"item_id"`
	Count           int64  `json:"count"`
	SlotKey         string `json:"slot_key"`
	CompletionKey   string `json:"completion_key"`
	Source          string `json:"source"`
}

// IsQueenLetterRecoveryCandidate is the bridge's cheap pre-catalog check. It
// accepts only the two no-reward auto-complete markers observed for 3226 and
// requires the already-active 3249/3610 pair. Inventory ownership and the
// receipt are checked again inside the settlement transaction.
func IsQueenLetterRecoveryCandidate(record dnfrepo.QuestRecord) bool {
	source, sourceKnown := questStateFor(record, QueenLetterSourceQuestID)
	parent, parentKnown := questStateFor(record, QueenLetterParentQuestID)
	delivery, deliveryKnown := questStateFor(record, QueenLetterDeliveryQuestID)
	if !sourceKnown || !parentKnown || !deliveryKnown {
		return false
	}
	if normalizeStatus(source.Status) != "completed" ||
		strings.TrimSpace(source.Extra["reward_state"]) != finishRewardGranted ||
		strings.TrimSpace(source.Extra[finishSettlementReceiptKey]) != "" ||
		strings.TrimSpace(source.Extra[QueenLetterRecoveryReceiptKey]) != "" {
		return false
	}
	switch strings.TrimSpace(source.Extra["auto_complete_reason"]) {
	case "main_story_level_clear_no_reward", "epic_below_character_level_no_reward":
	default:
		return false
	}
	return isActiveQuestStatus(parent.Status) && isActiveQuestStatus(delivery.Status)
}

// RecoverQueenLetter grants exactly one missing letter when real PVF data still
// proves 3226 -> (3249, 3609/3610) and 3610 still consumes the letter. The
// inventory mutation and the one-time receipt on 3226 commit in one
// CharacterSettlement transaction. A full inventory, allocator failure or save
// failure leaves both aggregates untouched so a later snapshot can retry.
func (o *Owner) RecoverQueenLetter(
	ctx context.Context,
	catalog *Catalog,
	input QueenLetterRecoveryInput,
) (QueenLetterRecoveryResult, error) {
	if o == nil || o.repositories.CharacterSettlement == nil || catalog == nil {
		return QueenLetterRecoveryResult{}, ErrQueenLetterRecoveryUnavailable
	}
	input.AccountID = strings.TrimSpace(input.AccountID)
	input.CharacterID = strings.TrimSpace(input.CharacterID)
	if input.AccountID == "" || input.CharacterID == "" {
		return QueenLetterRecoveryResult{}, ErrCharacterRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	recoveredAt := input.RecoveredAt.UTC()
	if recoveredAt.IsZero() {
		recoveredAt = time.Now().UTC()
	}

	var result QueenLetterRecoveryResult
	err := o.repositories.CharacterSettlement.WithinCharacterSettlement(ctx, input.CharacterID, func(tx dnfrepo.Group) error {
		if tx.Character == nil || tx.Quest == nil || tx.Inventory == nil {
			return ErrQueenLetterRecoveryUnavailable
		}
		character, found, err := tx.Character.Load(ctx, input.CharacterID)
		if err != nil {
			return err
		}
		if !found || strings.TrimSpace(character.CharacterID) != input.CharacterID ||
			strings.TrimSpace(character.AccountID) != input.AccountID {
			return fmt.Errorf("%w: character=%q account=%q", ErrCharacterNotFound, character.CharacterID, character.AccountID)
		}
		quests, found, err := tx.Quest.Load(ctx, input.CharacterID)
		if err != nil {
			return err
		}
		if !found || strings.TrimSpace(quests.CharacterID) != input.CharacterID {
			return fmt.Errorf("%w: quest owner=%q", ErrQueenLetterRecoveryUnavailable, quests.CharacterID)
		}
		quests = dnfrepo.CloneQuest(quests)
		if !IsQueenLetterRecoveryCandidate(quests) {
			result.PostCommitQuest = dnfrepo.CloneQuest(quests)
			if inventory, inventoryFound, loadErr := tx.Inventory.Load(ctx, input.CharacterID); loadErr != nil {
				return loadErr
			} else if inventoryFound && strings.TrimSpace(inventory.CharacterID) == input.CharacterID {
				result.PostCommitInventory = dnfrepo.CloneInventory(inventory)
			}
			return nil
		}
		if err := validateQueenLetterRecoveryDefinitions(catalog); err != nil {
			return err
		}
		inventory, found, err := tx.Inventory.Load(ctx, input.CharacterID)
		if err != nil {
			return err
		}
		if !found || strings.TrimSpace(inventory.CharacterID) != input.CharacterID {
			return fmt.Errorf("%w: inventory owner=%q", ErrQueenLetterRecoveryUnavailable, inventory.CharacterID)
		}
		if err := dnfrepo.BindInventoryContainerState(ctx, o.repositories.Settings, &inventory); err != nil {
			return err
		}
		inventory = dnfrepo.CloneInventory(inventory)
		if queenLetterInventoryCount(inventory) > 0 {
			result.PostCommitQuest = dnfrepo.CloneQuest(quests)
			result.PostCommitInventory = dnfrepo.CloneInventory(inventory)
			return nil
		}
		if input.AllocateItem == nil {
			return ErrFinishItemAllocatorRequired
		}

		completionKey := fmt.Sprintf("queen-letter-recovery/%s/%d", input.CharacterID, QueenLetterSourceQuestID)
		sourceName := "quest_source_recovery:3226->3249/3610"
		items, err := allocateFinishRewardItems(
			&inventory,
			[]RewardItemRule{{ItemID: QueenLetterItemID, Count: 1}},
			input.AllocateItem,
			FinishItemGrantRequest{
				QuestID: QueenLetterSourceQuestID, CompletionKey: completionKey, Source: sourceName,
			},
		)
		if err != nil {
			return err
		}
		if len(items) != 1 || items[0].ItemID != QueenLetterItemID || items[0].Delta != 1 {
			return fmt.Errorf("%w: allocator receipt=%+v", ErrQuestRewardMalformed, items)
		}
		sourceState, sourceField, known := mutableQuestState(&quests, QueenLetterSourceQuestID)
		if !known {
			return ErrQueenLetterRecoveryDefinitionMismatch
		}
		if sourceState.Extra == nil {
			sourceState.Extra = make(map[string]string, 1)
		}
		receiptJSON, err := json.Marshal(queenLetterRecoveryReceipt{
			Version:         queenLetterRecoveryVersion,
			CharacterID:     input.CharacterID,
			SourceQuestID:   QueenLetterSourceQuestID,
			ParentQuestID:   QueenLetterParentQuestID,
			DeliveryQuestID: QueenLetterDeliveryQuestID,
			ItemID:          QueenLetterItemID,
			Count:           1,
			SlotKey:         items[0].SlotKey,
			CompletionKey:   completionKey,
			Source:          sourceName,
		})
		if err != nil {
			return err
		}
		sourceState.Extra[QueenLetterRecoveryReceiptKey] = string(receiptJSON)
		sourceState.UpdatedAt = recoveredAt
		switch sourceField {
		case dnfrepo.QuestFieldStates:
			quests.States[QueenLetterSourceQuestID] = sourceState
		case dnfrepo.QuestFieldProgress:
			quests.Progress[QueenLetterSourceQuestID] = sourceState
		default:
			return ErrQueenLetterRecoveryDefinitionMismatch
		}
		quests.UpdatedAt = recoveredAt
		inventory.UpdatedAt = recoveredAt

		if err := dnfrepo.SaveInventoryFields(ctx, tx.Inventory, inventory, dnfrepo.InventoryFieldSlots); err != nil {
			return err
		}
		if err := dnfrepo.SaveQuestFields(ctx, tx.Quest, quests, sourceField); err != nil {
			return err
		}
		result.Recovered = true
		result.PostCommitQuest = dnfrepo.CloneQuest(quests)
		result.PostCommitInventory = dnfrepo.CloneInventory(inventory)
		result.Item = cloneFinishItems(items)[0]
		return nil
	})
	if err != nil {
		return QueenLetterRecoveryResult{}, err
	}
	return result, nil
}

func validateQueenLetterRecoveryDefinitions(catalog *Catalog) error {
	source, sourceKnown := catalog.Find(QueenLetterSourceQuestID)
	parent, parentKnown := catalog.Find(QueenLetterParentQuestID)
	meet, meetKnown := catalog.Find(QueenLetterMeetQuestID)
	delivery, deliveryKnown := catalog.Find(QueenLetterDeliveryQuestID)
	if !sourceKnown || !parentKnown || !meetKnown || !deliveryKnown ||
		normalizeQuestTag(source.RewardType) != "item" ||
		len(source.RewardIntData) != 2 ||
		source.RewardIntData[0] != QueenLetterItemID ||
		source.RewardIntData[1] != 1 ||
		normalizeQuestTag(parent.Type) != "quest clear" ||
		len(parent.IntData) != 2 ||
		parent.IntData[0] != QueenLetterMeetQuestID ||
		parent.IntData[1] != QueenLetterDeliveryQuestID ||
		meet.MainQuestID != QueenLetterParentQuestID ||
		delivery.MainQuestID != QueenLetterParentQuestID ||
		normalizeQuestTag(delivery.Type) != "seek n meet npc" ||
		len(delivery.IntData) < 2 ||
		delivery.IntData[0] != QueenLetterItemID ||
		delivery.IntData[1] != 1 {
		return fmt.Errorf("%w: source=%d parent=%d meet=%d delivery=%d",
			ErrQueenLetterRecoveryDefinitionMismatch,
			QueenLetterSourceQuestID, QueenLetterParentQuestID, QueenLetterMeetQuestID, QueenLetterDeliveryQuestID)
	}
	return nil
}

func queenLetterInventoryCount(inventory dnfrepo.InventoryRecord) int64 {
	var count int64
	for _, stack := range inventory.Slots {
		if stack.ItemID != QueenLetterItemID || stack.Count < 0 || stack.Count > math.MaxInt64-count {
			continue
		}
		if present := finishStackAvailableCount(stack); present > 0 {
			count += present
		}
	}
	return count
}
