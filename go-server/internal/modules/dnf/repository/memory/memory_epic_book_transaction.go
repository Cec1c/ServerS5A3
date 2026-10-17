package memory

import (
	"context"
	"errors"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

var _ repository.EpicBookAssetUnitOfWork = (*memoryAccountCharacterAssetUnitOfWork)(nil)

type memoryEpicBookDebit struct {
	ctx    context.Context
	record repository.AccountRecord
	dirty  bool
}

func (d *memoryEpicBookDebit) DebitPiece(ctx context.Context, pieceID, amount uint32, now time.Time) (uint32, error) {
	if err := d.ctx.Err(); err != nil {
		return 0, err
	}
	if err := ctxErr(ctx); err != nil {
		return 0, err
	}
	if pieceID == 0 || amount == 0 {
		return 0, repository.ErrAccountEpicPieceIDRequired
	}
	key := repository.AccountEpicPieceMetadataKey(pieceID)
	old := repository.ParseAccountEpicPieceBalance(d.record.Metadata[key])
	if old < amount {
		return 0, repository.ErrEpicBookPieceInsufficient
	}
	d.record.Metadata[key] = repository.FormatAccountEpicPieceBalance(old - amount)
	d.record.UpdatedAt = now.UTC()
	d.dirty = true
	return old - amount, nil
}

func (u *memoryAccountCharacterAssetUnitOfWork) WithinEpicBookAssets(ctx context.Context, accountID, characterID string,
	apply func(repository.EpicBookPieceDebitor, repository.CharacterRecord, repository.AccountInventoryRepository, repository.InventoryRepository) error,
) (err error) {
	accountID, characterID = strings.TrimSpace(accountID), strings.TrimSpace(characterID)
	if u == nil || u.account == nil || u.accountInventory == nil || u.character == nil || u.inventory == nil || accountID == "" || characterID == "" || apply == nil {
		return repository.ErrEpicBookTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	lock := &u.mu
	if u.sharedMu != nil {
		lock = u.sharedMu
	}
	lock.Lock()
	defer lock.Unlock()
	// This is also the credit and metadata-entry mutex. Keep it through commit
	// and restoration; aggregateTxMu alone does not cover ground-piece pickup.
	u.account.nameMu.Lock()
	defer u.account.nameMu.Unlock()
	account, found, err := u.account.MemoryStore.Load(ctx, accountID)
	if err != nil {
		return err
	}
	if !found || account.AccountID != accountID {
		return repository.ErrEpicBookOwnership
	}
	character, found, err := u.character.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if !found || character.CharacterID != characterID || character.AccountID != accountID {
		return repository.ErrEpicBookOwnership
	}
	shared, sharedExists, err := u.accountInventory.Load(ctx, accountID)
	if err != nil {
		return err
	}
	inventory, inventoryExists, err := u.inventory.Load(ctx, characterID)
	if err != nil {
		return err
	}
	debit := &memoryEpicBookDebit{ctx: ctx, record: repository.CloneAccount(account)}
	if debit.record.Metadata == nil {
		debit.record.Metadata = map[string]string{}
	}
	sharedTx := &memoryAccountInventoryTransaction{accountID: accountID, record: repository.CloneAccountInventory(shared), exists: sharedExists}
	inventoryTx := &memoryInventoryTransaction{characterID: characterID, record: repository.CloneInventory(inventory), exists: inventoryExists}
	if err = apply(debit, repository.CloneCharacter(character), sharedTx, inventoryTx); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	committed, debitAttempted, sharedAttempted, inventoryAttempted := false, false, false, false
	defer func() {
		if committed {
			return
		}
		err = errors.Join(err,
			restoreRaidRewardAttempt(func() error {
				return restoreInventoryRecordIfDirty(u.inventory, characterID, inventory, inventoryExists, inventoryAttempted)
			}),
			restoreRaidRewardAttempt(func() error {
				return restoreAccountInventoryRecordIfDirty(u.accountInventory, accountID, shared, sharedExists, sharedAttempted)
			}),
			restoreRaidRewardAttempt(func() error {
				if debitAttempted {
					return u.account.MemoryStore.Save(context.Background(), account)
				}
				return nil
			}))
	}()
	if debit.dirty {
		debitAttempted = true
		// The outer nameMu owns the critical section; Save would relock it.
		if err = u.account.MemoryStore.Save(ctx, debit.record); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	if sharedTx.dirty {
		sharedAttempted = true
		if err = u.accountInventory.Save(ctx, sharedTx.record); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	if inventoryTx.dirty {
		inventoryAttempted = true
		if err = u.inventory.Save(ctx, inventoryTx.record); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	committed = true
	return nil
}
