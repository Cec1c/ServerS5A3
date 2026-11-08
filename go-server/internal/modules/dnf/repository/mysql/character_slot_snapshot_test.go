package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
	"time"
)

type slotSnapshotState struct {
	rows               [][]driver.Value
	updates            [][]driver.NamedValue
	query              string
	commits, rollbacks int
	failUpdate         int
}
type slotSnapshotConnector struct{ state *slotSnapshotState }

func (c slotSnapshotConnector) Connect(context.Context) (driver.Conn, error) {
	return slotSnapshotConn{c.state}, nil
}
func (c slotSnapshotConnector) Driver() driver.Driver { return slotSnapshotDriver{c.state} }

type slotSnapshotDriver struct{ state *slotSnapshotState }

func (d slotSnapshotDriver) Open(string) (driver.Conn, error) { return slotSnapshotConn{d.state}, nil }

type slotSnapshotConn struct{ state *slotSnapshotState }

func (c slotSnapshotConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (c slotSnapshotConn) Close() error              { return nil }
func (c slotSnapshotConn) Begin() (driver.Tx, error) { return slotSnapshotTx{c.state}, nil }
func (c slotSnapshotConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.query = q
	if len(args) != 3 || args[0].Value != "owner" {
		return nil, errors.New("unscoped roster read")
	}
	return &slotSnapshotRows{rows: c.state.rows}, nil
}
func (c slotSnapshotConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.Contains(q, "WHERE character_id = ? AND account_id = ?") {
		return nil, errors.New("unscoped swap")
	}
	c.state.updates = append(c.state.updates, args)
	if c.state.failUpdate == len(c.state.updates) {
		return nil, errors.New("write failed")
	}
	return driver.RowsAffected(1), nil
}

type slotSnapshotTx struct{ state *slotSnapshotState }

func (t slotSnapshotTx) Commit() error   { t.state.commits++; return nil }
func (t slotSnapshotTx) Rollback() error { t.state.rollbacks++; return nil }

type slotSnapshotRows struct{ rows [][]driver.Value }

func (r *slotSnapshotRows) Columns() []string { return []string{"character_id", "slot"} }
func (r *slotSnapshotRows) Close() error      { return nil }
func (r *slotSnapshotRows) Next(v []driver.Value) error {
	if len(r.rows) == 0 {
		return io.EOF
	}
	copy(v, r.rows[0])
	r.rows = r.rows[1:]
	return nil
}

func TestCharacterSlotSnapshotValidatesLockedIdentities(t *testing.T) {
	for _, kind := range []string{"swap", "replacement", "missing", "write_failure", "same"} {
		t.Run(kind, func(t *testing.T) {
			state := &slotSnapshotState{rows: [][]driver.Value{{"201", int64(2)}, {"202", int64(7)}}}
			left := repo.CharacterRecord{CharacterID: "201", AccountID: "owner", Slot: 2}
			right := repo.CharacterRecord{CharacterID: "202", AccountID: "owner", Slot: 7}
			switch kind {
			case "replacement":
				state.rows[1][0] = "999"
			case "missing":
				state.rows = state.rows[:1]
			case "write_failure":
				state.failUpdate = 2
			case "same":
				right = left
				state.rows = state.rows[:1]
			}
			db := sql.OpenDB(slotSnapshotConnector{state})
			defer db.Close()
			store := &mysqlCharStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{db: SQLDBAdapter{db}, readDBs: []string{"replica"}, writeDBs: []string{"writer"}, tablePrefix: "dnf_", now: time.Now}}}
			err := store.SwapCharacterSlotsFromSnapshot(context.Background(), "owner", left, right)
			success := kind == "swap" || kind == "same"
			if (err == nil) != success {
				t.Fatalf("err=%v", err)
			}
			if !strings.Contains(state.query, "FOR UPDATE") || !strings.Contains(state.query, "delete_flag = 0") || strings.Contains(state.query, "replica") {
				t.Fatalf("unsafe read %s", state.query)
			}
			if success {
				if state.commits != 1 || state.rollbacks != 0 {
					t.Fatal("not committed exactly once")
				}
			} else if state.commits != 0 || state.rollbacks != 1 {
				t.Fatal("not rolled back")
			}
			if (kind == "replacement" || kind == "missing" || kind == "same") && len(state.updates) != 0 {
				t.Fatal("unexpected mutation")
			}
			if kind == "swap" {
				if len(state.updates) != 3 || state.updates[1][0].Value != int64(2) || state.updates[2][0].Value != int64(7) {
					t.Fatalf("wrong swap writes %v", state.updates)
				}
			}
		})
	}
}
