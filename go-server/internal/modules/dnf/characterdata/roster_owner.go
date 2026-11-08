package characterdata

import (
	"context"
	"errors"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrRosterOwnerUnavailable = errors.New("character roster owner unavailable")

// RosterOwner owns durable character-selector roster mutations.
type RosterOwner struct {
	characters dnfrepo.CharacterRepository
}

func NewRosterOwner(repositories dnfrepo.Group) (*RosterOwner, error) {
	if repositories.Character == nil {
		return nil, ErrRosterOwnerUnavailable
	}
	return &RosterOwner{characters: repositories.Character}, nil
}

// SwapSlotsFromSnapshot rejects rows replaced or moved since the selector was sent.
func (o *RosterOwner) SwapSlotsFromSnapshot(ctx context.Context, accountID string, left, right dnfrepo.CharacterRecord) error {
	if o == nil || o.characters == nil {
		return ErrRosterOwnerUnavailable
	}
	if accountID == "" || left.AccountID != accountID || right.AccountID != accountID ||
		left.CharacterID == "" || right.CharacterID == "" || left.Slot < 0 || right.Slot < 0 ||
		left.Slot >= dnfrepo.DefaultCharacterSlotLimit || right.Slot >= dnfrepo.DefaultCharacterSlotLimit {
		return dnfrepo.ErrCharacterSlotMissing
	}
	if swapper, ok := o.characters.(dnfrepo.CharacterSnapshotSlotSwapper); ok {
		return swapper.SwapCharacterSlotsFromSnapshot(ctx, accountID, left, right)
	}
	return dnfrepo.ErrCharacterSlotSwapUnavailable
}

// SwapSlots exchanges two occupied selector slots for one account.
func (o *RosterOwner) SwapSlots(ctx context.Context, accountID string, slotA, slotB int) error {
	if o == nil || o.characters == nil {
		return ErrRosterOwnerUnavailable
	}
	return dnfrepo.SwapCharacterSlots(ctx, o.characters, accountID, slotA, slotB)
}
