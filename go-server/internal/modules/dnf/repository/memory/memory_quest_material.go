package memory

import (
	"context"
	"errors"
	r "longheng.io/server/internal/modules/dnf/repository"
	"sort"
	"sync"
)

type memoryQuestMaterialPreparation struct {
	mu       *sync.Mutex
	quests   r.QuestRepository
	settings r.SettingsRepository
}

func (u *memoryQuestMaterialPreparation) WithinQuestMaterialPreparation(ctx context.Context, ids []string, apply func(r.Group) error) error {
	if apply == nil || len(ids) == 0 || len(ids) > 4 {
		return r.ErrCharacterSettlementTransactionUnavailable
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	qs := &materialQuestBuffer{base: u.quests, records: map[string]r.QuestRecord{}, before: map[string]r.QuestRecord{}, existed: map[string]bool{}, dirty: map[string]bool{}}
	ss := &materialSettingsBuffer{base: u.settings, records: map[string]r.SettingsRecord{}, before: map[string]r.SettingsRecord{}, existed: map[string]bool{}, dirty: map[string]bool{}}
	if err := apply(r.QuestMaterialPreparationGroup(r.Group{Quest: qs, Settings: ss}, ids)); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var undo []func() error
	fail := func(err error) error {
		for i := len(undo) - 1; i >= 0; i-- {
			err = errors.Join(err, undo[i]())
		}
		return err
	}
	keys := []string{}
	for k := range qs.dirty {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		k := key
		undo = append(undo, func() error { return restoreQuestRecordIfDirty(u.quests, k, qs.before[k], qs.existed[k], true) })
		if err := u.quests.Save(ctx, qs.records[k]); err != nil {
			return fail(err)
		}
	}
	keys = nil
	for k := range ss.dirty {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		k := key
		undo = append(undo, func() error { return restoreSettingsRecord(u.settings, k, ss.before[k], ss.existed[k], true) })
		if err := u.settings.Save(ctx, ss.records[k]); err != nil {
			return fail(err)
		}
	}
	return nil
}

type materialQuestBuffer struct {
	base            r.QuestRepository
	records, before map[string]r.QuestRecord
	existed, dirty  map[string]bool
}

func (b *materialQuestBuffer) Load(ctx context.Context, k string) (r.QuestRecord, bool, error) {
	if q, ok := b.records[k]; ok {
		return r.CloneQuest(q), b.existed[k], nil
	}
	q, ok, e := b.base.Load(ctx, k)
	if e == nil {
		b.records[k] = r.CloneQuest(q)
		b.before[k] = r.CloneQuest(q)
		b.existed[k] = ok
	}
	return r.CloneQuest(q), ok, e
}
func (b *materialQuestBuffer) Save(ctx context.Context, q r.QuestRecord) error {
	if _, ok := b.records[q.CharacterID]; !ok {
		if _, _, e := b.Load(ctx, q.CharacterID); e != nil {
			return e
		}
	}
	b.records[q.CharacterID] = r.CloneQuest(q)
	b.dirty[q.CharacterID] = true
	return nil
}

type materialSettingsBuffer struct {
	base            r.SettingsRepository
	records, before map[string]r.SettingsRecord
	existed, dirty  map[string]bool
}

func (b *materialSettingsBuffer) Load(ctx context.Context, k string) (r.SettingsRecord, bool, error) {
	if q, ok := b.records[k]; ok {
		return r.CloneSettings(q), b.existed[k], nil
	}
	q, ok, e := b.base.Load(ctx, k)
	if e == nil {
		b.records[k] = r.CloneSettings(q)
		b.before[k] = r.CloneSettings(q)
		b.existed[k] = ok
	}
	return r.CloneSettings(q), ok, e
}
func (b *materialSettingsBuffer) Save(ctx context.Context, q r.SettingsRecord) error {
	if _, ok := b.records[q.Scope]; !ok {
		if _, _, e := b.Load(ctx, q.Scope); e != nil {
			return e
		}
	}
	b.records[q.Scope] = r.CloneSettings(q)
	b.dirty[q.Scope] = true
	return nil
}
