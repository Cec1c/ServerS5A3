package memory

import (
	"context"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/db"
)

type memorySettingsStore struct {
	*db.MemoryStore[repository.SettingsRecord]
}

func (s *memorySettingsStore) WalkGuildSettlementReceipts(ctx context.Context, since time.Time, visit func(repository.SettingsRecord) error) error {
	if visit == nil {
		return repository.ErrGuildTransaction
	}
	for _, record := range s.Snapshot(nil) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.HasPrefix(record.Scope, "guild-clear:v1:") && !record.UpdatedAt.Before(since) && record.Values["receipt"] != "" {
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

func (s *memoryGuildReceiptSettings) WalkGuildSettlementReceipts(ctx context.Context, since time.Time, visit func(repository.SettingsRecord) error) error {
	r, ok := s.reader.(repository.GuildSettlementReceiptReader)
	if !ok {
		return repository.ErrGuildTransaction
	}
	return r.WalkGuildSettlementReceipts(ctx, since, visit)
}
