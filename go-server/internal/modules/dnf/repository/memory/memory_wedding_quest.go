package memory

import (
	"context"
	"errors"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func (u *memoryCharacterTradeUnitOfWork) WithinWeddingQuests(ctx context.Context, ids []string, apply func(r.CharacterRepository, r.QuestRepository) error) (err error) {
	if _, scopeErr := r.WeddingPairScope(ids); scopeErr != nil || u == nil || u.character == nil || u.quests == nil || apply == nil {
		return r.ErrCharacterTradeTransactionUnavailable
	}
	ids, _ = r.CharacterInventoryGroupIDs(ids)
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
	allowed := map[string]struct{}{}
	chars := &memoryCharacterTradeCharacterTransaction{allowed: allowed, records: map[string]r.CharacterRecord{}, exists: map[string]bool{}, dirty: map[string]bool{}}
	quests := &materialQuestBuffer{base: u.quests, records: map[string]r.QuestRecord{}, before: map[string]r.QuestRecord{}, existed: map[string]bool{}, dirty: map[string]bool{}}
	before := map[string]r.CharacterRecord{}
	existed := map[string]bool{}
	for _, id := range ids {
		allowed[id] = struct{}{}
		c, found, e := u.character.Load(ctx, id)
		if e != nil {
			return e
		}
		before[id], existed[id] = r.CloneCharacter(c), found
		chars.records[id], chars.exists[id] = r.CloneCharacter(c), found
		if _, _, e = quests.Load(ctx, id); e != nil {
			return e
		}
	}
	if err = apply(chars, r.ScopeWeddingQuests(quests, ids)); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	var undo []func() error
	rollback := func(cause error) error {
		for i := len(undo) - 1; i >= 0; i-- {
			cause = errors.Join(cause, undo[i]())
		}
		return cause
	}
	defer func() {
		if p := recover(); p != nil {
			_ = rollback(nil)
			panic(p)
		}
	}()
	for _, id := range ids {
		id := id
		if chars.dirty[id] {
			undo = append(undo, func() error { return restoreCharacterRecord(u.character, id, before[id], existed[id], true) })
			if e := chars.commit(ctx, u.character, id); e != nil {
				return rollback(e)
			}
		}
		if quests.dirty[id] {
			undo = append(undo, func() error {
				return restoreQuestRecordIfDirty(u.quests, id, quests.before[id], quests.existed[id], true)
			})
			if e := u.quests.Save(ctx, quests.records[id]); e != nil {
				return rollback(e)
			}
		}
	}
	return nil
}
