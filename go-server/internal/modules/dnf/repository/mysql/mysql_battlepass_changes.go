package mysql

import (
	"context"
	"database/sql"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
)

// 对原事务增加提交后回调；SQL、原Commit/Rollback和错误保持原样。
type sqlTransaction interface {
	PrepareContext(context.Context, string) (*sql.Stmt, error)
	StmtContext(context.Context, *sql.Stmt) *sql.Stmt
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	Commit() error
	Rollback() error
}
type committedSQLTransaction struct {
	sqlTransaction
	pending []func()
}

func beginCommittedSQLTransaction(ctx context.Context, b sqlTxBeginner, opts *sql.TxOptions) (*committedSQLTransaction, error) {
	tx, e := b.BeginTx(ctx, opts)
	if e != nil {
		return nil, e
	}
	return &committedSQLTransaction{sqlTransaction: tx}, nil
}
func (tx *committedSQLTransaction) Commit() error {
	e := tx.sqlTransaction.Commit()
	pending := tx.pending
	tx.pending = nil
	if e == nil {
		for _, fn := range pending {
			fn()
		}
	}
	return e
}
func (tx *committedSQLTransaction) Rollback() error {
	tx.pending = nil
	return tx.sqlTransaction.Rollback()
}
func stageBattlePassChange(database SQLDB, feed *repo.BattlePassChangeFeed, e repo.BattlePassChange) {
	if !feed.Interested(e.AccountID) {
		return
	}
	db, ok := database.(sqlTransactionDB)
	if !ok {
		return
	}
	tx, ok := db.tx.(*committedSQLTransaction)
	if !ok {
		return
	}
	tx.pending = append(tx.pending, func() { feed.Publish(e) })
}
func stageBattlePassCounters(database SQLDB, router mysqlRouter, ch repo.CharacterRecord, old ...map[string]int64) {
	if !router.battlePassChanges.Interested(ch.AccountID) {
		return
	}
	counters := make(map[string]int64)
	changed := len(old) != 1
	for key, v := range ch.Stats {
		if !strings.HasPrefix(key, repo.BattlePassCounterPrefix) {
			continue
		}
		counters[key] = v
		if len(old) == 1 && old[0][key] != v {
			changed = true
		}
	}
	if changed && len(counters) > 0 {
		stageBattlePassChange(database, router.battlePassChanges, repo.BattlePassChange{AccountID: ch.AccountID, CharacterID: ch.CharacterID, Counters: counters})
	}
}
