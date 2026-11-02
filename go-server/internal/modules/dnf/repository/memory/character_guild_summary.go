package memory

import (
	"context"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
	platformdb "longheng.io/server/internal/platform/db"
)

// LoadCharacterGuildSummaries projects only the scalar guild-display fields.
// storyDigestMu is the existing character-store write fence, so a multi-ID read
// cannot observe a concurrent character save between individual lookups.
func (s *memoryCharStore) LoadCharacterGuildSummaries(ctx context.Context, ids []string) (map[string]repository.CharacterGuildSummary, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}

	s.storyDigestMu.Lock()
	defer s.storyDigestMu.Unlock()
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}

	summaries := make(map[string]repository.CharacterGuildSummary)
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if err := ctxErr(ctx); err != nil {
			return nil, err
		}
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}

		summary, found, err := platformdb.ProjectMemoryValue(ctx, s.MemoryStore, id, func(record repository.CharacterRecord) repository.CharacterGuildSummary {
			return repository.CharacterGuildSummary{
				CharacterID: record.CharacterID,
				AccountID:   record.AccountID,
				Name:        record.Name,
				Job:         record.Job,
				Level:       record.Level,
				GrowType:    record.Stats[repository.CharacterGrowTypeStatKey],
			}
		})
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		summaries[id] = summary
	}
	return summaries, nil
}
