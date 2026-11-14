package repository

import "context"

// A read-only projection of the complete wedding namespace and its owner.
// It is deliberately separate from the saveable character record.
type CharacterWeddingSnapshot struct {
	CharacterID, AccountID string
	Stats                  map[string]int64
}

type CharacterWeddingReader interface {
	LoadCharacterWedding(context.Context, string) (CharacterWeddingSnapshot, bool, error)
}

func LoadCharacterWedding(ctx context.Context, store CharacterRepository, id string) (CharacterWeddingSnapshot, bool, error) {
	if reader, ok := store.(CharacterWeddingReader); ok {
		return reader.LoadCharacterWedding(ctx, id)
	}
	c, found, err := store.Load(ctx, id)
	if err != nil || !found {
		return CharacterWeddingSnapshot{}, found, err
	}
	snapshot := CharacterWeddingSnapshot{CharacterID: c.CharacterID, AccountID: c.AccountID, Stats: map[string]int64{}}
	for k, v := range c.Stats {
		if IsCharacterWeddingStat(k) {
			snapshot.Stats[k] = v
		}
	}
	return snapshot, true, nil
}

func (s *RedisCachedCharStore) LoadCharacterWedding(ctx context.Context, id string) (CharacterWeddingSnapshot, bool, error) {
	return LoadCharacterWedding(ctx, s.backing, id)
}
