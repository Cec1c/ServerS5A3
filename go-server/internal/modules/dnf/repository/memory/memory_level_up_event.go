package memory

import (
	"context"
	"longheng.io/server/internal/modules/dnf/repository"
)

func (s *memoryCharStore) ListPendingLevelUpCharacters(ctx context.Context, after string, limit int) ([]string, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 128 {
		limit = 128
	}
	records := s.Snapshot(func(a, b repository.CharacterRecord) bool { return a.CharacterID < b.CharacterID })
	var result []string
	for _, record := range records {
		if record.CharacterID > after && record.Stats["delete_flag"] == 0 && repository.HasPendingLevelUp(record) {
			result = append(result, record.CharacterID)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}
