package memory

import (
	"context"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/db"
)

func (s *memoryAccountStore) CreateIfAbsent(ctx context.Context, record repository.AccountRecord) (bool, error) {
	if err := ctxErr(ctx); err != nil {
		return false, err
	}
	id := repository.AccountKey(record)
	if id == "" {
		return false, db.ErrRecordKeyRequired
	}
	s.nameMu.Lock()
	defer s.nameMu.Unlock()
	if _, found, err := s.MemoryStore.Load(ctx, id); err != nil || found {
		return false, err
	}
	name := strings.TrimSpace(record.RepresentAccountName)
	if name != "" {
		for _, existing := range s.Snapshot(nil) {
			if strings.EqualFold(strings.TrimSpace(existing.RepresentAccountName), name) {
				return false, repository.ErrRepresentAccountNameExists
			}
		}
	}
	if err := ctxErr(ctx); err != nil {
		return false, err
	}
	record.AccountID, record.RepresentAccountName = id, name
	if err := s.MemoryStore.Save(ctx, record); err != nil {
		return false, err
	}
	return true, nil
}
