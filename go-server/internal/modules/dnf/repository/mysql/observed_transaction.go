package mysql

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

// The embedded transaction retains the original connection and isolation.
// Observation finishes on the first Commit/Rollback, including deferred cleanup;
// the usual rollback after a successful commit must not add a second sample.
type observedSQLTransaction struct {
	Tx          *committedSQLTransaction
	observation *SQLObservation
	started     time.Time
	once        sync.Once
}

func beginObservedSQLTransaction(ctx context.Context, beginner sqlTxBeginner, opts *sql.TxOptions) (*observedSQLTransaction, error) {
	timing := repository.TransactionTimingFromContext(ctx)
	var started time.Time
	if timing != nil {
		started = time.Now()
	}
	tx, err := beginner.BeginTx(ctx, opts)
	if timing != nil {
		timing.Record(repository.TransactionBegin, time.Since(started), 1)
	}
	if err != nil {
		return nil, err
	}
	result := &observedSQLTransaction{Tx: &committedSQLTransaction{sqlTransaction: tx}}
	if adapter, ok := beginner.(*ObservedSQLDB); ok {
		result.observation = adapter.Observation
		result.started = result.observation.start()
	}
	return result, nil
}

func (tx *observedSQLTransaction) Commit() error {
	err := tx.Tx.Commit()
	tx.finish(err)
	return err
}

func (tx *observedSQLTransaction) Rollback() error {
	err := tx.Tx.Rollback()
	tx.finish(err)
	return err
}

func (tx *observedSQLTransaction) finish(err error) {
	tx.once.Do(func() {
		// This error count describes the final SQL operation, not an owner
		// rejection that deliberately rolled back without a database failure.
		tx.observation.finish(1, sqlObserveTransaction, tx.started, 0, err)
	})
}
