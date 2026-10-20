package quest

import (
	"context"
	"errors"
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
)

var (
	ErrQueenLetterRecoveryUnavailable        = errors.New("queen letter recovery transaction is unavailable")
	ErrQueenLetterRecoveryDefinitionMismatch = errors.New("queen letter source relation does not match the supported PVF definition")
)

var queenLetterRecoverySpec = storySourceRecoverySpec{
	SourceQuestID:    QueenLetterSourceQuestID,
	ParentQuestID:    QueenLetterParentQuestID,
	MeetQuestID:      QueenLetterMeetQuestID,
	DeliveryQuestID:  QueenLetterDeliveryQuestID,
	ItemID:           QueenLetterItemID,
	ItemCount:        1,
	DeliveryType:     "seek n meet npc",
	ReceiptKey:       QueenLetterRecoveryReceiptKey,
	CompletionPrefix: "queen-letter-recovery",
	Source:           "quest_source_recovery:3226->3249/3610",
	UnavailableErr:   ErrQueenLetterRecoveryUnavailable,
	MismatchErr:      ErrQueenLetterRecoveryDefinitionMismatch,
}

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

// IsQueenLetterRecoveryCandidate is the bridge's cheap pre-catalog check. It
// accepts only the two no-reward auto-complete markers observed for 3226 and
// requires the already-active 3249/3610 pair. Inventory ownership and the
// receipt are checked again inside the settlement transaction.
func IsQueenLetterRecoveryCandidate(record dnfrepo.QuestRecord) bool {
	return isStorySourceRecoveryCandidate(record, queenLetterRecoverySpec)
}

// RecoverQueenLetter grants exactly one missing letter when real PVF data still
// proves 3226 -> (3249, 3609/3610) and 3610 still consumes the letter.
func (o *Owner) RecoverQueenLetter(
	ctx context.Context,
	catalog *Catalog,
	input QueenLetterRecoveryInput,
) (QueenLetterRecoveryResult, error) {
	result, err := recoverStorySource(o, ctx, catalog, queenLetterRecoverySpec, storySourceRecoveryInput{
		AccountID: input.AccountID, CharacterID: input.CharacterID,
		RecoveredAt: input.RecoveredAt, AllocateItem: input.AllocateItem,
	})
	if err != nil {
		return QueenLetterRecoveryResult{}, err
	}
	return QueenLetterRecoveryResult{
		Recovered: result.Recovered, PostCommitQuest: result.PostCommitQuest,
		PostCommitInventory: result.PostCommitInventory, Item: result.Item,
	}, nil
}
