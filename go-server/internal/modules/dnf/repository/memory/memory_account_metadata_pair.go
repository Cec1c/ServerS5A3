package memory

import (
	"context"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"time"
)

// Reads share nameMu so a reader cannot observe half of a reciprocal update.
func (s *memoryAccountStore) Load(ctx context.Context, id string) (repository.AccountRecord, bool, error) {
	s.nameMu.Lock()
	defer s.nameMu.Unlock()
	return s.MemoryStore.Load(ctx, id)
}

func (s *memoryAccountStore) UpdateMetadataPair(ctx context.Context, left, right string, apply func(repository.AccountRecord, repository.AccountRecord) (map[string]string, map[string]string, error), at time.Time) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" || left == right || apply == nil {
		return repository.ErrAccountMetadataUnavailable
	}
	s.nameMu.Lock()
	defer s.nameMu.Unlock()
	a, af, err := s.MemoryStore.Load(ctx, left)
	if err != nil {
		return err
	}
	if !af {
		return repository.ErrAccountMetadataUnavailable
	}
	b, bf, err := s.MemoryStore.Load(ctx, right)
	if err != nil {
		return err
	}
	if !bf {
		return repository.ErrAccountMetadataUnavailable
	}
	ac, bc, err := apply(repository.CloneAccount(a), repository.CloneAccount(b))
	if err != nil {
		return err
	}
	if !repository.ValidAccountMetadataChanges(ac) || !repository.ValidAccountMetadataChanges(bc) {
		return repository.ErrAccountMetadataKeyRequired
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	records := []*repository.AccountRecord{&a, &b}
	for i, changes := range []map[string]string{ac, bc} {
		record := records[i]
		if len(changes) == 0 {
			continue
		}
		if record.Metadata == nil {
			record.Metadata = make(map[string]string)
		}
		for key, value := range changes {
			record.Metadata[key] = value
		}
		record.UpdatedAt = at.UTC()
	}
	// Keys already exist. These in-memory writes have no failure after the final
	// cancellation check; cancellation must not leave a half-committed pair.
	if len(ac) > 0 {
		if err := s.MemoryStore.Save(context.Background(), a); err != nil {
			return err
		}
	}
	if len(bc) > 0 {
		return s.MemoryStore.Save(context.Background(), b)
	}
	return nil
}
