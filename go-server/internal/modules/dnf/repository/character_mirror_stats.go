package repository

import (
	"context"
	"time"
)

// CharacterMirrorStats is a fresh read-only projection, never a writable record.
type CharacterMirrorStats struct {
	CharacterID string
	AccountID   string
	Slot        int
	Name        string
	Job         string
	Level       int
	Stats       map[string]int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type CharacterMirrorStatsReader interface {
	LoadCharacterMirrorStats(context.Context, string, ...string) (CharacterMirrorStats, bool, error)
}

// CharacterMirrorStatKeys normalizes keys without modifying the caller's slice.
// Zero and one key need no allocation.
func CharacterMirrorStatKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	if len(keys) == 1 {
		if keys[0] == "" {
			return nil
		}
		return keys
	}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		duplicate := false
		for _, prior := range out {
			if prior == key {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, key)
		}
	}
	return out
}

func LoadCharacterMirrorStats(ctx context.Context, store CharacterRepository, id string, statKeys ...string) (CharacterMirrorStats, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return CharacterMirrorStats{}, false, err
	}
	keys := CharacterMirrorStatKeys(statKeys)
	if reader, ok := store.(CharacterMirrorStatsReader); ok {
		return reader.LoadCharacterMirrorStats(ctx, id, keys...)
	}
	record, found, err := store.Load(ctx, id)
	return SelectCharacterMirrorStats(record, keys...), found, err
}

func SelectCharacterMirrorStats(record CharacterRecord, keys ...string) CharacterMirrorStats {
	out := CharacterMirrorStats{CharacterID: record.CharacterID, AccountID: record.AccountID, Slot: record.Slot, Name: record.Name, Job: record.Job, Level: record.Level, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}
	for _, key := range keys {
		if key == "" {
			continue
		}
		if value, ok := record.Stats[key]; ok {
			if out.Stats == nil {
				out.Stats = make(map[string]int64, len(keys))
			}
			out.Stats[key] = value
		}
	}
	return out
}
