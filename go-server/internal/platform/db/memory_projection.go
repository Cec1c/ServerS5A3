package db

import (
	"context"
	"errors"
)

// ProjectMemoryValue reads one stored value under MemoryStore's read lock and
// projects it without invoking the configured clone function. The project
// callback must be short, must not modify the stored value, must not retain or
// return references into it, and must not call back into the same store.
func ProjectMemoryValue[T, R any](ctx context.Context, store *MemoryStore[T], key string, project func(T) R) (R, bool, error) {
	var zero R
	if store == nil {
		return zero, false, errors.New("memory store is nil")
	}
	ctx = contextOrBackground(ctx)
	if err := ctx.Err(); err != nil {
		return zero, false, err
	}

	store.mu.RLock()
	defer store.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return zero, false, err
	}
	record, ok := store.records[key]
	if !ok {
		return zero, false, nil
	}
	return project(record), true, nil
}
