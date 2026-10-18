package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	errCurrentQueenLetterRecoveryItemType      = errors.New("queen letter PVF item is not the observed quest item type")
	errCurrentQueenLetterRecoveryInventorySend = errors.New("queen letter recovery inventory update send failed")
)

// recoverCurrentQueenLetterForSnapshot is deliberately called only by the
// live session snapshot path. The completed-record snapshot path never reopens
// a domain transaction after a finish has already committed.
func (s *Service) recoverCurrentQueenLetterForSnapshot(
	ctx context.Context,
	session *gameSession,
	record dnfrepo.QuestRecord,
) (dnfrepo.QuestRecord, bool, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 ||
		!dnfquest.IsQueenLetterRecoveryCandidate(record) {
		return record, false, nil
	}
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		return record, false, fmt.Errorf("load quest catalog for queen letter recovery: %w", err)
	}
	// itemCatalog is the same PVF-backed item catalog used by normal finish
	// rewards. Progression tables are not consumed by this source-only grant.
	_, itemCatalog, err := s.currentQuestFinishResources(ctx)
	if err != nil {
		return record, false, fmt.Errorf("load item catalog for queen letter recovery: %w", err)
	}
	definition, err := itemCatalog.ResolveItem(uint32(dnfquest.QueenLetterItemID))
	if err != nil {
		return record, false, err
	}
	if definition.Kind != dungeonDropItemStackable ||
		normalizeDungeonDropStackableType(definition.StackableType) != "[quest]" {
		return record, false, fmt.Errorf(
			"%w: item=%d kind=%s stackable_type=%q",
			errCurrentQueenLetterRecoveryItemType,
			dnfquest.QueenLetterItemID,
			definition.Kind,
			definition.StackableType,
		)
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.CharacterSettlement == nil {
		return record, false, dnfquest.ErrQueenLetterRecoveryUnavailable
	}
	owner, err := dnfquest.NewOwner(repositories)
	if err != nil {
		return record, false, err
	}
	result, err := owner.RecoverQueenLetter(ctx, catalog, dnfquest.QueenLetterRecoveryInput{
		AccountID:    s.accountIDForSession(session),
		CharacterID:  strconv.Itoa(int(session.selectedCharacterID)),
		RecoveredAt:  s.gameplayNow().UTC(),
		AllocateItem: currentQuestFinishItemAllocator(itemCatalog),
	})
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
	if err := s.sendCurrentQueenLetterRecoveryInventoryUpdate(session, result); err != nil {
		return record, false, fmt.Errorf("%w: %v", errCurrentQueenLetterRecoveryInventorySend, err)
	}
	s.logGameEvent(session, "game-current-queen-letter-source-recovered",
		"char_id", session.selectedCharacterID,
		"source_quest_id", dnfquest.QueenLetterSourceQuestID,
		"parent_quest_id", dnfquest.QueenLetterParentQuestID,
		"delivery_quest_id", dnfquest.QueenLetterDeliveryQuestID,
		"item_id", result.Item.ItemID,
		"count", result.Item.Delta,
		"slot_key", result.Item.SlotKey,
		"receipt_key", dnfquest.QueenLetterRecoveryReceiptKey,
		"packet_order", "class0_op14_inventory_then_active_quest_snapshot")
	return nextRecord, true, nil
}

func (s *Service) sendCurrentQueenLetterRecoveryInventoryUpdate(
	session *gameSession,
	result dnfquest.QueenLetterRecoveryResult,
) error {
	if session == nil {
		return nil
	}
	stack, found := result.PostCommitInventory.Slots[result.Item.SlotKey]
	if !found || stack.ItemID != result.Item.ItemID ||
		stack.Count != result.Item.PostCount || len(result.Item.RawEntry) == 0 {
		return fmt.Errorf("queen letter committed inventory row is missing or stale")
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
	s.logGameEvent(session, "game-current-queen-letter-recovery-inventory-send",
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

func currentQueenLetterRecoveryItemTypeError(err error) bool {
	return errors.Is(err, errCurrentQueenLetterRecoveryItemType)
}
