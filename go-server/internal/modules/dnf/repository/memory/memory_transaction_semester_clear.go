package memory

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"sync"
)

type memorySemesterClearUnitOfWork struct {
	sharedMu  *sync.Mutex
	character repository.CharacterRepository
	quest     repository.QuestRepository
	settings  repository.SettingsRepository
}

func (u *memorySemesterClearUnitOfWork) WithinSemesterClear(ctx context.Context, source repository.SemesterClearOwner, owners []repository.SemesterClearOwner, scope string, apply func(repository.Group) error) (result error) {
	ordered, err := repository.SemesterClearOwners(source, owners, scope)
	if err != nil {
		return err
	}
	if u == nil || u.sharedMu == nil || u.character == nil || u.quest == nil || u.settings == nil || apply == nil {
		return repository.ErrSemesterClearTransaction
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
			return repository.ErrSemesterClearTransaction
		}
		chars.allowed[owner.CharacterID] = struct{}{}
		chars.records[owner.CharacterID] = repository.CloneCharacter(ch)
		chars.exists[owner.CharacterID] = true
		originals[owner.CharacterID] = repository.CloneCharacter(ch)
	}
	q, qFound, err := u.quest.Load(ctx, source.CharacterID)
	if err != nil {
		return err
	}
	receipt, rFound, err := u.settings.Load(ctx, scope)
	if err != nil {
		return err
	}
	quests := &memoryQuestTransaction{characterID: source.CharacterID, record: repository.CloneQuest(q), exists: qFound}
	settings := &memorySettingsTransaction{scope: scope, record: repository.CloneSettings(receipt), exists: rFound}
	base := repository.Group{Character: chars, Quest: quests, Settings: settings}
	if err = apply(repository.SemesterClearScopedGroup(base, source, ordered, scope)); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	touched := []string{}
	questTouched, receiptTouched := false, false
	committed := false
	restore := func() error {
		var failures []error
		if receiptTouched {
			failures = append(failures, restoreSettingsRecord(u.settings, scope, receipt, rFound, true))
		}
		if questTouched {
			failures = append(failures, restoreQuestRecordIfDirty(u.quest, source.CharacterID, q, qFound, true))
		}
		for i := len(touched) - 1; i >= 0; i-- {
			id := touched[i]
			failures = append(failures, restoreCharacterRecord(u.character, id, originals[id], true, true))
		}
		return errors.Join(failures...)
	}
	// Panic during the callback precedes physical writes. Panic during physical
	// stores still restores every attempted store and releases the aggregate lock.
	defer func() {
		if !committed {
			result = errors.Join(result, restore())
		}
	}()
	for _, owner := range ordered {
		if chars.dirty[owner.CharacterID] {
			touched = append(touched, owner.CharacterID)
			if err = u.character.Save(ctx, chars.records[owner.CharacterID]); err != nil {
				return err
			}
			if err = ctx.Err(); err != nil {
				return err
			}
		}
	}
	if quests.dirty {
		questTouched = true
		if err = u.quest.Save(ctx, quests.record); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	if settings.dirty {
		receiptTouched = true
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
