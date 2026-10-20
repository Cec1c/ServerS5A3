package dnfbridge

import (
	"context"
	"errors"

	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	errCurrentQueenLetterRecoveryItemType      = errors.New("queen letter PVF item is not the observed quest item type")
	errCurrentQueenLetterRecoveryInventorySend = errors.New("queen letter recovery inventory update send failed")
)

var currentQueenLetterRecoverySpec = currentStorySourceRecoverySpec{
	SourceQuestID:   dnfquest.QueenLetterSourceQuestID,
	ParentQuestID:   dnfquest.QueenLetterParentQuestID,
	DeliveryQuestID: dnfquest.QueenLetterDeliveryQuestID,
	ItemID:          dnfquest.QueenLetterItemID,
	ReceiptKey:      dnfquest.QueenLetterRecoveryReceiptKey,
	Candidate:       dnfquest.IsQueenLetterRecoveryCandidate,
	Recover: func(
		ctx context.Context,
		owner *dnfquest.Owner,
		catalog *dnfquest.Catalog,
		input currentStorySourceRecoveryInput,
	) (currentStorySourceRecoveryResult, error) {
		result, err := owner.RecoverQueenLetter(ctx, catalog, dnfquest.QueenLetterRecoveryInput{
			AccountID: input.AccountID, CharacterID: input.CharacterID,
			RecoveredAt: input.RecoveredAt, AllocateItem: input.AllocateItem,
		})
		return currentStorySourceRecoveryResult{
			Recovered: result.Recovered, PostCommitQuest: result.PostCommitQuest,
			PostCommitInventory: result.PostCommitInventory, Item: result.Item,
		}, err
	},
	UnavailableErr:    dnfquest.ErrQueenLetterRecoveryUnavailable,
	ItemTypeErr:       errCurrentQueenLetterRecoveryItemType,
	InventorySendErr:  errCurrentQueenLetterRecoveryInventorySend,
	InventoryLogEvent: "game-current-queen-letter-recovery-inventory-send",
	RecoveredLogEvent: "game-current-queen-letter-source-recovered",
}

// recoverCurrentQueenLetterForSnapshot is deliberately called only by the
// live session snapshot path. The completed-record snapshot path never reopens
// a domain transaction after a finish has already committed.
func (s *Service) recoverCurrentQueenLetterForSnapshot(
	ctx context.Context,
	session *gameSession,
	record dnfrepo.QuestRecord,
) (dnfrepo.QuestRecord, bool, error) {
	return s.recoverCurrentStorySourceForSnapshot(ctx, session, record, currentQueenLetterRecoverySpec)
}

func (s *Service) sendCurrentQueenLetterRecoveryInventoryUpdate(
	session *gameSession,
	result dnfquest.QueenLetterRecoveryResult,
) error {
	return s.sendCurrentStorySourceRecoveryInventoryUpdate(session, currentStorySourceRecoveryResult{
		Recovered: result.Recovered, PostCommitQuest: result.PostCommitQuest,
		PostCommitInventory: result.PostCommitInventory, Item: result.Item,
	}, currentQueenLetterRecoverySpec.InventoryLogEvent)
}

func currentQueenLetterRecoveryItemTypeError(err error) bool {
	return currentStorySourceRecoveryItemTypeError(err, errCurrentQueenLetterRecoveryItemType)
}
