package repository

import (
	"context"
	"errors"
	"time"
)

var ErrEpicBookTransactionUnavailable = errors.New("epic book asset transaction unavailable")
var ErrEpicBookPieceInsufficient = errors.New("epic book piece balance insufficient")
var ErrEpicBookOwnership = errors.New("epic book character ownership mismatch")

// EpicBookPieceDebitor is scoped to the authenticated account and one physical
// asset transaction. It never exposes Save(AccountRecord), which could replace
// unrelated metadata or lose a concurrent piece pickup.
type EpicBookPieceDebitor interface {
	DebitPiece(context.Context, uint32, uint32, time.Time) (uint32, error)
}

// EpicBookAssetUnitOfWork is an optional AccountAssets capability. Piece debit,
// shared soul inventory and the character's product share commit/rollback.
// Implementations serialize piece credits against the entire transaction.
type EpicBookAssetUnitOfWork interface {
	WithinEpicBookAssets(context.Context, string, string,
		func(EpicBookPieceDebitor, CharacterRecord, AccountInventoryRepository, InventoryRepository) error) error
}
