package repository

import (
	"context"
	"longheng.io/server/internal/modules/dnf/moderation"
)

// ClearHistoryWriter participates in the existing character settlement UOW.
type ClearHistoryWriter interface {
	AppendClear(context.Context, moderation.Clear) error
}
type ClearHistoryReader interface {
	QueryClears(context.Context, moderation.Query) (moderation.ClearPage, error)
}
