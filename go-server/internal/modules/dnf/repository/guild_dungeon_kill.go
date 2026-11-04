package repository

import (
	"context"
	"crypto/sha256"
	"fmt"
)

// One character, its inventory/mailbox and exactly one run receipt. The
// registry is inaccessible; admitted kill rewards do not update purification.
type GuildDungeonKillUnitOfWork interface {
	WithinGuildDungeonKill(context.Context, GuildOwner, string, func(Group) error) error
}

func GuildDungeonKillReceiptScope(owner GuildOwner, run string) string {
	return fmt.Sprintf("guild-kills:v1:%x", sha256.Sum256([]byte(fmt.Sprintf("%q:%q:%q", owner.AccountID, owner.CharacterID, run))))
}

func GuildDungeonKillSettings(base SettingsRepository, scope string) SettingsRepository {
	return &guildDungeonKillSettings{base, scope}
}

type guildDungeonKillSettings struct {
	base  SettingsRepository
	scope string
}

func (s *guildDungeonKillSettings) Load(ctx context.Context, scope string) (SettingsRecord, bool, error) {
	if s.base == nil || scope != s.scope {
		return SettingsRecord{}, false, ErrGuildTransaction
	}
	return s.base.Load(ctx, scope)
}
func (s *guildDungeonKillSettings) Save(ctx context.Context, record SettingsRecord) error {
	if s.base == nil || record.Scope != s.scope {
		return ErrGuildTransaction
	}
	return s.base.Save(ctx, record)
}
