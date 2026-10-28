package memory

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/db"
)

type memorySettingsStore struct {
	*db.MemoryStore[repository.SettingsRecord]
}

func (s *memorySettingsStore) WalkGuildSettlementReceipts(ctx context.Context, guildID uint32, since time.Time, visit func(repository.SettingsRecord) error) error {
	if visit == nil || guildID == 0 {
		return repository.ErrGuildTransaction
	}
	for _, record := range s.Snapshot(nil) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.HasPrefix(record.Scope, "guild-clear:v1:") && !record.UpdatedAt.Before(since) && record.Values["receipt"] != "" {
			var receipt struct{ GuildDungeon struct{ GuildID uint32 } }
			if json.Unmarshal([]byte(record.Values["receipt"]), &receipt) == nil && receipt.GuildDungeon.GuildID != guildID {
				continue
			}
			if err := visit(record); err != nil {
				return err
			}
		}
	}
	return nil
}

// Keep normal transaction writes restricted to the registry while exposing
// immutable receipts through this narrow read-only contract.
type memoryGuildReceiptSettings struct {
	repository.SettingsRepository
	reader repository.SettingsRepository
}

func (s *memoryGuildReceiptSettings) WalkGuildSettlementReceipts(ctx context.Context, guildID uint32, since time.Time, visit func(repository.SettingsRecord) error) error {
	r, ok := s.reader.(repository.GuildSettlementReceiptReader)
	if !ok {
		return repository.ErrGuildTransaction
	}
	return r.WalkGuildSettlementReceipts(ctx, guildID, since, visit)
}
