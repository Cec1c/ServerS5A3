package repository

import (
	"context"
	"time"
)

// GuildCharacter is a display-only projection, never an asset snapshot.
type GuildCharacter struct {
	CharacterID, AccountID, Name, Job string
	Level                             int
	GrowType, DeleteFlag              int64
}

type GuildCharacterReader interface {
	LoadGuildCharacters(context.Context, []string) (map[string]GuildCharacter, error)
}

// GuildReadView must use one consistent snapshot for membership, receipts and
// character identities. Settings permits only registry/receipt reads, not Save.
type GuildReadView struct {
	Settings   SettingsRepository
	Characters GuildCharacterReader
}

type GuildReadUnitOfWork interface {
	WithinGuildRead(context.Context, func(GuildReadView) error) error
	CompareGuildRegistry(context.Context, SettingsRecord, SettingsRecord) (bool, error)
}

type readOnlyGuildSettings struct{ base SettingsRepository }

func ReadOnlyGuildSettings(base SettingsRepository) SettingsRepository {
	return &readOnlyGuildSettings{base: base}
}
func (s *readOnlyGuildSettings) Load(ctx context.Context, scope string) (SettingsRecord, bool, error) {
	if scope != GuildRegistryScope {
		return SettingsRecord{}, false, ErrGuildTransaction
	}
	return s.base.Load(ctx, scope)
}
func (*readOnlyGuildSettings) Save(context.Context, SettingsRecord) error { return ErrGuildTransaction }
func (s *readOnlyGuildSettings) WalkGuildSettlementReceipts(ctx context.Context, guildID uint32, since time.Time, visit func(SettingsRecord) error) error {
	r, ok := s.base.(GuildSettlementReceiptReader)
	if !ok {
		return ErrGuildTransaction
	}
	return r.WalkGuildSettlementReceipts(ctx, guildID, since, visit)
}
