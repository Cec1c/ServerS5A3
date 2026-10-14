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
}

func (u *memoryGuildUnitOfWork) WithinGuild(ctx context.Context, owners []repository.GuildOwner, apply func(repository.Group) error) (result error) {
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
	if err = apply(group); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	touched := []string{}
	settingsTouched, committed := false, false
	defer func() {
		if committed {
			return
		}
		var failures []error
		if settingsTouched {
			failures = append(failures, restoreSettingsRecord(u.settings, repository.GuildRegistryScope, before, found, true))
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
