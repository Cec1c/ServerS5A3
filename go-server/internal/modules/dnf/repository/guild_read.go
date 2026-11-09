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

// GuildReadCounts observes one synchronous guild-read call stack. Callers must
// read it after the callback returns; the counters intentionally use no shared
// atomics or goroutine coordination.
type GuildReadCounts struct {
	Queries int64
	Rows    int64
}

type guildReadCountsContextKey struct{}

func WithGuildReadCounts(ctx context.Context) (context.Context, *GuildReadCounts) {
	if ctx == nil {
		ctx = context.Background()
	}
	counts := &GuildReadCounts{}
	return context.WithValue(ctx, guildReadCountsContextKey{}, counts), counts
}

func guildReadCountsFromContext(ctx context.Context) *GuildReadCounts {
	if ctx == nil {
		return nil
	}
	counts, _ := ctx.Value(guildReadCountsContextKey{}).(*GuildReadCounts)
	return counts
}

func RecordGuildCharacterQuery(ctx context.Context) {
	if counts := guildReadCountsFromContext(ctx); counts != nil {
		counts.Queries++
	}
}

func RecordGuildCharacterRows(ctx context.Context, n int64) {
	if counts := guildReadCountsFromContext(ctx); counts != nil && n > 0 {
		counts.Rows += n
	}
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

func (s *readOnlyGuildSettings) GuildSnapshotNamespace() *GuildSnapshotNamespace {
	if reader, ok := s.base.(GuildSnapshotRevisionReader); ok {
		return reader.GuildSnapshotNamespace()
	}
	return nil
}
func (s *readOnlyGuildSettings) LoadGuildSnapshotRevision(ctx context.Context) (string, bool, error) {
	if reader, ok := s.base.(GuildSnapshotRevisionReader); ok {
		return reader.LoadGuildSnapshotRevision(ctx)
	}
	return "", false, ErrGuildTransaction
}
func (*readOnlyGuildSettings) Save(context.Context, SettingsRecord) error { return ErrGuildTransaction }
func (s *readOnlyGuildSettings) WalkGuildSettlementReceipts(ctx context.Context, guildID uint32, since time.Time, visit func(SettingsRecord) error) error {
	r, ok := s.base.(GuildSettlementReceiptReader)
	if !ok {
		return ErrGuildTransaction
	}
	return r.WalkGuildSettlementReceipts(ctx, guildID, since, visit)
}
