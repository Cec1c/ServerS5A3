package repository

import (
	"context"
	"sort"
	"strings"
)

// CharacterGuildSummary is the narrow character projection used by guild and
// character-selection display reads. It must not be saved as a CharacterRecord.
type CharacterGuildSummary struct {
	CharacterID string
	AccountID   string
	Name        string
	Job         string
	Level       int
	GrowType    int64
}

// CharacterGuildSummaryReader avoids loading character aggregates and their
// supplementary stats, roster, or equipment state for display-only reads.
type CharacterGuildSummaryReader interface {
	LoadCharacterGuildSummaries(context.Context, []string) (map[string]CharacterGuildSummary, error)
}

// CharacterGuildSummaryReadCounts is a synchronous call-stack counter set.
// Callers must not share one counts value across concurrent reads.
type CharacterGuildSummaryReadCounts struct {
	Queries   int64
	FullLoads int64
}

type characterGuildSummaryReadCountsContextKey struct{}

// WithCharacterGuildSummaryReadCounts attaches fresh read counters to one
// synchronous summary call stack.
func WithCharacterGuildSummaryReadCounts(ctx context.Context) (context.Context, *CharacterGuildSummaryReadCounts) {
	if ctx == nil {
		ctx = context.Background()
	}
	counts := &CharacterGuildSummaryReadCounts{}
	return context.WithValue(ctx, characterGuildSummaryReadCountsContextKey{}, counts), counts
}

// RecordCharacterGuildSummaryQuery records one character-table query issued in
// the current summary call stack.
func RecordCharacterGuildSummaryQuery(ctx context.Context) {
	if ctx == nil {
		return
	}
	if counts, _ := ctx.Value(characterGuildSummaryReadCountsContextKey{}).(*CharacterGuildSummaryReadCounts); counts != nil {
		counts.Queries++
	}
}

// RecordCharacterGuildSummaryFullLoad records one fallback CharacterRepository
// Load issued in the current summary call stack.
func RecordCharacterGuildSummaryFullLoad(ctx context.Context) {
	if ctx == nil {
		return
	}
	if counts, _ := ctx.Value(characterGuildSummaryReadCountsContextKey{}).(*CharacterGuildSummaryReadCounts); counts != nil {
		counts.FullLoads++
	}
}

// LoadCharacterGuildSummaries uses the narrow reader when available. Repositories
// without that capability fall back to one complete Load per unique, nonblank ID.
func LoadCharacterGuildSummaries(ctx context.Context, characters CharacterRepository, ids []string) (map[string]CharacterGuildSummary, error) {
	if reader, ok := characters.(CharacterGuildSummaryReader); ok {
		return reader.LoadCharacterGuildSummaries(ctx, ids)
	}

	ids = normalizeCharacterGuildSummaryIDs(ids)
	summaries := make(map[string]CharacterGuildSummary, len(ids))
	for _, id := range ids {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}

		RecordCharacterGuildSummaryFullLoad(ctx)
		record, found, err := characters.Load(ctx, id)
		if err != nil {
			return nil, err
		}
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if !found {
			continue
		}
		summaries[id] = CharacterGuildSummary{
			CharacterID: record.CharacterID,
			AccountID:   record.AccountID,
			Name:        record.Name,
			Job:         record.Job,
			Level:       record.Level,
			GrowType:    record.Stats[CharacterGrowTypeStatKey],
		}
	}
	return summaries, nil
}

func normalizeCharacterGuildSummaryIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	normalized := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	sort.Strings(normalized)
	return normalized
}
