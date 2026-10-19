package quest

import (
	"context"
	"errors"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	MedicineSourceQuestID         int64 = 3346
	MedicineSourceParentQuestID   int64 = 3347
	MedicineSourceMeetQuestID     int64 = 3425
	MedicineSourceDeliveryQuestID int64 = 3426
	MedicineSourceItemID          int64 = 10164764

	MedicineSourceRecoveryReceiptKey = "medicine_source_recovery_receipt_v1"
)

var (
	ErrMedicineSourceRecoveryUnavailable        = errors.New("medicine source recovery transaction is unavailable")
	ErrMedicineSourceRecoveryDefinitionMismatch = errors.New("medicine source relation does not match the supported PVF definition")
)

var medicineSourceRecoverySpec = storySourceRecoverySpec{
	SourceQuestID:    MedicineSourceQuestID,
	ParentQuestID:    MedicineSourceParentQuestID,
	MeetQuestID:      MedicineSourceMeetQuestID,
	DeliveryQuestID:  MedicineSourceDeliveryQuestID,
	ItemID:           MedicineSourceItemID,
	ItemCount:        1,
	DeliveryType:     "seeking",
	MeetType:         "hunt enemy",
	ReceiptKey:       MedicineSourceRecoveryReceiptKey,
	CompletionPrefix: "medicine-source-recovery",
	Source:           "quest_source_recovery:3346->3347/3425,3426",
	UnavailableErr:   ErrMedicineSourceRecoveryUnavailable,
	MismatchErr:      ErrMedicineSourceRecoveryDefinitionMismatch,
}

// MedicineSourceRecoveryInput is the exact owner scope for the one confirmed
// source relation. Allocator applies the PVF-backed item row to the
// transaction-scoped inventory clone.
type MedicineSourceRecoveryInput struct {
	AccountID    string
	CharacterID  string
	RecoveredAt  time.Time
	AllocateItem FinishItemAllocator
}

// MedicineSourceRecoveryResult is detached from the transaction. When
// Recovered is true, Item identifies the exact inventory row whose op14 update
// must be sent before the active-quest snapshot.
type MedicineSourceRecoveryResult struct {
	Recovered           bool
	PostCommitQuest     dnfrepo.QuestRecord
	PostCommitInventory dnfrepo.InventoryRecord
	Item                FinishCommittedItem
}

// IsMedicineSourceRecoveryCandidate accepts only a no-reward-completed 3346
// while the active 3347 parent and 3426 medicine-delivery child still need the
// missing quest item. The 3425 hunt objective may already be complete.
func IsMedicineSourceRecoveryCandidate(record dnfrepo.QuestRecord) bool {
	return isStorySourceRecoveryCandidate(record, medicineSourceRecoverySpec)
}

// RecoverMedicineSource grants exactly one missing medicine when PVF still
// proves 3346 -> (3347, 3425/3426) and 3426 still consumes the item.
func (o *Owner) RecoverMedicineSource(
	ctx context.Context,
	catalog *Catalog,
	input MedicineSourceRecoveryInput,
) (MedicineSourceRecoveryResult, error) {
	result, err := recoverStorySource(o, ctx, catalog, medicineSourceRecoverySpec, storySourceRecoveryInput{
		AccountID: input.AccountID, CharacterID: input.CharacterID,
		RecoveredAt: input.RecoveredAt, AllocateItem: input.AllocateItem,
	})
	if err != nil {
		return MedicineSourceRecoveryResult{}, err
	}
	return MedicineSourceRecoveryResult{
		Recovered: result.Recovered, PostCommitQuest: result.PostCommitQuest,
		PostCommitInventory: result.PostCommitInventory, Item: result.Item,
	}, nil
}
