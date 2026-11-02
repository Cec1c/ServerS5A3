package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
)

var (
	errGuildReadBatchQuery = errors.New("guild read second batch query failed")
	errGuildReadBatchRows  = errors.New("guild read second batch rows failed")
	errGuildReadBatchClose = errors.New("guild read second batch close failed")
)

// guildReadBatchSQLState reuses the transaction counter and row fixtures; it
// adds only the options and batching assertions needed by this read path.
type guildReadBatchSQLState struct {
	semesterSQLState
	readOnly       bool
	isolation      driver.IsolationLevel
	openTx         bool
	batches        []int
	outsideQueries int
	scenario       string
}

func newGuildReadBatchSQLState(scenario string) *guildReadBatchSQLState {
	return &guildReadBatchSQLState{
		semesterSQLState: semesterSQLState{rollbackDone: make(chan struct{})},
		scenario:         scenario,
	}
}

type guildReadBatchSQLConnector struct{ state *guildReadBatchSQLState }

func (c guildReadBatchSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return guildReadBatchSQLConn{state: c.state}, nil
}

func (c guildReadBatchSQLConnector) Driver() driver.Driver {
	return guildReadBatchSQLDriver{state: c.state}
}

type guildReadBatchSQLDriver struct{ state *guildReadBatchSQLState }

func (d guildReadBatchSQLDriver) Open(string) (driver.Conn, error) {
	return guildReadBatchSQLConn{state: d.state}, nil
}

type guildReadBatchSQLConn struct{ state *guildReadBatchSQLState }

func (c guildReadBatchSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected guild read prepare")
}

func (c guildReadBatchSQLConn) Close() error { return nil }

func (c guildReadBatchSQLConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c guildReadBatchSQLConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	s := c.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.openTx {
		return nil, errors.New("nested guild read transaction")
	}
	s.begins++
	s.openTx = true
	s.isolation = opts.Isolation
	s.readOnly = opts.ReadOnly
	return guildReadBatchSQLTx{state: s}, nil
}

func (c guildReadBatchSQLConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.state
	s.mu.Lock()
	if !s.openTx {
		s.outsideQueries++
		s.mu.Unlock()
		return nil, errors.New("guild read escaped its snapshot")
	}
	s.queries = append(s.queries, query)
	batchNumber := len(s.batches) + 1
	s.batches = append(s.batches, len(args))
	scenario := s.scenario
	s.mu.Unlock()

	if scenario == "query" && batchNumber == 2 {
		return nil, errGuildReadBatchQuery
	}
	rows := &pairSQLRows{columns: []string{"character_id", "account_id", "name", "job", "level", "grow_type", "delete_flag"}}
	for _, arg := range args {
		id, ok := arg.Value.(string)
		if !ok {
			return nil, fmt.Errorf("guild read id type %T", arg.Value)
		}
		deleteFlag := int64(0)
		if id == "1" {
			deleteFlag = 1
		}
		rows.rows = append(rows.rows, []driver.Value{id, "account-" + id, "member-" + id, "1", int64(90), int64(33), deleteFlag})
	}
	wrapped := &guildReadBatchSQLRows{pairSQLRows: rows}
	if batchNumber == 2 {
		switch scenario {
		case "rows":
			wrapped.rowErr = errGuildReadBatchRows
		case "close":
			wrapped.closeErr = errGuildReadBatchClose
		}
	}
	return wrapped, nil
}

func (c guildReadBatchSQLConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("guild read attempted a write")
}

type guildReadBatchSQLTx struct{ state *guildReadBatchSQLState }

func (tx guildReadBatchSQLTx) Commit() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.openTx = false
	tx.state.commits++
	return nil
}

func (tx guildReadBatchSQLTx) Rollback() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.openTx = false
	tx.state.rollbacks++
	select {
	case <-tx.state.rollbackDone:
	default:
		close(tx.state.rollbackDone)
	}
	return nil
}

type guildReadBatchSQLRows struct {
	*pairSQLRows
	rowErr   error
	closeErr error
}

func (r *guildReadBatchSQLRows) Next(dest []driver.Value) error {
	err := r.pairSQLRows.Next(dest)
	if errors.Is(err, io.EOF) && r.rowErr != nil {
		err, r.rowErr = r.rowErr, nil
	}
	return err
}

func (r *guildReadBatchSQLRows) Close() error {
	return errors.Join(r.pairSQLRows.Close(), r.closeErr)
}

func TestGuildReadBatchesAt256And257InReadOnlySnapshot(t *testing.T) {
	for _, tc := range []struct {
		name        string
		count       int
		wantBatches []int
	}{
		{name: "256", count: 256, wantBatches: []int{256}},
		{name: "257", count: 257, wantBatches: []int{256, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := newGuildReadBatchSQLState("")
			database := sql.OpenDB(guildReadBatchSQLConnector{state: state})
			defer database.Close()
			uow := &mysqlGuildUnitOfWork{router: mysqlRouter{
				db:          SQLDBAdapter{DB: database},
				readDBs:     []string{"replica"},
				writeDBs:    []string{"writer"},
				tablePrefix: "dnf_",
			}}
			ids := make([]string, tc.count)
			for i := range ids {
				ids[i] = fmt.Sprint(i + 1)
			}
			ctx, counts := repo.WithGuildReadCounts(context.Background())
			var got map[string]repo.GuildCharacter
			err := uow.WithinGuildRead(ctx, func(view repo.GuildReadView) error {
				var loadErr error
				got, loadErr = view.Characters.LoadGuildCharacters(ctx, ids)
				return loadErr
			})
			if err != nil || len(got) != tc.count {
				t.Fatalf("members=%d err=%v", len(got), err)
			}
			if got["1"].GrowType != 33 || got["1"].DeleteFlag != 1 || got["1"].AccountID != "account-1" {
				t.Fatalf("identity/delete projection lost: %+v", got["1"])
			}
			if counts.Queries != int64(len(tc.wantBatches)) || counts.Rows != int64(tc.count) {
				t.Fatalf("counts=%+v, want queries=%d rows=%d", counts, len(tc.wantBatches), tc.count)
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if state.begins != 1 || state.commits != 1 || state.rollbacks != 0 || state.openTx || state.outsideQueries != 0 {
				t.Fatalf("transaction state=%+v", state)
			}
			if state.isolation != driver.IsolationLevel(sql.LevelRepeatableRead) || !state.readOnly {
				t.Fatalf("snapshot isolation=%v readonly=%v", state.isolation, state.readOnly)
			}
			if fmt.Sprint(state.batches) != fmt.Sprint(tc.wantBatches) {
				t.Fatalf("batches=%v, want %v", state.batches, tc.wantBatches)
			}
			for _, query := range state.queries {
				if strings.Contains(query, "FOR UPDATE") || strings.Contains(query, "replica") || !strings.Contains(query, "writer") {
					t.Fatalf("query escaped read-only writer snapshot: %s", query)
				}
			}
		})
	}
}

func TestGuildReadBatchErrorsDoNotFallBackOrReturnPartialResults(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "query", err: errGuildReadBatchQuery},
		{name: "rows", err: errGuildReadBatchRows},
		{name: "close", err: errGuildReadBatchClose},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := newGuildReadBatchSQLState(tc.name)
			database := sql.OpenDB(guildReadBatchSQLConnector{state: state})
			defer database.Close()
			uow := &mysqlGuildUnitOfWork{router: mysqlRouter{
				db:          SQLDBAdapter{DB: database},
				readDBs:     []string{"replica"},
				writeDBs:    []string{"writer"},
				tablePrefix: "dnf_",
			}}
			ids := make([]string, 257)
			for i := range ids {
				ids[i] = fmt.Sprint(i + 1)
			}
			ctx, counts := repo.WithGuildReadCounts(context.Background())
			var got map[string]repo.GuildCharacter
			err := uow.WithinGuildRead(ctx, func(view repo.GuildReadView) error {
				var loadErr error
				got, loadErr = view.Characters.LoadGuildCharacters(ctx, ids)
				return loadErr
			})
			if got != nil || !errors.Is(err, tc.err) {
				t.Fatalf("partial result=%d err=%v, want only %v", len(got), err, tc.err)
			}
			if counts.Queries != 2 || counts.Rows != 256 {
				t.Fatalf("counts=%+v, want only the successful first batch", counts)
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if state.begins != 1 || state.commits != 0 || state.rollbacks != 1 || state.openTx || state.outsideQueries != 0 {
				t.Fatalf("transaction state=%+v", state)
			}
			if state.isolation != driver.IsolationLevel(sql.LevelRepeatableRead) || !state.readOnly {
				t.Fatalf("snapshot isolation=%v readonly=%v", state.isolation, state.readOnly)
			}
			if fmt.Sprint(state.batches) != "[256 1]" {
				t.Fatalf("batches=%v, want [256 1]", state.batches)
			}
		})
	}
}
