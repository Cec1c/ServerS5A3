package mysql

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
)

// PreparedSQLDB bounds retained query shapes. Once full, new shapes use the
// ordinary driver path. Statements remain owned by this adapter until Close;
// database/sql manages preparation on each connection and reconnects.
type PreparedSQLDB struct {
	SQLDBAdapter
	mu         sync.RWMutex
	capacity   int
	statements map[string]*sql.Stmt
	closed     bool
	preparing  int
}

func NewPreparedSQLDB(db *sql.DB, capacity int) *PreparedSQLDB {
	if capacity < 0 {
		capacity = 0
	}
	if capacity > 1024 {
		capacity = 1024
	}
	return &PreparedSQLDB{SQLDBAdapter: SQLDBAdapter{DB: db}, capacity: capacity, statements: make(map[string]*sql.Stmt)}
}

func (a *PreparedSQLDB) statement(ctx context.Context, query string) (*sql.Stmt, error) {
	if a == nil || a.DB == nil {
		return nil, ErrMySQLDBRequired
	}
	a.mu.RLock()
	s, closed := a.statements[query], a.closed
	a.mu.RUnlock()
	if closed {
		return nil, sql.ErrConnDone
	}
	// Large batch shapes and parameter-free statements do not occupy slots.
	if len(query) > 4096 || !strings.Contains(query, "?") || strings.Count(query, "?") > 16 {
		return nil, nil
	}
	if s != nil {
		return s, nil
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil, sql.ErrConnDone
	}
	if s = a.statements[query]; s != nil {
		a.mu.Unlock()
		return s, nil
	}
	if len(a.statements)+a.preparing >= a.capacity {
		a.mu.Unlock()
		return nil, nil
	}
	a.preparing++
	a.mu.Unlock()
	var err error
	s, err = a.DB.PrepareContext(ctx, query)
	a.mu.Lock()
	a.preparing--
	if err != nil {
		a.mu.Unlock()
		return nil, err
	}
	if a.closed {
		a.mu.Unlock()
		_ = s.Close()
		return nil, sql.ErrConnDone
	}
	if existing := a.statements[query]; existing != nil {
		a.mu.Unlock()
		_ = s.Close()
		return existing, nil
	}
	a.statements[query] = s
	a.mu.Unlock()
	return s, nil
}

func (a *PreparedSQLDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	s, err := a.statement(ctx, query)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return a.SQLDBAdapter.ExecContext(ctx, query, args...)
	}
	return s.ExecContext(ctx, args...)
}

func (a *PreparedSQLDB) QueryContext(ctx context.Context, query string, args ...any) (SQLRows, error) {
	s, err := a.statement(ctx, query)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return a.SQLDBAdapter.QueryContext(ctx, query, args...)
	}
	return s.QueryContext(ctx, args...)
}

func (a *PreparedSQLDB) QueryRowContext(ctx context.Context, query string, args ...any) SQLRow {
	s, err := a.statement(ctx, query)
	if err != nil {
		return errorRow{err}
	}
	if s == nil {
		return a.SQLDBAdapter.QueryRowContext(ctx, query, args...)
	}
	return s.QueryRowContext(ctx, args...)
}

// Close releases statements only; the caller retains ownership of the pool.
// Stop request admission and finish transactions before calling Close. Calls
// racing shutdown may return database/sql's closed-statement errors.
func (a *PreparedSQLDB) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	statements := a.statements
	a.statements = make(map[string]*sql.Stmt)
	a.mu.Unlock()
	var errs []error
	for _, s := range statements {
		errs = append(errs, s.Close())
	}
	return errors.Join(errs...)
}

func newSQLTransactionDB(tx *sql.Tx, parent SQLDB) sqlTransactionDB {
	r := sqlTransactionDB{tx: tx}
	if p, ok := parent.(*PreparedSQLDB); ok {
		r.prepared = p
		r.local = &transactionStatements{statements: make(map[string]*sql.Stmt)}
	}
	return r
}

type transactionStatements struct {
	mu         sync.Mutex
	statements map[string]*sql.Stmt
}

func (db sqlTransactionDB) preparedStatement(ctx context.Context, query string) (*sql.Stmt, error) {
	if db.prepared == nil || db.local == nil {
		return nil, nil
	}
	if len(query) > 4096 || !strings.Contains(query, "?") || strings.Count(query, "?") > 16 {
		return nil, nil
	}
	db.local.mu.Lock()
	defer db.local.mu.Unlock()
	if s := db.local.statements[query]; s != nil {
		return s, nil
	}
	// Never prepare through the pool while holding a transaction connection:
	// a saturated pool could deadlock. Unadmitted shapes are prepared on the
	// transaction's own connection and retained only for this transaction.
	a := db.prepared
	a.mu.RLock()
	s, closed, capacity := a.statements[query], a.closed, a.capacity
	a.mu.RUnlock()
	if closed {
		return nil, sql.ErrConnDone
	}
	if len(db.local.statements) >= capacity {
		return nil, nil
	}
	var err error
	if s != nil {
		s = db.tx.StmtContext(ctx, s)
	} else {
		s, err = db.tx.PrepareContext(ctx, query)
	}
	if err != nil {
		return nil, err
	}
	// sql.Tx closes these handles automatically on both commit and rollback.
	db.local.statements[query] = s
	return s, nil
}

// Tx.StmtContext reports preparation failures on first execution/Scan.
// Discard a failed derived handle so a retry cannot retain its sticky error.
func (db sqlTransactionDB) forgetFailedStatement(query string, s *sql.Stmt, err error) {
	if err == nil || errors.Is(err, sql.ErrNoRows) || db.local == nil {
		return
	}
	db.local.mu.Lock()
	if db.local.statements[query] == s {
		delete(db.local.statements, query)
	}
	db.local.mu.Unlock()
	_ = s.Close()
}

type transactionPreparedRow struct {
	row       SQLRow
	database  sqlTransactionDB
	query     string
	statement *sql.Stmt
}

func (r transactionPreparedRow) Scan(dest ...any) error {
	err := r.row.Scan(dest...)
	r.database.forgetFailedStatement(r.query, r.statement, err)
	return err
}
