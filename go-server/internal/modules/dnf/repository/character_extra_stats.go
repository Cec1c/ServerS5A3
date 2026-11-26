package repository

import "context"

// CharacterExtraStatsReader reads a character's core identity/level fields and
// a bounded set of non-mirror extra stat keys in a single read-only snapshot.
type CharacterExtraStatsReader interface {
	LoadCharacterExtraStats(context.Context, string, ...string) (CharacterMirrorStats, bool, error)
}

func LoadCharacterExtraStats(ctx context.Context, store CharacterRepository, id string, statKeys ...string) (CharacterMirrorStats, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return CharacterMirrorStats{}, false, err
	}
	keys := CharacterMirrorStatKeys(statKeys)
	if reader, ok := store.(CharacterExtraStatsReader); ok {
		return reader.LoadCharacterExtraStats(ctx, id, keys...)
	}
	record, found, err := store.Load(ctx, id)
	return SelectCharacterMirrorStats(record, keys...), found, err
}
