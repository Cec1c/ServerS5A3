package memory

import (
	"context"
	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/db"
)

var _ repository.CharacterMirrorStatsReader = (*memoryCharStore)(nil)
var _ repository.CharacterExtraStatsReader = (*memoryCharStore)(nil)

func (s *memoryCharStore) LoadCharacterMirrorStats(ctx context.Context, id string, keys ...string) (repository.CharacterMirrorStats, bool, error) {
	if err := ctxErr(ctx); err != nil {
		return repository.CharacterMirrorStats{}, false, err
	}
	record, found := db.ProjectMemoryRecord(s.MemoryStore, id, func(row repository.CharacterRecord) repository.CharacterMirrorStats {
		return repository.SelectCharacterMirrorStats(row, keys...)
	})
	return record, found, nil
}

func (s *memoryCharStore) LoadCharacterExtraStats(ctx context.Context, id string, keys ...string) (repository.CharacterMirrorStats, bool, error) {
	return s.LoadCharacterMirrorStats(ctx, id, keys...)
}
