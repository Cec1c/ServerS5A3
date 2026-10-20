package repository

import (
	"context"
	"errors"
	"sort"
	"strings"
)

const GuildRegistryScope = "global:guild_registry:v1"

var ErrGuildTransaction = errors.New("guild transaction owner is unavailable")
var ErrGuildCommitUnknown = errors.New("guild SQL commit outcome is unknown")

type GuildOwner struct {
	AccountID   string
	CharacterID string
}

// GuildUnitOfWork serializes the shared guild registry and at most two actual
// participants. Creation debits the creator and records both memberships in the
// same commit; unrelated wallets, characters and settings remain inaccessible.
type GuildUnitOfWork interface {
	WithinGuild(context.Context, []GuildOwner, func(Group) error) error
}

// GuildInventoryUnitOfWork adds only the actor's inventory to the guild lock
// boundary. Creation and member-management transactions retain their original
// scope. Lock order is registry, owned character, owned inventory.
type GuildInventoryUnitOfWork interface {
	WithinGuildInventory(context.Context, GuildOwner, func(Group) error) error
}

func GuildInventoryScopedGroup(base Group, owner GuildOwner) Group {
	group := GuildScopedGroup(base, []GuildOwner{owner})
	group.Inventory = &guildInventory{base: base.Inventory, settings: base.Settings, characterID: owner.CharacterID}
	return group
}

type guildInventory struct {
	base        InventoryRepository
	settings    SettingsRepository
	characterID string
}

func (r *guildInventory) Load(ctx context.Context, id string) (InventoryRecord, bool, error) {
	if r.base == nil || id != r.characterID {
		return InventoryRecord{}, false, ErrGuildTransaction
	}
	record, found, err := r.base.Load(ctx, id)
	if err == nil && found && record.CharacterID != id {
		return InventoryRecord{}, false, ErrGuildTransaction
	}
	if err == nil && found {
		err = BindInventoryContainerState(ctx, r.settings, &record)
	}
	return record, found, err
}

func (r *guildInventory) Save(ctx context.Context, record InventoryRecord) error {
	if r.base == nil || record.CharacterID != r.characterID {
		return ErrGuildTransaction
	}
	return r.base.Save(ctx, record)
}

func (r *guildInventory) SaveFields(ctx context.Context, record InventoryRecord, fields ...InventoryField) error {
	if r.base == nil || record.CharacterID != r.characterID {
		return ErrGuildTransaction
	}
	return SaveInventoryFields(ctx, r.base, record, fields...)
}

func GuildOwners(owners []GuildOwner) ([]GuildOwner, error) {
	if len(owners) < 1 || len(owners) > 2 {
		return nil, ErrGuildTransaction
	}
	ordered := append([]GuildOwner(nil), owners...)
	seen := map[string]bool{}
	for _, o := range ordered {
		if o.AccountID == "" || o.CharacterID == "" || strings.TrimSpace(o.AccountID) != o.AccountID || strings.TrimSpace(o.CharacterID) != o.CharacterID || seen[o.CharacterID] {
			return nil, ErrGuildTransaction
		}
		seen[o.CharacterID] = true
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].CharacterID < ordered[j].CharacterID })
	return ordered, nil
}

func GuildScopedGroup(base Group, owners []GuildOwner) Group {
	allowed := make(map[string]string, len(owners))
	for _, owner := range owners {
		allowed[owner.CharacterID] = owner.AccountID
	}
	return Group{Character: &guildCharacters{base: base.Character, allowed: allowed}, Settings: &guildSettings{base: base.Settings}}
}

type guildCharacters struct {
	base    CharacterRepository
	allowed map[string]string
}

func (r *guildCharacters) Load(ctx context.Context, id string) (CharacterRecord, bool, error) {
	account, ok := r.allowed[id]
	if !ok {
		return CharacterRecord{}, false, ErrGuildTransaction
	}
	record, found, err := r.base.Load(ctx, id)
	if err == nil && (!found || record.CharacterID != id || record.AccountID != account) {
		return CharacterRecord{}, false, ErrGuildTransaction
	}
	return record, found, err
}
func (r *guildCharacters) Save(ctx context.Context, record CharacterRecord) error {
	if account, ok := r.allowed[record.CharacterID]; !ok || account != record.AccountID {
		return ErrGuildTransaction
	}
	return r.base.Save(ctx, record)
}
func (*guildCharacters) ListByAccount(context.Context, string, int) ([]CharacterRecord, error) {
	return nil, ErrGuildTransaction
}
func (*guildCharacters) FindIDByName(context.Context, string) (string, bool, error) {
	return "", false, ErrGuildTransaction
}
func (*guildCharacters) NextNumericID(context.Context) (int, error) { return 0, ErrGuildTransaction }

type guildSettings struct{ base SettingsRepository }

func (r *guildSettings) Load(ctx context.Context, scope string) (SettingsRecord, bool, error) {
	if scope != GuildRegistryScope {
		return SettingsRecord{}, false, ErrGuildTransaction
	}
	record, found, err := r.base.Load(ctx, scope)
	if err == nil && found && record.Scope != scope {
		return SettingsRecord{}, false, ErrGuildTransaction
	}
	return record, found, err
}
func (r *guildSettings) Save(ctx context.Context, record SettingsRecord) error {
	if record.Scope != GuildRegistryScope {
		return ErrGuildTransaction
	}
	return r.base.Save(ctx, record)
}
