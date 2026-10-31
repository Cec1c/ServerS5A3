package dungeonpermission

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
)

const accountOwnerKeyPrefix = "account-v1:"

var (
	ErrAccountRequired       = errors.New("副本难度共享缺少账号")
	ErrRepositoryUnavailable = errors.New("副本难度仓储不可用")
)

// Snapshot is the account-wide maximum difficulty state. StorageKey is an
// opaque repository owner key; callers must never substitute a character ID.
type Snapshot struct {
	AccountID       string
	StorageKey      string
	Record          repository.DungeonPermissionRecord
	LegacyOwners    int
	MigratedEntries int
}

// Owner owns the database-neutral account aggregation. MySQL and SQLite both
// expose the same repository group, so migration and maximum-state semantics
// cannot diverge between storage modes.
type Owner struct {
	repositories repository.Group
}

func NewOwner(repositories repository.Group) (*Owner, error) {
	if repositories.Character == nil || repositories.DungeonPermission == nil {
		return nil, ErrRepositoryUnavailable
	}
	return &Owner{repositories: repositories}, nil
}

// AccountOwnerKey is fixed-width so a maximum-length account ID still fits the
// existing VARCHAR(128) MySQL owner column. The namespace prevents collisions
// with legacy numeric character owners; the account ID remains in Snapshot for
// diagnostics and authorization.
func AccountOwnerKey(accountID string) (string, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return "", ErrAccountRequired
	}
	digest := sha256.Sum256([]byte(accountID))
	return accountOwnerKeyPrefix + hex.EncodeToString(digest[:]), nil
}

// LoadAccount只读取实时账号汇总，不枚举角色或执行旧数据迁移。
func (o *Owner) LoadAccount(ctx context.Context, accountID string) (Snapshot, error) {
	if o == nil || o.repositories.DungeonPermission == nil {
		return Snapshot{}, ErrRepositoryUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	accountID = strings.TrimSpace(accountID)
	key, err := AccountOwnerKey(accountID)
	if err != nil {
		return Snapshot{}, err
	}
	record, _, err := o.repositories.DungeonPermission.Load(ctx, key)
	if err != nil {
		return Snapshot{}, err
	}
	state := make(map[uint32]repository.DungeonPermissionEntry, len(record.Entries))
	mergeEntries(state, record.Entries)
	return Snapshot{AccountID: accountID, StorageKey: key, Record: recordFromState(key, state)}, nil
}

// MigrateAccount returns the highest state from the durable account owner and all
// active legacy character owners. Any legacy maximum missing from the account
// owner is copied with UpsertMax. Repeating this scan is intentional: a failed
// partial migration is automatically completed by the next read, without a
// second migration marker or cross-repository transaction.
func (o *Owner) MigrateAccount(ctx context.Context, accountID string) (Snapshot, error) {
	if o == nil || o.repositories.Character == nil || o.repositories.DungeonPermission == nil {
		return Snapshot{}, ErrRepositoryUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	accountID = strings.TrimSpace(accountID)
	storageKey, err := AccountOwnerKey(accountID)
	if err != nil {
		return Snapshot{}, err
	}

	stateByDungeon := make(map[uint32]repository.DungeonPermissionEntry)
	accountRecord, found, err := o.repositories.DungeonPermission.Load(ctx, storageKey)
	if err != nil {
		return Snapshot{}, err
	}
	if found {
		mergeEntries(stateByDungeon, accountRecord.Entries)
	}
	accountState := cloneStateMap(stateByDungeon)

	characters, err := o.repositories.Character.ListByAccount(
		ctx,
		accountID,
		repository.DefaultCharacterSlotLimit,
	)
	if err != nil {
		return Snapshot{}, err
	}
	legacyOwners := 0
	for _, character := range characters {
		if strings.TrimSpace(character.AccountID) != accountID {
			continue
		}
		characterID := strings.TrimSpace(character.CharacterID)
		if characterID == "" {
			continue
		}
		legacy, legacyFound, loadErr := o.repositories.DungeonPermission.Load(ctx, characterID)
		if loadErr != nil {
			return Snapshot{}, loadErr
		}
		if !legacyFound {
			continue
		}
		legacyOwners++
		mergeEntries(stateByDungeon, legacy.Entries)
	}

	dungeonIDs := sortedDungeonIDs(stateByDungeon)
	migrated := 0
	for _, dungeonID := range dungeonIDs {
		entry := stateByDungeon[dungeonID]
		if existing, ok := accountState[dungeonID]; ok && existing.ClearState >= entry.ClearState {
			continue
		}
		persisted, changed, upsertErr := o.repositories.DungeonPermission.UpsertMax(
			ctx,
			storageKey,
			dungeonID,
			entry.ClearState,
		)
		if upsertErr != nil {
			return Snapshot{}, upsertErr
		}
		if persisted.ClearState > stateByDungeon[dungeonID].ClearState {
			stateByDungeon[dungeonID] = persisted
		}
		if changed {
			migrated++
		}
	}

	return Snapshot{
		AccountID:       accountID,
		StorageKey:      storageKey,
		Record:          recordFromState(storageKey, stateByDungeon),
		LegacyOwners:    legacyOwners,
		MigratedEntries: migrated,
	}, nil
}

func (o *Owner) Advance(
	ctx context.Context,
	accountID string,
	dungeonID uint32,
	clearState byte,
) (Snapshot, repository.DungeonPermissionEntry, bool, error) {
	snapshot, err := o.LoadAccount(ctx, accountID)
	if err != nil {
		return Snapshot{}, repository.DungeonPermissionEntry{}, false, err
	}
	entry, changed, err := o.repositories.DungeonPermission.UpsertMax(
		ctx,
		snapshot.StorageKey,
		dungeonID,
		clearState,
	)
	if err != nil {
		return Snapshot{}, repository.DungeonPermissionEntry{}, false, err
	}
	state := make(map[uint32]repository.DungeonPermissionEntry, len(snapshot.Record.Entries)+1)
	mergeEntries(state, snapshot.Record.Entries)
	mergeEntries(state, []repository.DungeonPermissionEntry{entry})
	snapshot.Record = recordFromState(snapshot.StorageKey, state)
	return snapshot, entry, changed, nil
}

func mergeEntries(
	target map[uint32]repository.DungeonPermissionEntry,
	entries []repository.DungeonPermissionEntry,
) {
	for _, entry := range entries {
		if entry.DungeonID == 0 || entry.ClearState == 0 {
			continue
		}
		if current, found := target[entry.DungeonID]; found && current.ClearState >= entry.ClearState {
			continue
		}
		target[entry.DungeonID] = entry
	}
}

func cloneStateMap(source map[uint32]repository.DungeonPermissionEntry) map[uint32]repository.DungeonPermissionEntry {
	clone := make(map[uint32]repository.DungeonPermissionEntry, len(source))
	for dungeonID, entry := range source {
		clone[dungeonID] = entry
	}
	return clone
}

func sortedDungeonIDs(state map[uint32]repository.DungeonPermissionEntry) []uint32 {
	ids := make([]uint32, 0, len(state))
	for dungeonID := range state {
		ids = append(ids, dungeonID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func recordFromState(
	storageKey string,
	state map[uint32]repository.DungeonPermissionEntry,
) repository.DungeonPermissionRecord {
	ids := sortedDungeonIDs(state)
	record := repository.DungeonPermissionRecord{
		CharacterID: storageKey,
		Entries:     make([]repository.DungeonPermissionEntry, 0, len(ids)),
	}
	for index, dungeonID := range ids {
		entry := state[dungeonID]
		entry.SortOrder = index
		if entry.UpdatedAt.After(record.UpdatedAt) {
			record.UpdatedAt = entry.UpdatedAt
		}
		record.Entries = append(record.Entries, entry)
	}
	return record
}
