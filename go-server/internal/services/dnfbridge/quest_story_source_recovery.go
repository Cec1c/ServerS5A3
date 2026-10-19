package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type currentStorySourceRecoverySpec struct {
	SourceQuestID     int64
	ParentQuestID     int64
	DeliveryQuestID   int64
	ItemID            int64
	ReceiptKey        string
	Candidate         func(dnfrepo.QuestRecord) bool
	Recover           func(context.Context, *dnfquest.Owner, *dnfquest.Catalog, currentStorySourceRecoveryInput) (currentStorySourceRecoveryResult, error)
	UnavailableErr    error
	ItemTypeErr       error
	InventorySendErr  error
	InventoryLogEvent string
	RecoveredLogEvent string
}

type currentStorySourceRecoveryInput struct {
	AccountID    string
	CharacterID  string
	RecoveredAt  time.Time
	AllocateItem dnfquest.FinishItemAllocator
}

type currentStorySourceRecoveryResult struct {
	Recovered           bool
	PostCommitQuest     dnfrepo.QuestRecord
	PostCommitInventory dnfrepo.InventoryRecord
	Item                dnfquest.FinishCommittedItem
}

func (s *Service) recoverCurrentStorySourceForSnapshot(
	ctx context.Context,
	session *gameSession,
	record dnfrepo.QuestRecord,
	spec currentStorySourceRecoverySpec,
) (dnfrepo.QuestRecord, bool, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 || !spec.Candidate(record) {
		return record, false, nil
	}
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		return record, false, fmt.Errorf("load quest catalog for story source recovery: %w", err)
	}
	_, itemCatalog, err := s.currentQuestFinishResources(ctx)
	if err != nil {
		return record, false, fmt.Errorf("load item catalog for story source recovery: %w", err)
	}
	definition, err := itemCatalog.ResolveItem(uint32(spec.ItemID))
	if err != nil {
		return record, false, err
	}
	if definition.Kind != dungeonDropItemStackable ||
		normalizeDungeonDropStackableType(definition.StackableType) != "[quest]" {
		return record, false, fmt.Errorf(
			"%w: item=%d kind=%s stackable_type=%q",
			spec.ItemTypeErr,
			spec.ItemID,
			definition.Kind,
			definition.StackableType,
		)
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.CharacterSettlement == nil {
		return record, false, spec.UnavailableErr
	}
	owner, err := dnfquest.NewOwner(repositories)
	if err != nil {
		return record, false, err
	}
	result, err := spec.Recover(
		ctx,
		owner,
		catalog,
		currentStorySourceRecoveryInput{
			AccountID:    s.accountIDForSession(session),
			CharacterID:  strconv.Itoa(int(session.selectedCharacterID)),
			RecoveredAt:  s.gameplayNow().UTC(),
			AllocateItem: currentQuestFinishItemAllocator(itemCatalog),
		},
	)
	if err != nil {
		return record, false, err
	}
	nextRecord := record
	if result.PostCommitQuest.CharacterID != "" {
		nextRecord = result.PostCommitQuest
	}
	if !result.Recovered {
		return nextRecord, false, nil
	}
	if err := s.sendCurrentStorySourceRecoveryInventoryUpdate(session, result, spec.InventoryLogEvent); err != nil {
		return record, false, fmt.Errorf("%w: %v", spec.InventorySendErr, err)
	}
	s.logGameEvent(session, spec.RecoveredLogEvent,
		"char_id", session.selectedCharacterID,
		"source_quest_id", spec.SourceQuestID,
		"parent_quest_id", spec.ParentQuestID,
		"delivery_quest_id", spec.DeliveryQuestID,
		"item_id", result.Item.ItemID,
		"count", result.Item.Delta,
		"slot_key", result.Item.SlotKey,
		"receipt_key", spec.ReceiptKey,
		"packet_order", "class0_op14_inventory_then_active_quest_snapshot")
	return nextRecord, true, nil
}

func (s *Service) sendCurrentStorySourceRecoveryInventoryUpdate(
	session *gameSession,
	result currentStorySourceRecoveryResult,
	logEvent string,
) error {
	if session == nil {
		return nil
	}
	stack, found := result.PostCommitInventory.Slots[result.Item.SlotKey]
	if !found || stack.ItemID != result.Item.ItemID ||
		stack.Count != result.Item.PostCount || len(result.Item.RawEntry) == 0 {
		return fmt.Errorf("story source committed inventory row is missing or stale")
	}
	entry := currentItemListEntryFromStack(
		dnfrepo.MainInventoryListType,
		int16(result.Item.SlotIndex),
		stack,
	)
	body := buildCurrentItemUpdateBody(
		dnfrepo.MainInventoryListType,
		[]currentItemListEntry{entry},
	)
	s.logGameEvent(session, logEvent,
		"char_id", session.selectedCharacterID,
		"item_id", result.Item.ItemID,
		"slot_index", result.Item.SlotIndex,
		"count", result.Item.PostCount,
		"msg_id", 14,
		"classification", 0,
		"body_len", len(body),
		"body_source", "current_exe_class0_op14_raw77")
	return s.sendGameUpperRawClass(session, 14, body, 0)
}

func currentStorySourceRecoveryItemTypeError(err error, want error) bool {
	return errors.Is(err, want)
}
