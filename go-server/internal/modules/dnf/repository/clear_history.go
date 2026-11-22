package repository

import (
	"context"
	"longheng.io/server/internal/modules/dnf/moderation"
	"time"
)

// ClearHistoryWriter participates in the existing character settlement UOW.
type ClearHistoryWriter interface {
	AppendClear(context.Context, moderation.Clear) error
}
type ClearHistoryReader interface {
	QueryClears(context.Context, moderation.Query) (moderation.ClearPage, error)
}

type CharacterDungeonClearReader interface {
	HasDungeonClearSince(context.Context, string, int64, time.Time) (bool, error)
}
