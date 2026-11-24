package inventory

import (
	"context"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The EXE requests a normal material-page slot when withdrawing from personal
// cargo. Resolve the actual item again under the account/character item lock,
// then credit its fixed account warehouse slot in that same transaction.
func (o *Owner) withdrawPersonalCargoWarehouseMaterial(ctx context.Context, characterID string, cmd Command) (MoveResult, error) {
	if strings.TrimSpace(cmd.AccountID) == "" {
		return MoveResult{}, ErrAccountRequired
	}
	if o.accountRepo == nil || o.accountItems == nil {
		return MoveResult{}, ErrAccountSharedOwnerUnavailable
	}
	var result MoveResult
	err := o.accountItems.WithinAccountCharacterItems(ctx, cmd.AccountID, characterID, func(accounts dnfrepo.AccountInventoryRepository, characters dnfrepo.InventoryRepository) error {
		record, found, err := characters.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found {
			return ErrInventoryNotFound
		}
		source, found := record.Warehouse[slotKey(listTypePersonalCargo, cmd.SourceSlotIndex)]
		if !found || source.Count <= 0 {
			return ErrSlotNotFound
		}
		slot, mapped := dnfrepo.AccountWarehouseSlotForItem(source.ItemID)
		if !mapped {
			return ErrMoveItemPlacement
		}
		account, _, err := accounts.Load(ctx, cmd.AccountID)
		if err != nil {
			return err
		}
		if destination, occupied := account.Slots[slotKey(listTypeMain, slot)]; occupied &&
			!canStack(source, destination, normalizeMoveCount(source, int64(cmd.MoveCount))) {
			return ErrMoveCannotStack
		}
		cmd.DestinationSlotIndex = slot
		txOwner := *o
		txOwner.repo, txOwner.accountRepo, txOwner.inAccountItemTx = characters, accounts, true
		result, err = txOwner.moveAccountShared(ctx, characterID, cmd)
		return err
	})
	return result, err
}
