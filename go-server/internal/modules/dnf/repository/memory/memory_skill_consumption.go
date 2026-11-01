package memory

import (
	"context"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
)

func (u *memoryAccountCharacterAssetUnitOfWork) WithinAccountCharacterSkillAssets(ctx context.Context, account, character string, apply func(repository.AccountInventoryRepository, repository.CharacterRepository, repository.SkillRepository) error) error {
	if u == nil || u.skills == nil || u.accountInventory == nil || u.character == nil || apply == nil || strings.TrimSpace(account) == "" || strings.TrimSpace(character) == "" {
		return repository.ErrAccountCharacterAssetTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lock := &u.mu
	if u.sharedMu != nil {
		lock = u.sharedMu
	}
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	a, af, err := u.accountInventory.Load(ctx, account)
	if err != nil {
		return err
	}
	c, cf, err := u.character.Load(ctx, character)
	if err != nil {
		return err
	}
	s, sf, err := u.skills.Load(ctx, character)
	if err != nil {
		return err
	}
	at := &memoryAccountInventoryTransaction{accountID: account, record: repository.CloneAccountInventory(a), exists: af}
	ct := &memoryCharacterTransaction{characterID: character, record: repository.CloneCharacter(c), exists: cf}
	st := &memorySkillTransaction{characterID: character, record: repository.CloneSkill(s), exists: sf}
	var activity *memoryActivityTransaction
	if u.activity != nil && cf {
		activity = u.activity.transaction()
		ct.activity = repository.ScopeCharacterActivity(activity, c)
	}
	if err := apply(at, ct, st); err != nil {
		return err
	}
	err = commitMemoryLedgerAssets(ctx,
		memoryLedgerWrite{dirty: at.dirty, save: func() error { return u.accountInventory.Save(ctx, at.record) }, restore: func() error { return restoreAccountInventoryRecord(u.accountInventory, account, a, af) }},
		memoryLedgerWrite{dirty: ct.dirty, save: func() error { return u.character.Save(ctx, ct.record) }, restore: func() error { return restoreCharacterRecord(u.character, character, c, cf, true) }},
		memoryLedgerWrite{dirty: st.dirty, save: func() error { return u.skills.Save(ctx, st.record) }, restore: func() error { return restoreSkillRecord(u.skills, character, s, sf, true) }})
	if err == nil && activity != nil {
		activity.commit()
	}
	return err
}
