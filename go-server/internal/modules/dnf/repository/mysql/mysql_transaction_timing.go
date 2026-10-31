package mysql

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

func beginTimedObservedSQLTransaction(ctx context.Context, beginner sqlTxBeginner) (*observedSQLTransaction, error) {
	timing := repository.TransactionTimingFromContext(ctx)
	started := time.Now()
	tx, err := beginObservedSQLTransaction(ctx, beginner, nil)
	if timing != nil {
		timing.Record(repository.TransactionBegin, time.Since(started), 1)
	}
	return tx, err
}

func beginTimedSQLTransaction(ctx context.Context, beginner sqlTxBeginner) (*sql.Tx, error) {
	timing := repository.TransactionTimingFromContext(ctx)
	if timing == nil {
		return beginner.BeginTx(ctx, nil)
	}
	started := time.Now()
	tx, err := beginner.BeginTx(ctx, nil)
	timing.Record(repository.TransactionBegin, time.Since(started), 1)
	return tx, err
}

func commitTimedSQLTransaction(ctx context.Context, tx interface{ Commit() error }) error {
	timing := repository.TransactionTimingFromContext(ctx)
	if timing == nil {
		return tx.Commit()
	}
	started := time.Now()
	err := tx.Commit()
	timing.Record(repository.TransactionCommit, time.Since(started), 1)
	return err
}

func rollbackTimedSQLTransaction(ctx context.Context, tx interface{ Rollback() error }) error {
	timing := repository.TransactionTimingFromContext(ctx)
	if timing == nil {
		return tx.Rollback()
	}
	started := time.Now()
	err := tx.Rollback()
	if err != sql.ErrTxDone {
		timing.Record(repository.TransactionRollback, time.Since(started), 1)
	}
	return err
}

func transactionReadPhase(query string) repository.TransactionTimingPhase {
	if strings.HasSuffix(query, " FOR UPDATE") {
		return repository.TransactionLockRead
	}
	return repository.TransactionRead
}

// 各次驱动调用分别计时，不把业务在Next/Scan之间的计算算作数据库读取。
type timedTransactionRow struct {
	SQLRow
	timing *repository.TransactionTiming
	phase  repository.TransactionTimingPhase
}

func (r timedTransactionRow) Scan(dest ...any) error {
	started := time.Now()
	err := r.SQLRow.Scan(dest...)
	r.timing.Record(r.phase, time.Since(started), 0)
	return err
}

type timedTransactionRows struct {
	SQLRows
	timing *repository.TransactionTiming
	phase  repository.TransactionTimingPhase
}

func (r timedTransactionRows) Next() bool {
	started := time.Now()
	value := r.SQLRows.Next()
	r.timing.Record(r.phase, time.Since(started), 0)
	return value
}

func (r timedTransactionRows) Scan(dest ...any) error {
	started := time.Now()
	err := r.SQLRows.Scan(dest...)
	r.timing.Record(r.phase, time.Since(started), 0)
	return err
}

func (r timedTransactionRows) Close() error {
	started := time.Now()
	err := r.SQLRows.Close()
	r.timing.Record(r.phase, time.Since(started), 0)
	return err
}
