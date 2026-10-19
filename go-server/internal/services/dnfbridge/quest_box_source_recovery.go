package dnfbridge

import (
	"context"
	"errors"

	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	errCurrentBoxSourceRecoveryItemType      = errors.New("box PVF item is not the observed quest item type")
	errCurrentBoxSourceRecoveryInventorySend = errors.New("box source recovery inventory update send failed")
)

var currentBoxSourceRecoverySpec = currentStorySourceRecoverySpec{
	SourceQuestID:   dnfquest.BoxSourceQuestID,
	ParentQuestID:   dnfquest.BoxSourceParentQuestID,
	DeliveryQuestID: dnfquest.BoxSourceDeliveryQuestID,
	ItemID:          dnfquest.BoxSourceItemID,
	ReceiptKey:      dnfquest.BoxSourceRecoveryReceiptKey,
	Candidate:       dnfquest.IsBoxSourceRecoveryCandidate,
	Recover: func(
		ctx context.Context,
		owner *dnfquest.Owner,
		catalog *dnfquest.Catalog,
		input currentStorySourceRecoveryInput,
	) (currentStorySourceRecoveryResult, error) {
		result, err := owner.RecoverBoxSource(ctx, catalog, dnfquest.BoxSourceRecoveryInput{
			AccountID: input.AccountID, CharacterID: input.CharacterID,
			RecoveredAt: input.RecoveredAt, AllocateItem: input.AllocateItem,
		})
		return currentStorySourceRecoveryResult{
			Recovered: result.Recovered, PostCommitQuest: result.PostCommitQuest,
			PostCommitInventory: result.PostCommitInventory, Item: result.Item,
		}, err
	},
	UnavailableErr:    dnfquest.ErrBoxSourceRecoveryUnavailable,
	ItemTypeErr:       errCurrentBoxSourceRecoveryItemType,
	InventorySendErr:  errCurrentBoxSourceRecoveryInventorySend,
	InventoryLogEvent: "game-current-box-source-recovery-inventory-send",
	RecoveredLogEvent: "game-current-box-source-recovered",
}

func (s *Service) recoverCurrentBoxSourceForSnapshot(
	ctx context.Context,
	session *gameSession,
	record dnfrepo.QuestRecord,
) (dnfrepo.QuestRecord, bool, error) {
	return s.recoverCurrentStorySourceForSnapshot(ctx, session, record, currentBoxSourceRecoverySpec)
}

func (s *Service) sendCurrentBoxSourceRecoveryInventoryUpdate(
	session *gameSession,
	result dnfquest.BoxSourceRecoveryResult,
) error {
	return s.sendCurrentStorySourceRecoveryInventoryUpdate(session, currentStorySourceRecoveryResult{
		Recovered: result.Recovered, PostCommitQuest: result.PostCommitQuest,
		PostCommitInventory: result.PostCommitInventory, Item: result.Item,
	}, currentBoxSourceRecoverySpec.InventoryLogEvent)
}

func currentBoxSourceRecoveryItemTypeError(err error) bool {
	return currentStorySourceRecoveryItemTypeError(err, errCurrentBoxSourceRecoveryItemType)
}
