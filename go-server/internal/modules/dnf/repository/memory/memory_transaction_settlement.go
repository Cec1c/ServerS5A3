package memory

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"sync"
)

// memoryCharacterSettlementUnitOfWork buffers all settlement aggregates
// before committing them under the shared aggregate transaction mutex.
type memoryCharacterSettlementUnitOfWork struct {
	mu               sync.Mutex
	sharedMu         *sync.Mutex
	account          repository.AccountRepository
	accountInventory repository.AccountInventoryRepository
	character        repository.CharacterRepository
	quests           repository.QuestRepository
	inventory        repository.InventoryRepository
	equipment        repository.EquipmentRepository
	skills           repository.SkillRepository
	settings         repository.SettingsRepository
}

func (u *memoryCharacterSettlementUnitOfWork) WithinCharacterSettlement(
	ctx context.Context,
	characterID string,
	apply func(repository.Group) error,
) error {
	return u.withinSettlement(ctx, characterID, nil, "", apply)
}

func (u *memoryCharacterSettlementUnitOfWork) WithinGuildSettlement(ctx context.Context, owner repository.GuildOwner, completion string, apply func(repository.Group) error) error {
	if _, err := repository.GuildOwners([]repository.GuildOwner{owner}); err != nil {
		return err
	}
	if completion == "" || u == nil || u.settings == nil {
		return repository.ErrGuildTransaction
	}
	return u.withinSettlement(ctx, owner.CharacterID, &owner, repository.GuildSettlementReceiptScope(owner, completion), apply)
}

func (u *memoryCharacterSettlementUnitOfWork) withinSettlement(ctx context.Context, characterID string, owner *repository.GuildOwner, receiptScope string, apply func(repository.Group) error) (result error) {
	characterID = strings.TrimSpace(characterID)
	if characterID == "" || apply == nil || u == nil || u.character == nil ||
		u.quests == nil || u.inventory == nil || u.equipment == nil || u.skills == nil {
		return repository.ErrCharacterSettlementTransactionUnavailable
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

	character, characterExists, err := u.character.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if owner != nil && (!characterExists || character.CharacterID != owner.CharacterID || character.AccountID != owner.AccountID) {
		return repository.ErrGuildTransaction
	}
	accountID := strings.TrimSpace(character.AccountID)
	var account repository.AccountRecord
	var accountExists bool
	if accountID != "" && u.account != nil {
		account, accountExists, err = u.account.Load(ctx, accountID)
		if err != nil {
			return err
		}
	}
	var accountInventory repository.AccountInventoryRecord
	var accountInventoryExists bool
	if accountID != "" && u.accountInventory != nil {
		accountInventory, accountInventoryExists, err = u.accountInventory.Load(ctx, accountID)
		if err != nil {
			return err
		}
	}
	questRecord, questExists, err := u.quests.Load(ctx, characterID)
	if err != nil {
		return err
	}
	inventory, inventoryExists, err := u.inventory.Load(ctx, characterID)
	if err != nil {
		return err
	}
	equipment, equipmentExists, err := u.equipment.Load(ctx, characterID)
	if err != nil {
		return err
	}
	skills, skillExists, err := u.skills.Load(ctx, characterID)
	if err != nil {
		return err
	}

	characterTx := &memoryCharacterTransaction{
		characterID: characterID,
		record:      repository.CloneCharacter(character),
		exists:      characterExists,
	}
	questTx := &memoryQuestTransaction{
		characterID: characterID,
		record:      repository.CloneQuest(questRecord),
		exists:      questExists,
	}
	inventoryTx := &memoryInventoryTransaction{
		characterID: characterID,
		record:      repository.CloneInventory(inventory),
		exists:      inventoryExists,
	}
	equipmentTx := &memoryEquipmentTransaction{
		characterID: characterID,
		record:      repository.CloneEquipment(equipment),
		exists:      equipmentExists,
	}
	skillTx := &memorySkillTransaction{
		characterID: characterID,
		record:      repository.CloneSkill(skills),
		exists:      skillExists,
	}
	accountInventoryTx := &memoryAccountInventoryTransaction{
		accountID: accountID,
		record:    repository.CloneAccountInventory(accountInventory),
		exists:    accountInventoryExists,
	}
	var accountTx *memoryAccountTransaction
	if u.account != nil {
		accountTx = &memoryAccountTransaction{
			accountID: accountID,
			record:    repository.CloneAccount(account),
			exists:    accountExists,
		}
	}

	var comboSettingsTx *memorySettlementSettingsTransaction
	guildSettingsTx := map[string]*memorySettingsTransaction{}
	settingsBefore := map[string]repository.SettingsRecord{}
	settingsExisted := map[string]bool{}
	if owner != nil {
		if u.settings == nil {
			return repository.ErrGuildTransaction
		}
		for _, scope := range []string{repository.GuildRegistryScope, receiptScope} {
			record, found, err := u.settings.Load(ctx, scope)
			if err != nil {
				return err
			}
			settingsBefore[scope], settingsExisted[scope] = record, found
			guildSettingsTx[scope] = &memorySettingsTransaction{scope: scope, record: repository.CloneSettings(record), exists: found}
		}
	} else if u.settings != nil {
		comboSettingsTx = &memorySettlementSettingsTransaction{
			characterID: characterID, base: u.settings,
			records: make(map[string]repository.SettingsRecord), exists: make(map[string]bool),
			original: make(map[string]repository.SettingsRecord), originalExists: make(map[string]bool), dirty: make(map[string]bool),
		}
	}
	txGroup := repository.Group{
		Account:          accountTx,
		AccountInventory: accountInventoryTx,
		Character:        characterTx,
		Quest:            questTx,
		Inventory:        inventoryTx,
		Equipment:        equipmentTx,
		Skill:            skillTx,
	}
	if owner != nil {
		txGroup.Settings = memorySettlementSettings(guildSettingsTx)
	} else if comboSettingsTx != nil {
		txGroup.Settings = comboSettingsTx
	}
	if err := apply(txGroup); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Register rollback before each write: repositories may fail after writing.
	var undo []func() error
	committed := false
	defer func() {
		if !committed {
			for i := len(undo) - 1; i >= 0; i-- {
				result = errors.Join(result, undo[i]())
			}
		}
	}()
	writes := []struct {
		dirty         bool
		save, restore func() error
	}{
		{characterTx.dirty, func() error { return u.character.Save(ctx, characterTx.record) }, func() error {
			return restoreCharacterRecord(u.character, characterID, character, characterExists, true)
		}},
		{questTx.dirty, func() error { return u.quests.Save(ctx, questTx.record) }, func() error { return restoreQuestRecordIfDirty(u.quests, characterID, questRecord, questExists, true) }},
		{skillTx.dirty, func() error { return u.skills.Save(ctx, skillTx.record) }, func() error { return restoreSkillRecord(u.skills, characterID, skills, skillExists, true) }},
		{inventoryTx.dirty, func() error { return u.inventory.Save(ctx, inventoryTx.record) }, func() error {
			return restoreInventoryRecordIfDirty(u.inventory, characterID, inventory, inventoryExists, true)
		}},
		{equipmentTx.dirty, func() error { return u.equipment.Save(ctx, equipmentTx.record) }, func() error {
			return restoreEquipmentRecordIfDirty(u.equipment, characterID, equipment, equipmentExists, true)
		}},
		{accountInventoryTx.dirty, func() error {
			if u.accountInventory == nil {
				return repository.ErrCharacterSettlementTransactionUnavailable
			}
			return u.accountInventory.Save(ctx, accountInventoryTx.record)
		}, func() error {
			return restoreAccountInventoryRecordIfDirty(u.accountInventory, accountID, accountInventory, accountInventoryExists, u.accountInventory != nil)
		}},
	}
	if accountTx != nil {
		writes = append(writes, struct {
			dirty         bool
			save, restore func() error
		}{accountTx.dirty, func() error { return u.account.Save(ctx, accountTx.record) }, func() error { return restoreAccountRecord(u.account, accountID, account, accountExists, true) }})
	}
	for _, w := range writes {
		if !w.dirty {
			continue
		}
		undo = append(undo, w.restore)
		if err := w.save(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	// The shared aggregate mutex remains held until these durable records commit.
	if owner != nil {
		for _, scope := range []string{repository.GuildRegistryScope, receiptScope} {
			tx := guildSettingsTx[scope]
			if tx == nil || !tx.dirty {
				continue
			}
			undo = append(undo, func() error {
				return restoreSettingsRecord(u.settings, scope, settingsBefore[scope], settingsExisted[scope], true)
			})
			if err := u.settings.Save(ctx, tx.record); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
		}
	} else if comboSettingsTx != nil && comboSettingsTx.dirtyCount() > 0 {
		if err := comboSettingsTx.commit(ctx); err != nil {
			return err
		}
	}
	committed = true
	return nil
}

type memoryQuestTransaction struct {
	characterID string
	record      repository.QuestRecord
	exists      bool
	dirty       bool
}

func (tx *memoryQuestTransaction) Load(ctx context.Context, characterID string) (repository.QuestRecord, bool, error) {
	if err := repository.TransactionKeyError(ctx, tx.characterID, characterID); err != nil {
		return repository.QuestRecord{}, false, err
	}
	return repository.CloneQuest(tx.record), tx.exists, nil
}

func (tx *memoryQuestTransaction) Save(ctx context.Context, record repository.QuestRecord) error {
	if err := repository.TransactionKeyError(ctx, tx.characterID, record.CharacterID); err != nil {
		return err
	}
	tx.record = repository.CloneQuest(record)
	tx.exists = true
	tx.dirty = true
	return nil
}

func restoreQuestRecordIfDirty(
	repo repository.QuestRepository,
	characterID string,
	record repository.QuestRecord,
	existed bool,
	dirty bool,
) error {
	if !dirty {
		return nil
	}
	if existed {
		return repo.Save(context.Background(), record)
	}
	deleter, ok := repo.(interface {
		Delete(context.Context, string) error
	})
	if !ok {
		return errors.New("quest rollback cannot remove newly created record")
	}
	return deleter.Delete(context.Background(), characterID)
}

// Only the two explicitly loaded settings records are accessible.
type memorySettlementSettings map[string]*memorySettingsTransaction

func (s memorySettlementSettings) Load(ctx context.Context, scope string) (repository.SettingsRecord, bool, error) {
	if tx := s[scope]; tx != nil {
		return tx.Load(ctx, scope)
	}
	return repository.SettingsRecord{}, false, repository.ErrGuildTransaction
}
func (s memorySettlementSettings) Save(ctx context.Context, record repository.SettingsRecord) error {
	if tx := s[record.Scope]; tx != nil {
		return tx.Save(ctx, record)
	}
	return repository.ErrGuildTransaction
}
