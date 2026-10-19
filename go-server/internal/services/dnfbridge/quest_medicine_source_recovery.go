package dnfbridge

import (
	"context"
	"errors"

	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	errCurrentMedicineSourceRecoveryItemType      = errors.New("medicine PVF item is not the observed quest item type")
	errCurrentMedicineSourceRecoveryInventorySend = errors.New("medicine source recovery inventory update send failed")
)

var currentMedicineSourceRecoverySpec = currentStorySourceRecoverySpec{
	SourceQuestID:   dnfquest.MedicineSourceQuestID,
	ParentQuestID:   dnfquest.MedicineSourceParentQuestID,
	DeliveryQuestID: dnfquest.MedicineSourceDeliveryQuestID,
	ItemID:          dnfquest.MedicineSourceItemID,
	ReceiptKey:      dnfquest.MedicineSourceRecoveryReceiptKey,
	Candidate:       dnfquest.IsMedicineSourceRecoveryCandidate,
	Recover: func(
		ctx context.Context,
		owner *dnfquest.Owner,
		catalog *dnfquest.Catalog,
		input currentStorySourceRecoveryInput,
	) (currentStorySourceRecoveryResult, error) {
		result, err := owner.RecoverMedicineSource(ctx, catalog, dnfquest.MedicineSourceRecoveryInput{
			AccountID: input.AccountID, CharacterID: input.CharacterID,
			RecoveredAt: input.RecoveredAt, AllocateItem: input.AllocateItem,
		})
		return currentStorySourceRecoveryResult{
			Recovered: result.Recovered, PostCommitQuest: result.PostCommitQuest,
			PostCommitInventory: result.PostCommitInventory, Item: result.Item,
		}, err
	},
	UnavailableErr:    dnfquest.ErrMedicineSourceRecoveryUnavailable,
	ItemTypeErr:       errCurrentMedicineSourceRecoveryItemType,
	InventorySendErr:  errCurrentMedicineSourceRecoveryInventorySend,
	InventoryLogEvent: "game-current-medicine-source-recovery-inventory-send",
	RecoveredLogEvent: "game-current-medicine-source-recovered",
}

func (s *Service) recoverCurrentMedicineSourceForSnapshot(
	ctx context.Context,
	session *gameSession,
	record dnfrepo.QuestRecord,
) (dnfrepo.QuestRecord, bool, error) {
	return s.recoverCurrentStorySourceForSnapshot(ctx, session, record, currentMedicineSourceRecoverySpec)
}

func (s *Service) sendCurrentMedicineSourceRecoveryInventoryUpdate(
	session *gameSession,
	result dnfquest.MedicineSourceRecoveryResult,
) error {
	return s.sendCurrentStorySourceRecoveryInventoryUpdate(session, currentStorySourceRecoveryResult{
		Recovered: result.Recovered, PostCommitQuest: result.PostCommitQuest,
		PostCommitInventory: result.PostCommitInventory, Item: result.Item,
	}, currentMedicineSourceRecoverySpec.InventoryLogEvent)
}

func currentMedicineSourceRecoveryItemTypeError(err error) bool {
	return currentStorySourceRecoveryItemTypeError(err, errCurrentMedicineSourceRecoveryItemType)
}
