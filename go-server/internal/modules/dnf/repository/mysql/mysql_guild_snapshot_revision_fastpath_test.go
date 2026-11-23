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

	mysqldriver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/repository"
)

type guildFastQueryResult struct {
	err    error
	values []driver.Value
}

type guildFastSQLState struct {
	mu        sync.Mutex
	begins    int
	commits   int
	rollbacks int
	openTx    bool
	queries   []string
	fast      []guildFastQueryResult
	source    []guildFastQueryResult
	nextFast  int
	nextSrc   int
}

type guildFastSQLConnector struct{ state *guildFastSQLState }

func (c guildFastSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return guildFastSQLConn{state: c.state}, nil
}

func (c guildFastSQLConnector) Driver() driver.Driver {
	return guildFastSQLDriver{state: c.state}
}

type guildFastSQLDriver struct{ state *guildFastSQLState }

func (d guildFastSQLDriver) Open(string) (driver.Conn, error) {
	return guildFastSQLConn{state: d.state}, nil
}

type guildFastSQLConn struct{ state *guildFastSQLState }

func (c guildFastSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected guild fast-path prepare")
}

func (c guildFastSQLConn) Close() error { return nil }

func (c guildFastSQLConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c guildFastSQLConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if c.state.openTx {
		return nil, errors.New("nested guild fast-path transaction")
	}
	c.state.begins++
	c.state.openTx = true
	return guildFastSQLTx{state: c.state}, nil
}

func (c guildFastSQLConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	state := c.state
	state.mu.Lock()
	defer state.mu.Unlock()
	state.queries = append(state.queries, query)
	if len(args) != 1 || args[0].Value != repository.GuildRegistryScope {
		return nil, fmt.Errorf("guild fast-path scope args=%v", args)
	}
	switch {
	case strings.Contains(query, "SELECT EXISTS(SELECT 1 FROM"):
		result := nextGuildFastResult(state.fast, &state.nextFast)
		if result.err != nil {
			return nil, result.err
		}
		rows := &pairSQLRows{columns: []string{"exists", "fingerprint"}}
		if result.values != nil {
			rows.rows = [][]driver.Value{result.values}
		}
		return rows, nil
	case strings.Contains(query, "SHA2(v.entry_value, 256)"):
		result := nextGuildFastResult(state.source, &state.nextSrc)
		if result.err != nil {
			return nil, result.err
		}
		rows := &pairSQLRows{columns: []string{"SHA2(v.entry_value, 256)"}}
		if result.values != nil {
			rows.rows = [][]driver.Value{result.values}
		}
		return rows, nil
	default:
		return nil, fmt.Errorf("unexpected guild fast-path query: %s", query)
	}
}

func (c guildFastSQLConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("guild fast-path read attempted a write")
}

func nextGuildFastResult(results []guildFastQueryResult, index *int) guildFastQueryResult {
	if len(results) == 0 {
		return guildFastQueryResult{err: errors.New("no guild fast-path result scripted")}
	}
	if *index >= len(results) {
		return results[len(results)-1]
	}
	result := results[*index]
	*index = *index + 1
	return result
}

type guildFastSQLTx struct{ state *guildFastSQLState }

func (tx guildFastSQLTx) Commit() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.openTx = false
	tx.state.commits++
	return nil
}

func (tx guildFastSQLTx) Rollback() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.openTx = false
	tx.state.rollbacks++
	return nil
}

func newGuildFastPathStore(state *guildFastSQLState) (*mysqlSettingsStore, *sql.DB) {
	database := sql.OpenDB(guildFastSQLConnector{state: state})
	store := &mysqlSettingsStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{
		db:                     SQLDBAdapter{DB: database},
		readDBs:                []string{"guild"},
		writeDBs:               []string{"guild"},
		tablePrefix:            "dnf",
		storedGuildFingerprint: true,
	}}}
	return store, database
}

func guildFastStateSnapshot(state *guildFastSQLState) (begins, commits, rollbacks int, queries []string, open bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.begins, state.commits, state.rollbacks, append([]string(nil), state.queries...), state.openTx
}

func TestGuildRevisionFastPathSidecarHitUsesNoTransaction(t *testing.T) {
	state := &guildFastSQLState{fast: []guildFastQueryResult{{values: []driver.Value{true, "digest"}}}}
	store, database := newGuildFastPathStore(state)
	defer database.Close()
	digest, found, err := store.LoadGuildSnapshotRevision(context.Background())
	if err != nil || !found || digest != "digest" {
		t.Fatalf("digest=%q found=%t err=%v", digest, found, err)
	}
	begins, commits, rollbacks, queries, open := guildFastStateSnapshot(state)
	if begins != 0 || commits != 0 || rollbacks != 0 || open || len(queries) != 1 {
		t.Fatalf("transaction state begins=%d commits=%d rollbacks=%d open=%t queries=%d", begins, commits, rollbacks, open, len(queries))
	}
}

func TestGuildRevisionFastPathOrphanGuardUsesNoTransaction(t *testing.T) {
	state := &guildFastSQLState{fast: []guildFastQueryResult{{values: []driver.Value{false, "orphan"}}}}
	store, database := newGuildFastPathStore(state)
	defer database.Close()
	digest, found, err := store.LoadGuildSnapshotRevision(context.Background())
	if err != nil || !found || digest != "" {
		t.Fatalf("digest=%q found=%t err=%v", digest, found, err)
	}
	begins, _, _, queries, _ := guildFastStateSnapshot(state)
	if begins != 0 || len(queries) != 1 {
		t.Fatalf("begins=%d queries=%d", begins, len(queries))
	}
}

func TestGuildRevisionFastPathMissingSidecarFallbackSharesSnapshot(t *testing.T) {
	state := &guildFastSQLState{
		fast: []guildFastQueryResult{
			{values: []driver.Value{true, nil}},
			{values: []driver.Value{true, nil}},
		},
		source: []guildFastQueryResult{{values: []driver.Value{"computed-from-source"}}},
	}
	store, database := newGuildFastPathStore(state)
	defer database.Close()
	digest, found, err := store.LoadGuildSnapshotRevision(context.Background())
	if err != nil || !found || digest != "computed-from-source" {
		t.Fatalf("digest=%q found=%t err=%v", digest, found, err)
	}
	begins, commits, rollbacks, queries, open := guildFastStateSnapshot(state)
	if begins != 1 || commits != 1 || rollbacks != 0 || open || len(queries) != 3 {
		t.Fatalf("transaction state begins=%d commits=%d rollbacks=%d open=%t queries=%d", begins, commits, rollbacks, open, len(queries))
	}
}

func TestGuildRevisionFastPathOptionalSchemaFailureFallsBackWithoutTransaction(t *testing.T) {
	state := &guildFastSQLState{
		fast:   []guildFastQueryResult{{err: &mysqldriver.MySQLError{Number: 1146}}},
		source: []guildFastQueryResult{{values: []driver.Value{"digest"}}},
	}
	store, database := newGuildFastPathStore(state)
	defer database.Close()
	digest, found, err := store.LoadGuildSnapshotRevision(context.Background())
	if err != nil || !found || digest != "digest" {
		t.Fatalf("digest=%q found=%t err=%v", digest, found, err)
	}
	begins, _, _, queries, _ := guildFastStateSnapshot(state)
	if begins != 0 || len(queries) != 2 || !strings.Contains(queries[1], "SHA2(v.entry_value, 256)") {
		t.Fatalf("begins=%d queries=%v", begins, queries)
	}
}

func TestGuildRevisionFastPathInsideCallerTransactionDoesNotBeginAnother(t *testing.T) {
	state := &guildFastSQLState{fast: []guildFastQueryResult{{values: []driver.Value{true, "digest"}}}}
	store, database := newGuildFastPathStore(state)
	defer database.Close()
	tx, err := database.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	store.router.db = newSQLTransactionDB(tx, store.router.db)
	digest, found, err := store.LoadGuildSnapshotRevision(context.Background())
	if err != nil || !found || digest != "digest" {
		t.Fatalf("digest=%q found=%t err=%v", digest, found, err)
	}
	begins, commits, _, queries, _ := guildFastStateSnapshot(state)
	if begins != 1 || commits != 0 || len(queries) != 1 {
		t.Fatalf("caller transaction begins=%d commits=%d queries=%d", begins, commits, len(queries))
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestGuildRevisionFastPathMissingScopeRowNeedsNoTransaction(t *testing.T) {
	state := &guildFastSQLState{fast: []guildFastQueryResult{{}}}
	store, database := newGuildFastPathStore(state)
	defer database.Close()
	digest, found, err := store.LoadGuildSnapshotRevision(context.Background())
	if err != nil || found || digest != "" {
		t.Fatalf("digest=%q found=%t err=%v", digest, found, err)
	}
	begins, _, _, queries, _ := guildFastStateSnapshot(state)
	if begins != 0 || len(queries) != 1 {
		t.Fatalf("begins=%d queries=%d", begins, len(queries))
	}
}
