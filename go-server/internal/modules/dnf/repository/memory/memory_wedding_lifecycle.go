package memory

import (
	"context"
	"errors"
	r "longheng.io/server/internal/modules/dnf/repository"
)

var _ r.WeddingLifecycleUnitOfWork = (*memoryCharacterTradeUnitOfWork)(nil)
var _ r.WeddingGuestLifecycleUnitOfWork = (*memoryCharacterTradeUnitOfWork)(nil)

func (u *memoryCharacterTradeUnitOfWork) WithinWeddingLifecycle(ctx context.Context, ids []string, scope string, apply func(r.Group) error) error {
	return u.withinWeddingLifecycle(ctx, ids, nil, scope, false, apply)
}

func (u *memoryCharacterTradeUnitOfWork) WithinWeddingGuestLifecycle(ctx context.Context, hosts, guests []string, scope string, apply func(r.Group) error) (err error) {
	return u.withinWeddingLifecycle(ctx, hosts, guests, scope, true, apply)
}

func (u *memoryCharacterTradeUnitOfWork) withinWeddingLifecycle(ctx context.Context, hosts, guests []string, scope string, index bool, apply func(r.Group) error) (err error) {
	ids, err := r.WeddingLifecycleIDs(hosts, guests, scope)
	if err != nil || u == nil || apply == nil || u.character == nil || u.inventory == nil || u.settings == nil || u.quests == nil || u.mailbox == nil {
		return r.ErrCharacterTradeTransactionUnavailable
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
	allowed := map[string]struct{}{}
	chars := &memoryCharacterTradeCharacterTransaction{allowed: allowed, records: map[string]r.CharacterRecord{}, exists: map[string]bool{}, dirty: map[string]bool{}}
	inv := &memoryCharacterTradeInventoryTransaction{allowed: allowed, records: map[string]r.InventoryRecord{}, exists: map[string]bool{}, dirty: map[string]bool{}}
	quests := &materialQuestBuffer{base: u.quests, records: map[string]r.QuestRecord{}, before: map[string]r.QuestRecord{}, existed: map[string]bool{}, dirty: map[string]bool{}}
	mails := &memoryWeddingMailboxes{records: map[string]*memoryMailboxTransaction{}}
	beforeC := map[string]r.CharacterRecord{}
	beforeI := map[string]r.InventoryRecord{}
	beforeM := map[string]r.MailboxRecord{}
	existsI, existsM := map[string]bool{}, map[string]bool{}
	for _, id := range ids {
		allowed[id] = struct{}{}
		c, found, e := u.character.Load(ctx, id)
		if e != nil {
			return e
		}
		if !found || c.CharacterID != id {
			return r.ErrCharacterTradeTransactionUnavailable
		}
		chars.records[id], chars.exists[id], beforeC[id] = r.CloneCharacter(c), true, r.CloneCharacter(c)
		i, found, e := u.inventory.Load(ctx, id)
		if e != nil {
			return e
		}
		inv.records[id], inv.exists[id], beforeI[id], existsI[id] = r.CloneInventory(i), found, r.CloneInventory(i), found
		if _, _, e = quests.Load(ctx, id); e != nil {
			return e
		}
		m, found, e := u.mailbox.Load(ctx, id)
		if e != nil {
			return e
		}
		mails.records[id] = &memoryMailboxTransaction{characterID: id, record: r.CloneMailbox(m), exists: found}
		beforeM[id], existsM[id] = r.CloneMailbox(m), found
	}
	scopes := []string{scope}
	var extra []string
	if index {
		scopes = append([]string{r.WeddingGuestIndexScope}, scopes...)
		extra = []string{r.WeddingGuestIndexScope}
	}
	beforeS := map[string]r.SettingsRecord{}
	existsS := map[string]bool{}
	settings := memorySettlementSettings{}
	for _, key := range scopes {
		record, found, e := u.settings.Load(ctx, key)
		if e != nil {
			return e
		}
		beforeS[key], existsS[key] = r.CloneSettings(record), found
		settings[key] = &memorySettingsTransaction{scope: key, record: r.CloneSettings(record), exists: found}
	}
	group := r.Group{Character: chars, Inventory: inv, Quest: r.ScopeWeddingQuests(quests, ids), Settings: r.ScopeWeddingLifecycleSettings(settings, u.settings, scope, ids, extra...), Mailbox: r.ScopeWeddingMailboxes(mails, ids)}
	if err = apply(group); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	var undo []func() error
	committed := false
	defer func() {
		if committed {
			return
		}
		for i := len(undo) - 1; i >= 0; i-- {
			err = errors.Join(err, restoreMailboxAssetAttempt(undo[i]))
		}
	}()
	for _, id := range ids {
		if chars.dirty[id] {
			undo = append(undo, func() error { return restoreCharacterRecord(u.character, id, beforeC[id], true, true) })
			if err = chars.commit(ctx, u.character, id); err != nil {
				return err
			}
		}
		if inv.dirty[id] {
			undo = append(undo, func() error { return restoreInventoryRecordIfDirty(u.inventory, id, beforeI[id], existsI[id], true) })
			if err = u.inventory.Save(ctx, inv.records[id]); err != nil {
				return err
			}
		}
		if quests.dirty[id] {
			undo = append(undo, func() error {
				return restoreQuestRecordIfDirty(u.quests, id, quests.before[id], quests.existed[id], true)
			})
			if err = u.quests.Save(ctx, quests.records[id]); err != nil {
				return err
			}
		}
		if mails.records[id].dirty {
			undo = append(undo, func() error { return restoreMailboxRecordIfDirty(u.mailbox, id, beforeM[id], existsM[id], true) })
			if err = u.mailbox.Save(ctx, mails.records[id].record); err != nil {
				return err
			}
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	for _, key := range scopes {
		tx := settings[key]
		if !tx.dirty {
			continue
		}
		undo = append(undo, func() error { return restoreSettingsRecord(u.settings, key, beforeS[key], existsS[key], true) })
		if err = u.settings.Save(ctx, tx.record); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	committed = true
	return nil
}

type memoryWeddingMailboxes struct {
	records map[string]*memoryMailboxTransaction
}

func (m *memoryWeddingMailboxes) Load(ctx context.Context, id string) (r.MailboxRecord, bool, error) {
	if tx := m.records[id]; tx != nil {
		return tx.Load(ctx, id)
	}
	return r.MailboxRecord{}, false, r.ErrCharacterTradeTransactionUnavailable
}
func (m *memoryWeddingMailboxes) Save(ctx context.Context, record r.MailboxRecord) error {
	if tx := m.records[record.CharacterID]; tx != nil {
		return tx.Save(ctx, record)
	}
	return r.ErrCharacterTradeTransactionUnavailable
}
func (m *memoryWeddingMailboxes) SaveFields(ctx context.Context, record r.MailboxRecord, fields ...r.MailboxField) error {
	if tx := m.records[record.CharacterID]; tx != nil {
		return tx.SaveFields(ctx, record, fields...)
	}
	return r.ErrCharacterTradeTransactionUnavailable
}
