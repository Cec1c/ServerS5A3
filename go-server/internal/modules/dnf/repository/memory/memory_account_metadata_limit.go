package memory

import (
	"context"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"time"
)

func (s *memoryAccountStore) SaveMetadataEntryWithPrefixLimit(ctx context.Context, accountID, prefix, key, activeValue string, limit int, updatedAt time.Time) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(accountID) == "" || prefix == "" || !strings.HasPrefix(key, prefix) || activeValue == "" || limit <= 0 {
		return repository.ErrAccountMetadataKeyRequired
	}
	s.nameMu.Lock()
	defer s.nameMu.Unlock()
	record, found, err := s.MemoryStore.Load(ctx, accountID)
	if err != nil {
		return err
	}
	if !found {
		return repository.ErrAccountMetadataUnavailable
	}
	if record.Metadata[key] != activeValue {
		count := 0
		for k, v := range record.Metadata {
			if strings.HasPrefix(k, prefix) && v == activeValue {
				count++
			}
		}
		if count >= limit {
			return repository.ErrAccountMetadataLimitReached
		}
	}
	record = repository.CloneAccount(record)
	if record.Metadata == nil {
		record.Metadata = make(map[string]string)
	}
	record.Metadata[key] = activeValue
	record.UpdatedAt = updatedAt.UTC()
	return s.MemoryStore.Save(ctx, record)
}
