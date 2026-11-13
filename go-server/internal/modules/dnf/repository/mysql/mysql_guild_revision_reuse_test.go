package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

type guildRevisionResult struct {
	value string
	found bool
	err   error
}

// guildRevisionSQLState is a SQL-interface double that scripts the fingerprint
// query and records the physical transaction options. It never touches a live
// database.
type guildRevisionSQLState struct {
	mu                 sync.Mutex
	begins             int
	commits            int
	rollbacks          int
	openTx             bool
	isolation          driver.IsolationLevel
	readOnly           bool
	fingerprintQueries int
	otherQueries       int
	results            []guildRevisionResult
	next               int
}

type guildRevisionSQLConnector struct{ state *guildRevisionSQLState }

func (c guildRevisionSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return guildRevisionSQLConn{state: c.state}, nil
}

func (c guildRevisionSQLConnector) Driver() driver.Driver {
	return guildRevisionSQLDriver{state: c.state}
}

type guildRevisionSQLDriver struct{ state *guildRevisionSQLState }

func (d guildRevisionSQLDriver) Open(string) (driver.Conn, error) {
	return guildRevisionSQLConn{state: d.state}, nil
}

type guildRevisionSQLConn struct{ state *guildRevisionSQLState }

func (c guildRevisionSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected guild revision prepare")
}

func (c guildRevisionSQLConn) Close() error { return nil }

func (c guildRevisionSQLConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c guildRevisionSQLConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	s := c.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.openTx {
		return nil, errors.New("nested guild revision transaction")
	}
	s.begins++
	s.openTx = true
	s.isolation = opts.Isolation
	s.readOnly = opts.ReadOnly
	return guildRevisionSQLTx{state: s}, nil
}

func (c guildRevisionSQLConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.state
	s.mu.Lock()
	if !s.openTx {
		s.mu.Unlock()
		return nil, errors.New("guild revision read escaped its transaction")
	}
	if !strings.Contains(query, "SHA2(v.entry_value, 256)") {
		s.otherQueries++
		s.mu.Unlock()
		return nil, fmt.Errorf("unexpected guild revision query: %s", query)
	}
	if len(args) != 1 || args[0].Value != r.GuildRegistryScope {
		s.mu.Unlock()
		return nil, fmt.Errorf("guild revision scope args=%v", args)
	}
	s.fingerprintQueries++
	index := s.next
	if index < len(s.results) {
		s.next++
	} else if len(s.results) > 0 {
		index = len(s.results) - 1
	} else {
		s.mu.Unlock()
		return nil, errors.New("no guild revision result scripted")
	}
	result := s.results[index]
	s.mu.Unlock()
	if result.err != nil {
		return nil, result.err
	}
	rows := &pairSQLRows{columns: []string{"SHA2(v.entry_value, 256)"}}
	if result.found {
		rows.rows = [][]driver.Value{{result.value}}
	}
	return rows, nil
}

func (c guildRevisionSQLConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("guild revision read attempted a write")
}

type guildRevisionSQLTx struct{ state *guildRevisionSQLState }

func (tx guildRevisionSQLTx) Commit() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.openTx = false
	tx.state.commits++
	return nil
}

func (tx guildRevisionSQLTx) Rollback() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.openTx = false
	tx.state.rollbacks++
	return nil
}

func newGuildRevisionUnitOfWork(state *guildRevisionSQLState) (*mysqlGuildUnitOfWork, *sql.DB) {
	database := sql.OpenDB(guildRevisionSQLConnector{state: state})
	uow := &mysqlGuildUnitOfWork{router: mysqlRouter{
		db:                     SQLDBAdapter{DB: database},
		readDBs:                []string{"replica"},
		writeDBs:               []string{"writer"},
		tablePrefix:            "dnf_",
		guildSnapshotNamespace: &r.GuildSnapshotNamespace{},
	}}
	return uow, database
}

func loadGuildRevisionFromView(ctx context.Context, view r.GuildReadView) (string, bool, error) {
	reader, ok := view.Settings.(r.GuildSnapshotRevisionReader)
	if !ok {
		return "", false, errors.New("guild read settings do not expose a revision reader")
	}
	return reader.LoadGuildSnapshotRevision(ctx)
}

// A cache miss in loadGuildReadSnapshot reads the fingerprint before, inside
// and after the registry load. All three belong to one repeatable-read view.
func TestGuildReadRevisionReusedWithinRepeatableReadTransaction(t *testing.T) {
	state := &guildRevisionSQLState{results: []guildRevisionResult{{value: "revision-1", found: true}}}
	uow, database := newGuildRevisionUnitOfWork(state)
	defer database.Close()
	ctx := context.Background()
	err := uow.WithinGuildRead(ctx, func(view r.GuildReadView) error {
		for i := 0; i < 3; i++ {
			revision, found, loadErr := loadGuildRevisionFromView(ctx, view)
			if loadErr != nil || !found || revision != "revision-1" {
				return fmt.Errorf("revision %d = %q found=%v err=%w", i, revision, found, loadErr)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.fingerprintQueries != 1 {
		t.Fatalf("fingerprint queries=%d, want one per repeatable-read transaction", state.fingerprintQueries)
	}
	if state.otherQueries != 0 || state.begins != 1 || state.commits != 1 || state.rollbacks != 0 || state.openTx {
		t.Fatalf("transaction state=%+v", state)
	}
	if state.isolation != driver.IsolationLevel(sql.LevelRepeatableRead) || !state.readOnly {
		t.Fatalf("snapshot isolation=%v readonly=%v", state.isolation, state.readOnly)
	}
}

func TestGuildReadRevisionNotReusedAcrossTransactions(t *testing.T) {
	state := &guildRevisionSQLState{results: []guildRevisionResult{{value: "revision-1", found: true}}}
	uow, database := newGuildRevisionUnitOfWork(state)
	defer database.Close()
	ctx := context.Background()
	for transaction := 0; transaction < 2; transaction++ {
		err := uow.WithinGuildRead(ctx, func(view r.GuildReadView) error {
			for i := 0; i < 2; i++ {
				revision, found, loadErr := loadGuildRevisionFromView(ctx, view)
				if loadErr != nil || !found || revision != "revision-1" {
					return fmt.Errorf("transaction %d revision %d = %q found=%v err=%w", transaction, i, revision, found, loadErr)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.fingerprintQueries != 2 {
		t.Fatalf("fingerprint queries=%d, want a fresh read for each transaction", state.fingerprintQueries)
	}
	if state.begins != 2 || state.commits != 2 || state.rollbacks != 0 || state.openTx {
		t.Fatalf("transaction state=%+v", state)
	}
}

func TestGuildReadRevisionErrorsAreNotCached(t *testing.T) {
	transient := errors.New("transient revision failure")
	state := &guildRevisionSQLState{results: []guildRevisionResult{
		{err: transient},
		{value: "revision-after-error", found: true},
	}}
	uow, database := newGuildRevisionUnitOfWork(state)
	defer database.Close()
	ctx := context.Background()
	err := uow.WithinGuildRead(ctx, func(view r.GuildReadView) error {
		if _, _, loadErr := loadGuildRevisionFromView(ctx, view); !errors.Is(loadErr, transient) {
			return fmt.Errorf("first revision error=%v, want %v", loadErr, transient)
		}
		revision, found, loadErr := loadGuildRevisionFromView(ctx, view)
		if loadErr != nil || !found || revision != "revision-after-error" {
			return fmt.Errorf("retry revision=%q found=%v err=%w", revision, found, loadErr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.fingerprintQueries != 2 {
		t.Fatalf("fingerprint queries=%d, want the failed read to be retried", state.fingerprintQueries)
	}
	if state.commits != 1 || state.rollbacks != 0 || state.openTx {
		t.Fatalf("transaction state=%+v", state)
	}
}

func TestGuildReadRevisionEscapedViewDoesNotReuseMemo(t *testing.T) {
	state := &guildRevisionSQLState{results: []guildRevisionResult{{value: "revision-1", found: true}}}
	uow, database := newGuildRevisionUnitOfWork(state)
	defer database.Close()
	ctx := context.Background()
	var escaped r.GuildReadView
	err := uow.WithinGuildRead(ctx, func(view r.GuildReadView) error {
		escaped = view
		revision, found, loadErr := loadGuildRevisionFromView(ctx, view)
		if loadErr != nil || !found || revision != "revision-1" {
			return fmt.Errorf("revision=%q found=%v err=%w", revision, found, loadErr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if revision, found, loadErr := loadGuildRevisionFromView(ctx, escaped); loadErr == nil {
		t.Fatalf("escaped view reused revision=%q found=%v", revision, found)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.fingerprintQueries != 1 {
		t.Fatalf("fingerprint queries=%d, want no post-transaction reuse", state.fingerprintQueries)
	}
}

// Write transactions and ReadCommitted paths construct the plain settings
// store, so they must keep issuing a fresh fingerprint on every call.
func TestGuildReadRevisionPlainStoreDoesNotCache(t *testing.T) {
	database := &fakeSQLDB{rows: []fakeSQLRow{
		{values: []any{"revision-1"}},
		{values: []any{"revision-1"}},
	}}
	store := &mysqlSettingsStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{
		db:          database,
		readDBs:     []string{"writer"},
		writeDBs:    []string{"writer"},
		tablePrefix: "dnf_",
	}}}
	for i := 0; i < 2; i++ {
		revision, found, err := store.LoadGuildSnapshotRevision(context.Background())
		if err != nil || !found || revision != "revision-1" {
			t.Fatalf("revision %d = %q found=%v err=%v", i, revision, found, err)
		}
	}
	if len(database.execs) != 2 {
		t.Fatalf("plain store queries=%d, want no cache outside the guild read transaction", len(database.execs))
	}
}
