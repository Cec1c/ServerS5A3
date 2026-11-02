package memory

import (
	"context"
	"reflect"
	"strings"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/db"
)

func (u *memoryGuildUnitOfWork) WithinGuildRead(ctx context.Context, apply func(r.GuildReadView) error) error {
	if u == nil || u.sharedMu == nil || apply == nil {
		return r.ErrGuildTransaction
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	settings, ok := u.settings.(*memorySettingsStore)
	characters, charsOK := u.character.(*memoryCharStore)
	if !ok || !charsOK {
		return r.ErrGuildTransaction
	}
	u.sharedMu.Lock()
	records := settings.Snapshot(nil)
	chars := characters.Snapshot(nil)
	u.sharedMu.Unlock()
	copySettings := &memorySettingsStore{db.NewMemoryStore(r.SettingsKey, r.CloneSettings)}
	for _, record := range records {
		if record.Scope != r.GuildRegistryScope && !strings.HasPrefix(record.Scope, "guild-clear:v1:") {
			continue
		}
		if err := copySettings.Save(ctx, record); err != nil {
			return err
		}
	}
	projection := memoryGuildCharacters{}
	for _, c := range chars {
		projection[c.CharacterID] = r.GuildCharacter{CharacterID: c.CharacterID, AccountID: c.AccountID, Name: c.Name, Job: c.Job, Level: c.Level, GrowType: c.Stats["grow_type"], DeleteFlag: c.Stats["delete_flag"]}
	}
	return apply(r.GuildReadView{Settings: r.ReadOnlyGuildSettings(copySettings), Characters: projection})
}

func (u *memoryGuildUnitOfWork) CompareGuildRegistry(ctx context.Context, before, after r.SettingsRecord) (bool, error) {
	if u == nil || before.Scope != r.GuildRegistryScope || after.Scope != before.Scope {
		return false, r.ErrGuildTransaction
	}
	u.sharedMu.Lock()
	defer u.sharedMu.Unlock()
	actual, found, err := u.settings.Load(ctx, r.GuildRegistryScope)
	if err != nil || !found {
		return false, err
	}
	if !actual.UpdatedAt.Equal(before.UpdatedAt) || !reflect.DeepEqual(actual.Values, before.Values) {
		return false, nil
	}
	if err := u.settings.Save(ctx, after); err != nil {
		return false, err
	}
	return true, nil
}

type memoryGuildCharacters map[string]r.GuildCharacter

func (s memoryGuildCharacters) LoadGuildCharacters(ctx context.Context, ids []string) (map[string]r.GuildCharacter, error) {
	if len(ids) > 1000 {
		return nil, r.ErrGuildTransaction
	}
	result := map[string]r.GuildCharacter{}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if id == "" || strings.TrimSpace(id) != id {
			return nil, r.ErrGuildTransaction
		}
		if c, found := s[id]; found {
			result[id] = c
		}
	}
	return result, nil
}
