package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

var errPreparedSQLFixturePrepare = errors.New("prepared SQL fixture prepare failure")

func TestPreparedSQLPoolWaitDoesNotLockTransaction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	db, _ := openPreparedSQLFixture(t)
	a := NewPreparedSQLDB(db, 4)
	defer a.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	waiting := make(chan error, 1)
	go func() { _, err := a.ExecContext(ctx, "UPDATE waiting SET n=?", 1); waiting <- err }()
	for db.Stats().WaitCount == 0 && ctx.Err() == nil {
		time.Sleep(time.Millisecond)
	}
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	transaction := newSQLTransactionDB(tx, a)
	if _, err := transaction.ExecContext(ctx, "UPDATE own_connection SET n=?", 1); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-waiting; err != nil {
		t.Fatal(err)
	}
}

func TestPreparedSQLFailedDerivedHandleCanRetry(t *testing.T) {
	ctx := context.Background()
	db, state := openPreparedSQLFixture(t)
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	a := NewPreparedSQLDB(db, 4)
	defer a.Close()
	query := "UPDATE retry_fixture SET n=?"
	if _, err := a.ExecContext(ctx, query, 1); err != nil {
		t.Fatal(err)
	}
	occupied, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	state.failNextPrepare(query)
	transaction := newSQLTransactionDB(tx, a)
	if _, err := transaction.ExecContext(ctx, query, 2); !errors.Is(err, errPreparedSQLFixturePrepare) {
		t.Fatalf("first error=%v", err)
	}
	if preparedSQLTransactionStatement(transaction, query) != nil {
		t.Fatal("failed handle retained")
	}
	if _, err := transaction.ExecContext(ctx, query, 3); err != nil {
		t.Fatal(err)
	}
}

type preparedSQLFixtureState struct {
	mu              sync.Mutex
	prepareCalls    int
	prepareQueries  []string
	failPrepare     map[string]int
	rawExecCalls    int
	stmtExecCalls   int
	stmtCloseCalls  int
	connectionOpens int
	connectionClose int
	begins          int
	commits         int
	rollbacks       int
}

type preparedSQLFixtureCounts struct {
	prepareCalls    int
	prepareQueries  []string
	rawExecCalls    int
	stmtExecCalls   int
	stmtCloseCalls  int
	connectionOpens int
	connectionClose int
	begins          int
	commits         int
	rollbacks       int
}

func (s *preparedSQLFixtureState) snapshot() preparedSQLFixtureCounts {
	s.mu.Lock()
	defer s.mu.Unlock()
	return preparedSQLFixtureCounts{
		prepareCalls:    s.prepareCalls,
		prepareQueries:  append([]string(nil), s.prepareQueries...),
		rawExecCalls:    s.rawExecCalls,
		stmtExecCalls:   s.stmtExecCalls,
		stmtCloseCalls:  s.stmtCloseCalls,
		connectionOpens: s.connectionOpens,
		connectionClose: s.connectionClose,
		begins:          s.begins,
		commits:         s.commits,
		rollbacks:       s.rollbacks,
	}
}

func (s *preparedSQLFixtureState) failNextPrepare(query string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failPrepare == nil {
		s.failPrepare = make(map[string]int)
	}
	s.failPrepare[query]++
}

func (s *preparedSQLFixtureState) openConnection() *preparedSQLFixtureConn {
	s.mu.Lock()
	s.connectionOpens++
	s.mu.Unlock()
	return &preparedSQLFixtureConn{state: s}
}

type preparedSQLFixtureConnector struct {
	state *preparedSQLFixtureState
}

func (c preparedSQLFixtureConnector) Connect(context.Context) (driver.Conn, error) {
	return c.state.openConnection(), nil
}

func (c preparedSQLFixtureConnector) Driver() driver.Driver {
	return preparedSQLFixtureDriver{state: c.state}
}

type preparedSQLFixtureDriver struct {
	state *preparedSQLFixtureState
}

func (d preparedSQLFixtureDriver) Open(string) (driver.Conn, error) {
	return d.state.openConnection(), nil
}

type preparedSQLFixtureConn struct {
	state  *preparedSQLFixtureState
	tx     *preparedSQLFixtureTx
	closed bool
}

func (c *preparedSQLFixtureConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *preparedSQLFixtureConn) PrepareContext(_ context.Context, query string) (driver.Stmt, error) {
	c.state.mu.Lock()
	c.state.prepareCalls++
	c.state.prepareQueries = append(c.state.prepareQueries, query)
	if c.state.failPrepare[query] > 0 {
		c.state.failPrepare[query]--
		c.state.mu.Unlock()
		return nil, errPreparedSQLFixturePrepare
	}
	c.state.mu.Unlock()
	return &preparedSQLFixtureStmt{state: c.state, query: query}, nil
}

func (c *preparedSQLFixtureConn) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	c.state.mu.Lock()
	c.state.connectionClose++
	c.state.mu.Unlock()
	return nil
}

func (c *preparedSQLFixtureConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *preparedSQLFixtureConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if c.tx != nil {
		return nil, errors.New("overlapping prepared SQL fixture transaction")
	}
	tx := &preparedSQLFixtureTx{conn: c}
	c.tx = tx
	c.state.mu.Lock()
	c.state.begins++
	c.state.mu.Unlock()
	return tx, nil
}

func (c *preparedSQLFixtureConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	c.state.rawExecCalls++
	c.state.mu.Unlock()
	return driver.RowsAffected(1), nil
}

type preparedSQLFixtureTx struct {
	conn *preparedSQLFixtureConn
	done bool
}

func (tx *preparedSQLFixtureTx) Commit() error {
	return tx.finish(true)
}

func (tx *preparedSQLFixtureTx) Rollback() error {
	return tx.finish(false)
}

func (tx *preparedSQLFixtureTx) finish(commit bool) error {
	if tx.done || tx.conn.tx != tx {
		return errors.New("inactive prepared SQL fixture transaction")
	}
	tx.done = true
	tx.conn.tx = nil
	tx.conn.state.mu.Lock()
	if commit {
		tx.conn.state.commits++
	} else {
		tx.conn.state.rollbacks++
	}
	tx.conn.state.mu.Unlock()
	return nil
}

type preparedSQLFixtureStmt struct {
	state  *preparedSQLFixtureState
	query  string
	mu     sync.Mutex
	closed bool
}

func (s *preparedSQLFixtureStmt) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.state.mu.Lock()
	s.state.stmtCloseCalls++
	s.state.mu.Unlock()
	return nil
}

func (*preparedSQLFixtureStmt) NumInput() int {
	return -1
}

func (s *preparedSQLFixtureStmt) Exec([]driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), nil)
}

func (s *preparedSQLFixtureStmt) Query([]driver.Value) (driver.Rows, error) {
	return nil, errors.New("prepared SQL fixture query is not implemented")
}

func (s *preparedSQLFixtureStmt) ExecContext(context.Context, []driver.NamedValue) (driver.Result, error) {
	s.state.mu.Lock()
	s.state.stmtExecCalls++
	s.state.mu.Unlock()
	return driver.RowsAffected(1), nil
}

func (s *preparedSQLFixtureStmt) QueryContext(context.Context, []driver.NamedValue) (driver.Rows, error) {
	return nil, errors.New("prepared SQL fixture query is not implemented")
}

var (
	_ driver.Connector          = preparedSQLFixtureConnector{}
	_ driver.Driver             = preparedSQLFixtureDriver{}
	_ driver.Conn               = (*preparedSQLFixtureConn)(nil)
	_ driver.ConnPrepareContext = (*preparedSQLFixtureConn)(nil)
	_ driver.ConnBeginTx        = (*preparedSQLFixtureConn)(nil)
	_ driver.ExecerContext      = (*preparedSQLFixtureConn)(nil)
	_ driver.Stmt               = (*preparedSQLFixtureStmt)(nil)
	_ driver.StmtExecContext    = (*preparedSQLFixtureStmt)(nil)
	_ driver.StmtQueryContext   = (*preparedSQLFixtureStmt)(nil)
	_ driver.Tx                 = (*preparedSQLFixtureTx)(nil)
)

func openPreparedSQLFixture(t *testing.T) (*sql.DB, *preparedSQLFixtureState) {
	t.Helper()
	state := &preparedSQLFixtureState{}
	db := sql.OpenDB(preparedSQLFixtureConnector{state: state})
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close fixture database: %v", err)
		}
	})
	return db, state
}

func preparedSQLCachedStatement(a *PreparedSQLDB, query string) *sql.Stmt {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.statements[query]
}

func preparedSQLCacheSize(a *PreparedSQLDB) int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.statements)
}

func preparedSQLTransactionStatement(db sqlTransactionDB, query string) *sql.Stmt {
	if db.local == nil {
		return nil
	}
	db.local.mu.Lock()
	defer db.local.mu.Unlock()
	return db.local.statements[query]
}

func TestPreparedSQLDBReusesStatementForRepeatedShape(t *testing.T) {
	ctx := context.Background()
	db, state := openPreparedSQLFixture(t)
	adapter := NewPreparedSQLDB(db, 2)
	query := "UPDATE prepared_sql_fixture SET value = ? WHERE id = ?"

	for _, value := range []int64{1, 2} {
		if _, err := adapter.ExecContext(ctx, query, value, int64(7)); err != nil {
			t.Fatalf("exec cached shape: %v", err)
		}
	}

	counts := state.snapshot()
	if counts.prepareCalls != 1 {
		t.Fatalf("prepare calls = %d, want 1; queries = %v", counts.prepareCalls, counts.prepareQueries)
	}
	if counts.stmtExecCalls != 2 || counts.rawExecCalls != 0 {
		t.Fatalf("statement/raw exec calls = %d/%d, want 2/0", counts.stmtExecCalls, counts.rawExecCalls)
	}
	if got := preparedSQLCacheSize(adapter); got != 1 {
		t.Fatalf("prepared cache size = %d, want 1", got)
	}
}

func TestPreparedSQLDBFullCapacityFallsBackWithoutGrowingCache(t *testing.T) {
	ctx := context.Background()
	db, state := openPreparedSQLFixture(t)
	adapter := NewPreparedSQLDB(db, 1)
	first := "UPDATE prepared_sql_fixture SET first_value = ? WHERE id = ?"
	second := "UPDATE prepared_sql_fixture SET second_value = ? WHERE id = ?"

	if _, err := adapter.ExecContext(ctx, first, int64(1), int64(2)); err != nil {
		t.Fatalf("prepare first shape: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := adapter.ExecContext(ctx, second, int64(i), int64(2)); err != nil {
			t.Fatalf("execute overflow shape: %v", err)
		}
	}

	counts := state.snapshot()
	if counts.prepareCalls != 1 || counts.stmtExecCalls != 1 || counts.rawExecCalls != 2 {
		t.Fatalf("prepare/statement/raw calls = %d/%d/%d, want 1/1/2", counts.prepareCalls, counts.stmtExecCalls, counts.rawExecCalls)
	}
	if got := preparedSQLCacheSize(adapter); got != 1 {
		t.Fatalf("prepared cache size = %d, want 1", got)
	}
	if preparedSQLCachedStatement(adapter, first) == nil || preparedSQLCachedStatement(adapter, second) != nil {
		t.Fatal("cache contents changed after overflow fallback")
	}
}

func TestPreparedSQLDBPrepareFailureDoesNotPolluteCache(t *testing.T) {
	ctx := context.Background()
	db, state := openPreparedSQLFixture(t)
	adapter := NewPreparedSQLDB(db, 2)
	query := "UPDATE prepared_sql_fixture SET value = ? WHERE id = ?"
	state.failNextPrepare(query)

	if _, err := adapter.ExecContext(ctx, query, int64(1), int64(2)); !errors.Is(err, errPreparedSQLFixturePrepare) {
		t.Fatalf("first exec error = %v, want fixture prepare failure", err)
	}
	if got := preparedSQLCacheSize(adapter); got != 0 {
		t.Fatalf("cache size after failed prepare = %d, want 0", got)
	}

	for i := 0; i < 2; i++ {
		if _, err := adapter.ExecContext(ctx, query, int64(i), int64(2)); err != nil {
			t.Fatalf("retry exec %d: %v", i, err)
		}
	}
	counts := state.snapshot()
	if counts.prepareCalls != 2 || counts.stmtExecCalls != 2 {
		t.Fatalf("prepare/statement calls = %d/%d, want 2/2", counts.prepareCalls, counts.stmtExecCalls)
	}
	if got := preparedSQLCacheSize(adapter); got != 1 {
		t.Fatalf("cache size after retry = %d, want 1", got)
	}
}

func TestPreparedSQLDBCloseIsIdempotentAndKeepsCallerDatabaseOpen(t *testing.T) {
	ctx := context.Background()
	db, state := openPreparedSQLFixture(t)
	adapter := NewPreparedSQLDB(db, 1)
	query := "UPDATE prepared_sql_fixture SET value = ? WHERE id = ?"

	if _, err := adapter.ExecContext(ctx, query, int64(1), int64(2)); err != nil {
		t.Fatalf("prepare cached shape: %v", err)
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if got := preparedSQLCacheSize(adapter); got != 0 {
		t.Fatalf("cache size after close = %d, want 0", got)
	}
	counts := state.snapshot()
	if counts.stmtCloseCalls != 1 {
		t.Fatalf("statement close calls = %d, want 1", counts.stmtCloseCalls)
	}
	if counts.connectionClose != 0 {
		t.Fatalf("caller connection closes = %d, want 0", counts.connectionClose)
	}

	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("caller database ping after adapter close: %v", err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM prepared_sql_fixture WHERE id = 1"); err != nil {
		t.Fatalf("caller database exec after adapter close: %v", err)
	}
}

func TestPreparedSQLDBOverSixteenParametersBypassCache(t *testing.T) {
	ctx := context.Background()
	db, state := openPreparedSQLFixture(t)
	adapter := NewPreparedSQLDB(db, 4)
	const parameterCount = 17
	query := "INSERT INTO prepared_sql_fixture VALUES (" + strings.Repeat("?,", parameterCount-1) + "?)"
	args := make([]any, parameterCount)
	for i := range args {
		args[i] = i
	}

	for i := 0; i < 2; i++ {
		if _, err := adapter.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("execute wide shape %d: %v", i, err)
		}
	}

	counts := state.snapshot()
	if counts.prepareCalls != 0 || counts.stmtExecCalls != 0 || counts.rawExecCalls != 2 {
		t.Fatalf("prepare/statement/raw calls = %d/%d/%d, want 0/0/2", counts.prepareCalls, counts.stmtExecCalls, counts.rawExecCalls)
	}
	if got := preparedSQLCacheSize(adapter); got != 0 {
		t.Fatalf("cache size for 17 parameters = %d, want 0", got)
	}
}

func TestPreparedSQLDBTransactionReusesShapesAndRollbackKeepsPoolCache(t *testing.T) {
	ctx := context.Background()
	db, state := openPreparedSQLFixture(t)
	adapter := NewPreparedSQLDB(db, 2)
	cachedQuery := "UPDATE prepared_sql_fixture SET cached_value = ? WHERE id = ?"
	transactionOnlyQuery := "UPDATE prepared_sql_fixture SET tx_value = ? WHERE id = ?"

	if _, err := adapter.ExecContext(ctx, cachedQuery, int64(1), int64(2)); err != nil {
		t.Fatalf("prime pool statement: %v", err)
	}
	poolStatement := preparedSQLCachedStatement(adapter, cachedQuery)
	if poolStatement == nil {
		t.Fatal("primed pool statement is missing")
	}

	tx1, err := adapter.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin first transaction: %v", err)
	}
	txDB1 := newSQLTransactionDB(tx1, adapter)
	for i := 0; i < 2; i++ {
		if _, err := txDB1.ExecContext(ctx, cachedQuery, int64(i), int64(2)); err != nil {
			t.Fatalf("execute cached shape in first transaction %d: %v", i, err)
		}
	}
	tx1Statement := preparedSQLTransactionStatement(txDB1, cachedQuery)
	if tx1Statement == nil {
		t.Fatal("first transaction did not cache its statement")
	}
	if tx1Statement == poolStatement {
		t.Fatal("transaction statement unexpectedly replaced the pool statement handle")
	}
	counts := state.snapshot()
	if counts.prepareCalls != 1 {
		t.Fatalf("prepare calls while reusing pool shape = %d, want 1", counts.prepareCalls)
	}
	if err := tx1.Rollback(); err != nil {
		t.Fatalf("rollback first transaction: %v", err)
	}
	if _, err := tx1Statement.ExecContext(ctx, int64(3), int64(2)); err == nil {
		t.Fatal("transaction statement remained usable after rollback")
	}
	counts = state.snapshot()
	if counts.stmtCloseCalls != 0 {
		t.Fatalf("pool-backed transaction rollback closed %d driver statements, want 0", counts.stmtCloseCalls)
	}
	if _, err := adapter.ExecContext(ctx, cachedQuery, int64(3), int64(2)); err != nil {
		t.Fatalf("pool statement after first rollback: %v", err)
	}
	if got := preparedSQLCachedStatement(adapter, cachedQuery); got != poolStatement {
		t.Fatal("pool statement handle changed after transaction rollback")
	}
	if counts := state.snapshot(); counts.prepareCalls != 1 {
		t.Fatalf("pool statement was prepared again after rollback: %d calls", counts.prepareCalls)
	}

	tx2, err := adapter.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin second transaction: %v", err)
	}
	txDB2 := newSQLTransactionDB(tx2, adapter)
	for i := 0; i < 2; i++ {
		if _, err := txDB2.ExecContext(ctx, transactionOnlyQuery, int64(i), int64(2)); err != nil {
			t.Fatalf("execute transaction-only shape %d: %v", i, err)
		}
	}
	tx2Statement := preparedSQLTransactionStatement(txDB2, transactionOnlyQuery)
	if tx2Statement == nil {
		t.Fatal("second transaction did not cache its statement")
	}
	counts = state.snapshot()
	if counts.prepareCalls != 2 {
		t.Fatalf("transaction-only shape prepare calls = %d, want 2 total", counts.prepareCalls)
	}
	if err := tx2.Rollback(); err != nil {
		t.Fatalf("rollback second transaction: %v", err)
	}
	if _, err := tx2Statement.ExecContext(ctx, int64(3), int64(2)); err == nil {
		t.Fatal("transaction-only statement remained usable after rollback")
	}
	counts = state.snapshot()
	if counts.stmtCloseCalls != 1 {
		t.Fatalf("rollback driver statement closes = %d, want 1", counts.stmtCloseCalls)
	}
	if _, err := adapter.ExecContext(ctx, cachedQuery, int64(4), int64(2)); err != nil {
		t.Fatalf("pool statement after second rollback: %v", err)
	}
	counts = state.snapshot()
	if counts.prepareCalls != 2 || counts.rawExecCalls != 0 {
		t.Fatalf("prepare/raw calls after rollback = %d/%d, want 2/0", counts.prepareCalls, counts.rawExecCalls)
	}
	if got := preparedSQLCacheSize(adapter); got != 1 {
		t.Fatalf("pool cache size after rollback = %d, want 1", got)
	}
}
