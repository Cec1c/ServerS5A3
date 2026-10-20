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
	return u.withinGuild(ctx, owners, false, apply)
}

func (u *memoryGuildUnitOfWork) WithinGuildInventory(ctx context.Context, owner repository.GuildOwner, apply func(repository.Group) error) error {
	return u.withinGuild(ctx, []repository.GuildOwner{owner}, true, apply)
}

func (u *memoryGuildUnitOfWork) withinGuild(ctx context.Context, owners []repository.GuildOwner, withInventory bool, apply func(repository.Group) error) (result error) {
	ordered, err := repository.GuildOwners(owners)
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
	group := repository.GuildScopedGroup(repository.Group{Character: chars, Settings: settings}, ordered)
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
	settingsTouched, inventoryTouched, committed := false, false, false
	defer func() {
		if committed {
			return
		}
		var failures []error
		if settingsTouched {
			failures = append(failures, restoreSettingsRecord(u.settings, repository.GuildRegistryScope, before, found, true))
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
	if settings.dirty {
		settingsTouched = true
		if err = u.settings.Save(ctx, settings.record); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	committed = true
	return nil
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
