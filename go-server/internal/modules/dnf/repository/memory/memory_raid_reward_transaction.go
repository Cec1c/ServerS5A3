package memory

import (
	"context"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
)

var _ repository.AccountCharacterMailboxAssetUnitOfWork = (*memoryAccountCharacterAssetUnitOfWork)(nil)

func (u *memoryAccountCharacterAssetUnitOfWork) WithinAccountCharacterMailboxAssets(
	ctx context.Context,
	accountID string,
	characterID string,
	apply func(repository.AccountInventoryRepository, repository.CharacterRepository, repository.InventoryRepository, repository.EquipmentRepository, repository.MailboxRepository) error,
) (err error) {
	accountID = strings.TrimSpace(accountID)
	characterID = strings.TrimSpace(characterID)
	if accountID == "" || characterID == "" || apply == nil || u == nil ||
		u.accountInventory == nil || u.character == nil || u.inventory == nil || u.equipment == nil || u.mailbox == nil {
		return repository.ErrAccountCharacterAssetTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	lock := &u.mu
	if u.sharedMu != nil {
		lock = u.sharedMu
	}
	lock.Lock()
	defer lock.Unlock()

	accountRecord, accountExists, err := u.accountInventory.Load(ctx, accountID)
	if err != nil {
		return err
	}
	characterRecord, characterExists, err := u.character.Load(ctx, characterID)
	if err != nil {
		return err
	}
	inventoryRecord, inventoryExists, err := u.inventory.Load(ctx, characterID)
	if err != nil {
		return err
	}
	equipmentRecord, equipmentExists, err := u.equipment.Load(ctx, characterID)
	if err != nil {
		return err
	}

	mailboxRecord, mailboxExists, err := u.mailbox.Load(ctx, characterID)
	if err != nil {
		return err
	}
	mailboxTx := &memoryMailboxTransaction{characterID: characterID, record: repository.CloneMailbox(mailboxRecord), exists: mailboxExists}

	if !characterExists || characterRecord.CharacterID != characterID || characterRecord.AccountID != accountID {
		return repository.ErrAccountCharacterMailboxOwnership
	}

	accountTx := &memoryAccountInventoryTransaction{
		accountID: accountID,
		record:    repository.CloneAccountInventory(accountRecord),
		exists:    accountExists,
	}
	characterTx := &memoryCharacterTransaction{
		characterID: characterID,
		record:      repository.CloneCharacter(characterRecord),
		exists:      characterExists,
	}
	inventoryTx := &memoryInventoryTransaction{
		characterID: characterID,
		record:      repository.CloneInventory(inventoryRecord),
		exists:      inventoryExists,
	}
	equipmentTx := &memoryEquipmentTransaction{
		characterID: characterID,
		record:      repository.CloneEquipment(equipmentRecord),
		exists:      equipmentExists,
	}
	if err := apply(accountTx, characterTx, inventoryTx, equipmentTx, mailboxTx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Mark an attempted physical write BEFORE calling the repository. A Save
	// can mutate then return an error or panic; callback-only rollback is not
	// sufficient. This defer runs before the shared mutex is released.
	committed := false
	accountAttempted, characterAttempted, inventoryAttempted, equipmentAttempted, mailboxAttempted := false, false, false, false, false
	defer func() {
		if committed {
			return
		}
		err = errors.Join(err,
			restoreRaidRewardAttempt(func() error {
				return restoreMailboxRecordIfDirty(u.mailbox, characterID, mailboxRecord, mailboxExists, mailboxAttempted)
			}),
			restoreRaidRewardAttempt(func() error {
				return restoreEquipmentRecordIfDirty(u.equipment, characterID, equipmentRecord, equipmentExists, equipmentAttempted)
			}),
			restoreRaidRewardAttempt(func() error {
				return restoreInventoryRecordIfDirty(u.inventory, characterID, inventoryRecord, inventoryExists, inventoryAttempted)
			}),
			restoreRaidRewardAttempt(func() error {
				return restoreCharacterRecord(u.character, characterID, characterRecord, characterExists, characterAttempted)
			}),
			restoreRaidRewardAttempt(func() error {
				return restoreAccountInventoryRecordIfDirty(u.accountInventory, accountID, accountRecord, accountExists, accountAttempted)
			}))
	}()
	if accountTx.dirty {
		accountAttempted = true
		if err = u.accountInventory.Save(ctx, accountTx.record); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	if characterTx.dirty {
		characterAttempted = true
		if err = u.character.Save(ctx, characterTx.record); err != nil {
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
	if equipmentTx.dirty {
		equipmentAttempted = true
		if err = u.equipment.Save(ctx, equipmentTx.record); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	if mailboxTx.dirty {
		mailboxAttempted = true
		if err = u.mailbox.Save(ctx, mailboxTx.record); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	committed = true
	return nil
}

// Restoration failure must not prevent the other attempted aggregates from
// being restored. An original Save panic still propagates after all attempts.
func restoreRaidRewardAttempt(restore func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("raid reward aggregate restore panicked: %v", p)
		}
	}()
	return restore()
}
