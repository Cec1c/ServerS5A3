package memory

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"sync"
)

type memoryGuildUnitOfWork struct {
	sharedMu  *sync.Mutex
	character repository.CharacterRepository
	settings  repository.SettingsRepository
	inventory repository.InventoryRepository
}

func (u *memoryGuildUnitOfWork) WithinGuild(ctx context.Context, owners []repository.GuildOwner, apply func(repository.Group) error) error {
	return u.withinGuild(ctx, owners, false, false, false, "", apply)
}

func (u *memoryGuildUnitOfWork) WithinGuildTravel(ctx context.Context, owners []repository.GuildOwner, apply func(repository.Group) error) error {
	return u.withinGuild(ctx, owners, false, true, false, "", apply)
}

func (u *memoryGuildUnitOfWork) WithinGuildDungeonEntry(ctx context.Context, owners []repository.GuildOwner, receiptScope string, apply func(repository.Group) error) error {
	if !repository.ValidGuildDungeonEntryReceiptScope(receiptScope) {
		return repository.ErrGuildTransaction
	}
	return u.withinGuild(ctx, owners, false, false, false, receiptScope, apply)
}

func (u *memoryGuildUnitOfWork) WithinGuildInventory(ctx context.Context, owner repository.GuildOwner, apply func(repository.Group) error) error {
	return u.withinGuild(ctx, []repository.GuildOwner{owner}, true, false, false, "", apply)
}

func (u *memoryGuildUnitOfWork) withinGuild(ctx context.Context, owners []repository.GuildOwner, withInventory, travel, raidClear bool, receiptScope string, apply func(repository.Group) error) (result error) {
	var ordered []repository.GuildOwner
	var err error
	switch {
	case raidClear:
		ordered, err = repository.GuildRaidClearOwners(owners)
	case receiptScope != "":
		ordered, err = repository.GuildDungeonEntryOwners(owners)
	case travel:
		ordered, err = repository.GuildTravelOwners(owners)
	default:
		ordered, err = repository.GuildOwners(owners)
	}
	if err != nil {
		return err
	}
	if u == nil || u.sharedMu == nil || u.character == nil || u.settings == nil || apply == nil {
		return repository.ErrGuildTransaction
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	u.sharedMu.Lock()
	defer u.sharedMu.Unlock()
	chars := &memoryCharacterTradeCharacterTransaction{allowed: map[string]struct{}{}, records: map[string]repository.CharacterRecord{}, exists: map[string]bool{}, dirty: map[string]bool{}}
	originals := map[string]repository.CharacterRecord{}
	for _, owner := range ordered {
		ch, found, e := u.character.Load(ctx, owner.CharacterID)
		if e != nil {
			return e
		}
		if !found || ch.CharacterID != owner.CharacterID || ch.AccountID != owner.AccountID {
			return repository.ErrGuildTransaction
		}
		chars.allowed[ch.CharacterID] = struct{}{}
		chars.records[ch.CharacterID] = repository.CloneCharacter(ch)
		chars.exists[ch.CharacterID] = true
		originals[ch.CharacterID] = repository.CloneCharacter(ch)
	}
	before, found, err := u.settings.Load(ctx, repository.GuildRegistryScope)
	if err != nil {
		return err
	}
	settings := &memorySettingsTransaction{scope: repository.GuildRegistryScope, record: repository.CloneSettings(before), exists: found}
	settingsTransactions := map[string]*memorySettingsTransaction{repository.GuildRegistryScope: settings}
	settingsBefore := map[string]repository.SettingsRecord{repository.GuildRegistryScope: before}
	settingsFound := map[string]bool{repository.GuildRegistryScope: found}
	if receiptScope != "" {
		receipt, receiptFound, loadErr := u.settings.Load(ctx, receiptScope)
		if loadErr != nil {
			return loadErr
		}
		settingsTransactions[receiptScope] = &memorySettingsTransaction{
			scope: receiptScope, record: repository.CloneSettings(receipt), exists: receiptFound,
		}
		settingsBefore[receiptScope] = receipt
		settingsFound[receiptScope] = receiptFound
	}
	readSettings := &memoryGuildReceiptSettings{SettingsRepository: settings, reader: u.settings}
	group := repository.GuildScopedGroup(repository.Group{Character: chars, Settings: readSettings}, ordered)
	if receiptScope != "" {
		group.Settings = repository.GuildDungeonEntrySettings(memoryGuildDungeonEntrySettings(settingsTransactions), receiptScope)
	}
	var inventoryBefore repository.InventoryRecord
	var inventoryFound bool
	var inventory *memoryInventoryTransaction
	if withInventory {
		if u.inventory == nil {
			return repository.ErrGuildTransaction
		}
		inventoryBefore, inventoryFound, err = u.inventory.Load(ctx, ordered[0].CharacterID)
		if err != nil {
			return err
		}
		inventory = &memoryInventoryTransaction{characterID: ordered[0].CharacterID, record: repository.CloneInventory(inventoryBefore), exists: inventoryFound}
		containerScope := repository.CharacterContainerStateScope(ordered[0].CharacterID)
		container, exists, loadErr := u.settings.Load(ctx, containerScope)
		if loadErr != nil {
			return loadErr
		}
		readSettings := &memoryGuildInventorySettings{SettingsRepository: settings, scope: containerScope, record: repository.CloneSettings(container), found: exists}
		group = repository.GuildInventoryScopedGroup(repository.Group{Character: chars, Settings: readSettings, Inventory: inventory}, ordered[0])
	}
	if err = apply(group); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	touched := []string{}
	settingsTouched := []string{}
	inventoryTouched, committed := false, false
	defer func() {
		if committed {
			return
		}
		var failures []error
		for i := len(settingsTouched) - 1; i >= 0; i-- {
			scope := settingsTouched[i]
			failures = append(failures, restoreSettingsRecord(u.settings, scope, settingsBefore[scope], settingsFound[scope], true))
		}
		if inventoryTouched {
			failures = append(failures, restoreInventoryRecord(u.inventory, ordered[0].CharacterID, inventoryBefore, inventoryFound))
		}
		for i := len(touched) - 1; i >= 0; i-- {
			id := touched[i]
			failures = append(failures, restoreCharacterRecord(u.character, id, originals[id], true, true))
		}
		result = errors.Join(result, errors.Join(failures...))
	}()
	for _, owner := range ordered {
		id := owner.CharacterID
		if chars.dirty[id] {
			touched = append(touched, id)
			if err = u.character.Save(ctx, chars.records[id]); err != nil {
				return err
			}
			if err = ctx.Err(); err != nil {
				return err
			}
		}
	}
	if inventory != nil && inventory.dirty {
		inventoryTouched = true
		if err = u.inventory.Save(ctx, inventory.record); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	for _, scope := range []string{repository.GuildRegistryScope, receiptScope} {
		tx := settingsTransactions[scope]
		if tx == nil || !tx.dirty {
			continue
		}
		settingsTouched = append(settingsTouched, scope)
		if err = u.settings.Save(ctx, tx.record); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	committed = true
	return nil
}

type memoryGuildDungeonEntrySettings map[string]*memorySettingsTransaction

func (s memoryGuildDungeonEntrySettings) Load(ctx context.Context, scope string) (repository.SettingsRecord, bool, error) {
	if tx := s[scope]; tx != nil {
		return tx.Load(ctx, scope)
	}
	return repository.SettingsRecord{}, false, repository.ErrGuildTransaction
}

func (s memoryGuildDungeonEntrySettings) Save(ctx context.Context, record repository.SettingsRecord) error {
	if tx := s[record.Scope]; tx != nil {
		return tx.Save(ctx, record)
	}
	return repository.ErrGuildTransaction
}

// Capacity is a read-only snapshot under the same owner lock. Settings writes
// still delegate to the registry-only transaction; other owners remain hidden.
type memoryGuildInventorySettings struct {
	repository.SettingsRepository
	scope  string
	record repository.SettingsRecord
	found  bool
}

func (s *memoryGuildInventorySettings) Load(ctx context.Context, scope string) (repository.SettingsRecord, bool, error) {
	if scope == s.scope {
		return repository.CloneSettings(s.record), s.found, nil
	}
	return s.SettingsRepository.Load(ctx, scope)
}

func (u *memoryGuildUnitOfWork) WithinGuildRaidClear(ctx context.Context, owners []repository.GuildOwner, apply func(repository.Group) error) error {
	return u.withinGuild(ctx, owners, false, false, true, "", apply)
}
