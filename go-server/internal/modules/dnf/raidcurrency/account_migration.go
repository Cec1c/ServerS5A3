package raidcurrency

import (
	"context"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// MigrateAccount visits the real account roster before its first wallet
// projection. Each character is consolidated in the same account-first asset
// transaction as its source deletion. A partial failure can safely be retried;
// a previously committed character has no remaining source rows to credit.
func MigrateAccount(ctx context.Context, group repo.Group, accountID string, now time.Time) (uint64, error) {
	if group.Character == nil || group.Inventory == nil || accountID == "" {
		return 0, ErrState
	}
	characters, err := group.Character.ListByAccount(ctx, accountID, repo.DefaultCharacterSlotLimit)
	if err != nil {
		return 0, err
	}
	var moved uint64
	for _, character := range characters {
		if character.CharacterID == "" || character.AccountID != accountID {
			return moved, ErrState
		}
		inventory, found, err := group.Inventory.Load(ctx, character.CharacterID)
		if err != nil {
			return moved, err
		}
		if !found {
			continue
		}
		if inventory.CharacterID != character.CharacterID {
			return moved, ErrState
		}
		contains := false
		for _, stack := range inventory.Slots {
			if stack.ItemID == int64(FateItemID) {
				contains = true
				break
			}
		}
		if !contains {
			continue
		}
		amount, err := MigrateCharacter(ctx, group, accountID, character.CharacterID, now)
		if err != nil {
			return moved, err
		}
		moved += uint64(amount)
	}
	return moved, nil
}
