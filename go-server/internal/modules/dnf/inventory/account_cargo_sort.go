package inventory

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Account cargo has one account-owned map. Never sort its old per-character
// residue or include crystal/soul warehouse slots that share this repository.
func (o *Owner) sortAccountCargo(ctx context.Context, cmd Command) (SortResult, error) {
	accountID := strings.TrimSpace(cmd.AccountID)
	if accountID == "" {
		return SortResult{}, ErrAccountRequired
	}
	if o.accountRepo == nil || o.accountItems == nil || o.accounts == nil {
		return SortResult{}, ErrAccountSharedOwnerUnavailable
	}
	account, found, err := o.accounts.Load(ctx, accountID)
	if err != nil {
		return SortResult{}, err
	}
	if !found {
		return SortResult{}, ErrAccountRequired
	}
	capacity, created := accountCargoCapacity(account)
	if !created {
		return SortResult{}, ErrAccountCargoNotCreated
	}
	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	result := SortResult{CharacterID: characterID, ListType: listTypeAccountCargo, Category: cmd.Category, StartSlot: 0, EndSlot: capacity - 1, Mode: "noop"}
	err = o.accountItems.WithinAccountCharacterItems(ctx, accountID, characterID, func(accounts dnfrepo.AccountInventoryRepository, characters dnfrepo.InventoryRepository) error {
		if _, exists, err := characters.Load(ctx, characterID); err != nil {
			return err
		} else if !exists {
			return ErrInventoryNotFound
		}
		cargo, exists, err := accounts.Load(ctx, accountID)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		cargo = dnfrepo.CloneAccountInventory(cargo)
		result.Refresh = cloneItemMap(cargo.Slots)
		// Category 11 is the cargo segment in the current sort command.
		if cmd.Category != 11 {
			return nil
		}
		candidates := sortCandidates(cargo.Slots, listTypeAccountCargo, 0, capacity-1)
		result.MovedCount = len(candidates)
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].kind != candidates[j].kind {
				return candidates[i].kind < candidates[j].kind
			}
			if candidates[i].stack.ItemID != candidates[j].stack.ItemID {
				return candidates[i].stack.ItemID < candidates[j].stack.ItemID
			}
			return candidates[i].slot < candidates[j].slot
		})
		for i, candidate := range candidates {
			if candidate.slot != int16(i) {
				result.Changed = true
				break
			}
		}
		if !result.Changed {
			return nil
		}
		for _, candidate := range candidates {
			delete(cargo.Slots, slotKey(listTypeAccountCargo, candidate.slot))
		}
		for i, candidate := range candidates {
			cargo.Slots[slotKey(listTypeAccountCargo, int16(i))] = candidate.stack
		}
		cargo.AccountID = accountID
		cargo.UpdatedAt = time.Now()
		if err := accounts.Save(ctx, cargo); err != nil {
			return err
		}
		result.Mode = "sort"
		result.Refresh = cloneItemMap(cargo.Slots)
		return nil
	})
	if err != nil {
		return SortResult{}, err
	}
	return result, nil
}
