package repository

import (
	"context"
	"errors"
	"sort"
	"strings"
)

var ErrSemesterClearTransaction = errors.New("semester clear transaction owner is unavailable")
var ErrSemesterClearCommitUnknown = errors.New("semester clear SQL commit outcome is unknown")

type SemesterClearOwner struct {
	AccountID   string `json:"account_id"`
	CharacterID string `json:"character_id"`
}
type SemesterClearUnitOfWork interface {
	WithinSemesterClear(context.Context, SemesterClearOwner, []SemesterClearOwner, string, func(Group) error) error
}

// SemesterClearOwners fixes the exact, bounded owner set and deterministic SQL
// row-lock order. The source must be one of the actual participants.
func SemesterClearOwners(source SemesterClearOwner, owners []SemesterClearOwner, scope string) ([]SemesterClearOwner, error) {
	if source.AccountID == "" || source.CharacterID == "" || strings.TrimSpace(scope) == "" || len(owners) < 1 || len(owners) > 4 {
		return nil, ErrSemesterClearTransaction
	}
	result := append([]SemesterClearOwner(nil), owners...)
	seen := map[string]bool{}
	found := false
	for _, owner := range result {
		if owner.AccountID == "" || owner.CharacterID == "" || owner.AccountID != strings.TrimSpace(owner.AccountID) || owner.CharacterID != strings.TrimSpace(owner.CharacterID) || seen[owner.CharacterID] {
			return nil, ErrSemesterClearTransaction
		}
		seen[owner.CharacterID] = true
		if owner == source {
			found = true
		}
	}
	if !found {
		return nil, ErrSemesterClearTransaction
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CharacterID < result[j].CharacterID })
	return result, nil
}

// SemesterClearScopedGroup intentionally exposes only bounded characters,
// source quests, and one exact receipt scope. No general repository is leaked.
func SemesterClearScopedGroup(base Group, source SemesterClearOwner, owners []SemesterClearOwner, scope string) Group {
	allowed := map[string]string{}
	for _, owner := range owners {
		allowed[owner.CharacterID] = owner.AccountID
	}
	return Group{Character: &semesterCharacters{base: base.Character, allowed: allowed}, Quest: &semesterQuests{base: base.Quest, id: source.CharacterID}, Settings: &semesterSettings{base: base.Settings, scope: scope}}
}

type semesterCharacters struct {
	base    CharacterRepository
	allowed map[string]string
}

func (r *semesterCharacters) Load(ctx context.Context, id string) (CharacterRecord, bool, error) {
	account, ok := r.allowed[id]
	if !ok {
		return CharacterRecord{}, false, ErrSemesterClearTransaction
	}
	record, found, err := r.base.Load(ctx, id)
	if err == nil && (!found || record.CharacterID != id || record.AccountID != account) {
		return CharacterRecord{}, false, ErrSemesterClearTransaction
	}
	return record, found, err
}
func (r *semesterCharacters) Save(ctx context.Context, record CharacterRecord) error {
	if account, ok := r.allowed[record.CharacterID]; !ok || account != record.AccountID {
		return ErrSemesterClearTransaction
	}
	return r.base.Save(ctx, record)
}
func (*semesterCharacters) ListByAccount(context.Context, string, int) ([]CharacterRecord, error) {
	return nil, ErrSemesterClearTransaction
}
func (*semesterCharacters) FindIDByName(context.Context, string) (string, bool, error) {
	return "", false, ErrSemesterClearTransaction
}
func (*semesterCharacters) NextNumericID(context.Context) (int, error) {
	return 0, ErrSemesterClearTransaction
}

type semesterQuests struct {
	base QuestRepository
	id   string
}

func (r *semesterQuests) Load(ctx context.Context, id string) (QuestRecord, bool, error) {
	if err := TransactionKeyError(ctx, r.id, id); err != nil {
		return QuestRecord{}, false, err
	}
	rec, found, err := r.base.Load(ctx, id)
	if err == nil && found && rec.CharacterID != id {
		return QuestRecord{}, false, ErrSemesterClearTransaction
	}
	return rec, found, err
}
func (r *semesterQuests) Save(ctx context.Context, rec QuestRecord) error {
	if err := TransactionKeyError(ctx, r.id, rec.CharacterID); err != nil {
		return err
	}
	return r.base.Save(ctx, rec)
}

type semesterSettings struct {
	base  SettingsRepository
	scope string
}

func (r *semesterSettings) Load(ctx context.Context, scope string) (SettingsRecord, bool, error) {
	if err := TransactionKeyError(ctx, r.scope, scope); err != nil {
		return SettingsRecord{}, false, err
	}
	rec, found, err := r.base.Load(ctx, scope)
	if err == nil && found && rec.Scope != scope {
		return SettingsRecord{}, false, ErrSemesterClearTransaction
	}
	return rec, found, err
}
func (r *semesterSettings) Save(ctx context.Context, rec SettingsRecord) error {
	if err := TransactionKeyError(ctx, r.scope, rec.Scope); err != nil {
		return err
	}
	return r.base.Save(ctx, rec)
}
