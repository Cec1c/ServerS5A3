package quest

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const storySourceRecoveryVersion = 1

type storySourceRecoverySpec struct {
	SourceQuestID    int64
	ParentQuestID    int64
	MeetQuestID      int64
	DeliveryQuestID  int64
	ItemID           int64
	ItemCount        int64
	DeliveryType     string
	MeetType         string
	ReceiptKey       string
	CompletionPrefix string
	Source           string
	UnavailableErr   error
	MismatchErr      error
}

type storySourceRecoveryInput struct {
	AccountID    string
	CharacterID  string
	RecoveredAt  time.Time
	AllocateItem FinishItemAllocator
}

type storySourceRecoveryResult struct {
	Recovered           bool
	PostCommitQuest     dnfrepo.QuestRecord
	PostCommitInventory dnfrepo.InventoryRecord
	Item                FinishCommittedItem
}

type storySourceRecoveryReceipt struct {
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

func isStorySourceRecoveryCandidate(record dnfrepo.QuestRecord, spec storySourceRecoverySpec) bool {
	source, sourceKnown := questStateFor(record, spec.SourceQuestID)
	parent, parentKnown := questStateFor(record, spec.ParentQuestID)
	delivery, deliveryKnown := questStateFor(record, spec.DeliveryQuestID)
	if !sourceKnown || !parentKnown || !deliveryKnown {
		return false
	}
	if normalizeStatus(source.Status) != "completed" ||
		strings.TrimSpace(source.Extra["reward_state"]) != finishRewardGranted ||
		strings.TrimSpace(source.Extra[finishSettlementReceiptKey]) != "" ||
		strings.TrimSpace(source.Extra[spec.ReceiptKey]) != "" {
		return false
	}
	switch strings.TrimSpace(source.Extra["auto_complete_reason"]) {
	case "main_story_level_clear_no_reward", "epic_below_character_level_no_reward":
	default:
		return false
	}
	return isActiveQuestStatus(parent.Status) && isActiveQuestStatus(delivery.Status)
}

func recoverStorySource(
	o *Owner,
	ctx context.Context,
	catalog *Catalog,
	spec storySourceRecoverySpec,
	input storySourceRecoveryInput,
) (storySourceRecoveryResult, error) {
	if o == nil || o.repositories.CharacterSettlement == nil || catalog == nil {
		return storySourceRecoveryResult{}, spec.UnavailableErr
	}
	input.AccountID = strings.TrimSpace(input.AccountID)
	input.CharacterID = strings.TrimSpace(input.CharacterID)
	if input.AccountID == "" || input.CharacterID == "" {
		return storySourceRecoveryResult{}, ErrCharacterRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	recoveredAt := input.RecoveredAt.UTC()
	if recoveredAt.IsZero() {
		recoveredAt = time.Now().UTC()
	}

	var result storySourceRecoveryResult
	err := o.repositories.CharacterSettlement.WithinCharacterSettlement(ctx, input.CharacterID, func(tx dnfrepo.Group) error {
		if tx.Character == nil || tx.Quest == nil || tx.Inventory == nil {
			return spec.UnavailableErr
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
			return fmt.Errorf("%w: quest owner=%q", spec.UnavailableErr, quests.CharacterID)
		}
		quests = dnfrepo.CloneQuest(quests)
		if !isStorySourceRecoveryCandidate(quests, spec) {
			result.PostCommitQuest = dnfrepo.CloneQuest(quests)
			if inventory, inventoryFound, loadErr := tx.Inventory.Load(ctx, input.CharacterID); loadErr != nil {
				return loadErr
			} else if inventoryFound && strings.TrimSpace(inventory.CharacterID) == input.CharacterID {
				result.PostCommitInventory = dnfrepo.CloneInventory(inventory)
			}
			return nil
		}
		if err := validateStorySourceRecoveryDefinitions(catalog, spec); err != nil {
			return err
		}
		inventory, found, err := tx.Inventory.Load(ctx, input.CharacterID)
		if err != nil {
			return err
		}
		if !found || strings.TrimSpace(inventory.CharacterID) != input.CharacterID {
			return fmt.Errorf("%w: inventory owner=%q", spec.UnavailableErr, inventory.CharacterID)
		}
		if err := dnfrepo.BindInventoryContainerState(ctx, o.repositories.Settings, &inventory); err != nil {
			return err
		}
		inventory = dnfrepo.CloneInventory(inventory)
		if storySourceInventoryCount(inventory, spec.ItemID) > 0 {
			result.PostCommitQuest = dnfrepo.CloneQuest(quests)
			result.PostCommitInventory = dnfrepo.CloneInventory(inventory)
			return nil
		}
		if input.AllocateItem == nil {
			return ErrFinishItemAllocatorRequired
		}

		completionKey := fmt.Sprintf("%s/%s/%d", spec.CompletionPrefix, input.CharacterID, spec.SourceQuestID)
		items, err := allocateFinishRewardItems(
			&inventory,
			[]RewardItemRule{{ItemID: spec.ItemID, Count: spec.ItemCount}},
			input.AllocateItem,
			FinishItemGrantRequest{
				QuestID: spec.SourceQuestID, CompletionKey: completionKey, Source: spec.Source,
			},
		)
		if err != nil {
			return err
		}
		if len(items) != 1 || items[0].ItemID != spec.ItemID || items[0].Delta != spec.ItemCount {
			return fmt.Errorf("%w: allocator receipt=%+v", ErrQuestRewardMalformed, items)
		}
		sourceState, sourceField, known := mutableQuestState(&quests, spec.SourceQuestID)
		if !known {
			return spec.MismatchErr
		}
		if sourceState.Extra == nil {
			sourceState.Extra = make(map[string]string, 1)
		}
		receiptJSON, err := json.Marshal(storySourceRecoveryReceipt{
			Version:         storySourceRecoveryVersion,
			CharacterID:     input.CharacterID,
			SourceQuestID:   spec.SourceQuestID,
			ParentQuestID:   spec.ParentQuestID,
			DeliveryQuestID: spec.DeliveryQuestID,
			ItemID:          spec.ItemID,
			Count:           spec.ItemCount,
			SlotKey:         items[0].SlotKey,
			CompletionKey:   completionKey,
			Source:          spec.Source,
		})
		if err != nil {
			return err
		}
		sourceState.Extra[spec.ReceiptKey] = string(receiptJSON)
		sourceState.UpdatedAt = recoveredAt
		switch sourceField {
		case dnfrepo.QuestFieldStates:
			quests.States[spec.SourceQuestID] = sourceState
		case dnfrepo.QuestFieldProgress:
			quests.Progress[spec.SourceQuestID] = sourceState
		default:
			return spec.MismatchErr
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
		return storySourceRecoveryResult{}, err
	}
	return result, nil
}

func validateStorySourceRecoveryDefinitions(catalog *Catalog, spec storySourceRecoverySpec) error {
	source, sourceKnown := catalog.Find(spec.SourceQuestID)
	parent, parentKnown := catalog.Find(spec.ParentQuestID)
	meet, meetKnown := catalog.Find(spec.MeetQuestID)
	delivery, deliveryKnown := catalog.Find(spec.DeliveryQuestID)
	validMeetType := spec.MeetType == "" || normalizeQuestTag(meet.Type) == spec.MeetType
	if !sourceKnown || !parentKnown || !meetKnown || !deliveryKnown ||
		normalizeQuestTag(source.RewardType) != "item" ||
		len(source.RewardIntData) != 2 ||
		source.RewardIntData[0] != spec.ItemID ||
		source.RewardIntData[1] != spec.ItemCount ||
		normalizeQuestTag(parent.Type) != "quest clear" ||
		len(parent.IntData) != 2 ||
		parent.IntData[0] != spec.MeetQuestID ||
		parent.IntData[1] != spec.DeliveryQuestID ||
		meet.MainQuestID != spec.ParentQuestID ||
		!validMeetType ||
		delivery.MainQuestID != spec.ParentQuestID ||
		normalizeQuestTag(delivery.Type) != spec.DeliveryType ||
		len(delivery.IntData) < 2 ||
		delivery.IntData[0] != spec.ItemID ||
		delivery.IntData[1] != spec.ItemCount {
		return fmt.Errorf("%w: source=%d parent=%d meet=%d delivery=%d",
			spec.MismatchErr, spec.SourceQuestID, spec.ParentQuestID, spec.MeetQuestID, spec.DeliveryQuestID)
	}
	return nil
}

func storySourceInventoryCount(inventory dnfrepo.InventoryRecord, itemID int64) int64 {
	var count int64
	for _, stack := range inventory.Slots {
		if stack.ItemID != itemID || stack.Count < 0 || stack.Count > math.MaxInt64-count {
			continue
		}
		if present := finishStackAvailableCount(stack); present > 0 {
			count += present
		}
	}
	return count
}
