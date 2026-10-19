package quest

import (
	"context"
	"errors"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	BoxSourceQuestID         int64 = 3649
	BoxSourceParentQuestID   int64 = 3656
	BoxSourceMeetQuestID     int64 = 3692
	BoxSourceDeliveryQuestID int64 = 3693
	BoxSourceItemID          int64 = 10164767

	BoxSourceRecoveryReceiptKey = "box_source_recovery_receipt_v1"
)

var (
	ErrBoxSourceRecoveryUnavailable        = errors.New("box source recovery transaction is unavailable")
	ErrBoxSourceRecoveryDefinitionMismatch = errors.New("box source relation does not match the supported PVF definition")
)

var boxSourceRecoverySpec = storySourceRecoverySpec{
	SourceQuestID:    BoxSourceQuestID,
	ParentQuestID:    BoxSourceParentQuestID,
	MeetQuestID:      BoxSourceMeetQuestID,
	DeliveryQuestID:  BoxSourceDeliveryQuestID,
	ItemID:           BoxSourceItemID,
	ItemCount:        1,
	DeliveryType:     "seeking",
	MeetType:         "clear map",
	ReceiptKey:       BoxSourceRecoveryReceiptKey,
	CompletionPrefix: "box-source-recovery",
	Source:           "quest_source_recovery:3649->3656/3693",
	UnavailableErr:   ErrBoxSourceRecoveryUnavailable,
	MismatchErr:      ErrBoxSourceRecoveryDefinitionMismatch,
}

type BoxSourceRecoveryInput struct {
	AccountID    string
	CharacterID  string
	RecoveredAt  time.Time
	AllocateItem FinishItemAllocator
}

type BoxSourceRecoveryResult struct {
	Recovered           bool
	PostCommitQuest     dnfrepo.QuestRecord
	PostCommitInventory dnfrepo.InventoryRecord
	Item                FinishCommittedItem
}

// IsBoxSourceRecoveryCandidate accepts only a no-reward-completed 3649 while
// the 3656 parent and 3693 item-delivery child remain active.
func IsBoxSourceRecoveryCandidate(record dnfrepo.QuestRecord) bool {
	return isStorySourceRecoveryCandidate(record, boxSourceRecoverySpec)
}

// RecoverBoxSource grants the one missing quest box when PVF still proves
// 3649 -> (3656, 3692/3693) and 3693 still consumes one box.
func (o *Owner) RecoverBoxSource(
	ctx context.Context,
	catalog *Catalog,
	input BoxSourceRecoveryInput,
) (BoxSourceRecoveryResult, error) {
	result, err := recoverStorySource(o, ctx, catalog, boxSourceRecoverySpec, storySourceRecoveryInput{
		AccountID: input.AccountID, CharacterID: input.CharacterID,
		RecoveredAt: input.RecoveredAt, AllocateItem: input.AllocateItem,
	})
	if err != nil {
		return BoxSourceRecoveryResult{}, err
	}
	return BoxSourceRecoveryResult{
		Recovered: result.Recovered, PostCommitQuest: result.PostCommitQuest,
		PostCommitInventory: result.PostCommitInventory, Item: result.Item,
	}, nil
}
