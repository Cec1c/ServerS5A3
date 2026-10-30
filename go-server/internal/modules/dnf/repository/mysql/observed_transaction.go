package mysql

import (
	"context"
	"database/sql"
	"sync"
	"time"
)

// The embedded transaction retains the original connection and isolation.
// Observation finishes on the first Commit/Rollback, including deferred cleanup;
// the usual rollback after a successful commit must not add a second sample.
type observedSQLTransaction struct {
	*sql.Tx
	observation *SQLObservation
	started     time.Time
	once        sync.Once
}

func beginObservedSQLTransaction(ctx context.Context, beginner sqlTxBeginner, opts *sql.TxOptions) (*observedSQLTransaction, error) {
	tx, err := beginner.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	result := &observedSQLTransaction{Tx: tx}
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
