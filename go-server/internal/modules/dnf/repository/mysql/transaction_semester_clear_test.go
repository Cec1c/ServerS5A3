package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"sync"
	"testing"
	"time"
)

// semesterSQLState records the physical transaction boundaries observed by the
// semester repository fixtures. The counters are mutex-guarded because
// database/sql may release a canceled transaction from its async awaitDone
// goroutine, which runs the driver Rollback concurrently with the caller and
// with the caller-side counter reads that would otherwise race it.
type semesterSQLState struct {
	mu        sync.Mutex
	begins    int
	commits   int
	rollbacks int
	writes    int
	queries   []string
	scenario  string

	// rollbackDone is closed once the driver Rollback has actually run. Tests
	// that cancel the transaction context join on it before reading the counters;
	// the caller-facing Tx.Rollback returns ErrTxDone before this driver call
	// executes, so the count must not be sampled earlier.
	rollbackDone chan struct{}
}

func newSemesterSQLState(scenario string) *semesterSQLState {
	return &semesterSQLState{scenario: scenario, rollbackDone: make(chan struct{})}
}

func (s *semesterSQLState) snapshot() (begins, commits, rollbacks, writes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.begins, s.commits, s.rollbacks, s.writes
}

func (s *semesterSQLState) describe() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("{begins:%d commits:%d rollbacks:%d writes:%d scenario:%s queries:%v}", s.begins, s.commits, s.rollbacks, s.writes, s.scenario, s.queries)
}

// awaitRollback blocks until database/sql has invoked the driver Rollback. A
// canceled context can be released from the async awaitDone goroutine, where the
// caller-facing Tx.Rollback returns ErrTxDone before that driver call runs.
func (s *semesterSQLState) awaitRollback(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	done := s.rollbackDone
	s.mu.Unlock()
	if done == nil {
		t.Fatal("semester SQL state has no rollback signal")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("driver rollback did not complete: %s", s.describe())
	}
}

type semesterSQLConnector struct{ s *semesterSQLState }

func (c semesterSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return semesterSQLConn{c.s}, nil
}
func (c semesterSQLConnector) Driver() driver.Driver { return semesterSQLDriver{c.s} }

type semesterSQLDriver struct{ s *semesterSQLState }

func (d semesterSQLDriver) Open(string) (driver.Conn, error) { return semesterSQLConn{d.s}, nil }

type semesterSQLConn struct{ s *semesterSQLState }

func (c semesterSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (c semesterSQLConn) Close() error { return nil }
func (c semesterSQLConn) Begin() (driver.Tx, error) {
	c.s.mu.Lock()
	c.s.begins++
	c.s.mu.Unlock()
	return semesterSQLTx{c.s}, nil
}
func (c semesterSQLConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}
func (c semesterSQLConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.s.mu.Lock()
	c.s.queries = append(c.s.queries, q)
	begins := c.s.begins
	c.s.mu.Unlock()
	if begins != 1 {
		return nil, errors.New("read escaped physical transaction")
	}
	if !strings.Contains(q, "FOR UPDATE") {
		return nil, errors.New("owner read did not lock")
	}
	if strings.Contains(q, "readonly") {
		return nil, errors.New("read router escaped write DB plan")
	}
	fields := strings.Split(strings.Split(strings.TrimPrefix(q, "SELECT "), " FROM ")[0], ",")
	rows := &pairSQLRows{columns: fields}
	if strings.HasPrefix(q, "SELECT character_id, account_id,") {
		id := fmt.Sprint(args[0].Value)
		vals := make([]driver.Value, len(fields))
		for i := range vals {
			vals[i] = int64(0)
		}
		vals[0], vals[1], vals[2], vals[3], vals[4], vals[5] = id, "a"+id, int64(0), "name", int64(0), int64(17)
		vals[len(vals)-1], vals[len(vals)-2] = nil, nil
		if c.s.scenario == "foreign-physical-owner" && id == "4" {
			vals[1] = "wrong"
		}
		if c.s.scenario == "wrong-physical-record" && id == "4" {
			vals[0] = "5"
		}
		rows.rows = [][]driver.Value{vals}
	}
	return rows, nil
}
func (c semesterSQLConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	c.s.mu.Lock()
	if c.s.begins != 1 || c.s.commits != 0 || c.s.rollbacks != 0 {
		c.s.mu.Unlock()
		return nil, errors.New("write escaped transaction")
	}
	if strings.Contains(q, "readonly") {
		c.s.mu.Unlock()
		return nil, errors.New("write escaped router")
	}
	c.s.writes++
	c.s.mu.Unlock()
	if c.s.scenario == "write-failure" {
		return nil, errors.New("write-failure")
	}
	return driver.RowsAffected(1), nil
}

type semesterSQLTx struct{ s *semesterSQLState }

func (t semesterSQLTx) Commit() error {
	t.s.mu.Lock()
	t.s.commits++
	t.s.mu.Unlock()
	if t.s.scenario == "commit-unknown" {
		return errors.New("commit outcome unknown")
	}
	return nil
}
func (t semesterSQLTx) Rollback() error {
	t.s.mu.Lock()
	t.s.rollbacks++
	if t.s.rollbackDone != nil {
		select {
		case <-t.s.rollbackDone:
		default:
			close(t.s.rollbackDone)
		}
	}
	t.s.mu.Unlock()
	return nil
}

func TestSemesterClearSQLSamePhysicalTransaction(t *testing.T) {
	scenarios := []string{"success", "foreign-physical-owner", "wrong-physical-record", "foreign-char", "foreign-account", "foreign-quest", "foreign-scope", "callback-failure", "write-failure", "cancel", "panic", "commit-unknown", "pre-cancel"}
	for _, scenario := range scenarios {
		t.Run(scenario, func(t *testing.T) {
			state := newSemesterSQLState(scenario)
			db := sql.OpenDB(semesterSQLConnector{state})
			defer db.Close()
			u := &mysqlSemesterClearUnitOfWork{router: mysqlRouter{db: SQLDBAdapter{DB: db}, readDBs: []string{"readonly"}, writeDBs: []string{"shard_a", "shard_b"}, tablePrefix: "dnf_", now: time.Now}}
			owners := []repo.SemesterClearOwner{{AccountID: "a4", CharacterID: "4"}, {AccountID: "a2", CharacterID: "2"}, {AccountID: "a1", CharacterID: "1"}, {AccountID: "a3", CharacterID: "3"}}
			source := owners[2]
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "pre-cancel" {
				cancel()
			}
			invoked, panicked := false, false
			var err error
			func() {
				defer func() {
					if recover() != nil {
						panicked = true
					}
				}()
				err = u.WithinSemesterClear(ctx, source, owners, "semester-receipt:one", func(tx repo.Group) error {
					invoked = true
					switch scenario {
					case "foreign-char":
						_, _, e := tx.Character.Load(ctx, "5")
						return e
					case "foreign-account":
						return tx.Character.Save(ctx, repo.CharacterRecord{CharacterID: "1", AccountID: "wrong"})
					case "foreign-quest":
						return tx.Quest.Save(ctx, repo.QuestRecord{CharacterID: "2"})
					case "foreign-scope":
						return tx.Settings.Save(ctx, repo.SettingsRecord{Scope: "other"})
					case "callback-failure":
						return errors.New("callback-failure")
					case "panic":
						panic("callback-panic")
					}
					for _, o := range owners {
						ch, _, e := tx.Character.Load(ctx, o.CharacterID)
						if e != nil {
							return e
						}
						ch.Stats = map[string]int64{"qualified_days": 1}
						if e = tx.Character.Save(ctx, ch); e != nil {
							return e
						}
					}
					if e := tx.Quest.Save(ctx, repo.QuestRecord{CharacterID: "1", States: map[int64]repo.QuestState{9: {Status: "pending"}}}); e != nil {
						return e
					}
					if e := tx.Settings.Save(ctx, repo.SettingsRecord{Scope: "semester-receipt:one", Values: map[string]string{"receipt": "valid"}}); e != nil {
						return e
					}
					if scenario == "cancel" {
						cancel()
					}
					return nil
				})
			}()
			if scenario == "cancel" {
				// A canceled context lets database/sql release the physical transaction
				// from its async awaitDone goroutine; join on the driver rollback before
				// sampling the counters.
				state.awaitRollback(t)
			}
			begins, commits, rollbacks, writes := state.snapshot()
			switch scenario {
			case "success":
				if err != nil || begins != 1 || commits != 1 || rollbacks != 0 || writes < 6 {
					t.Fatalf("success err=%v state=%s", err, state.describe())
				}
			case "pre-cancel":
				if err == nil || invoked || begins != 0 {
					t.Fatal("pre-cancel", err, state.describe())
				}
			case "commit-unknown":
				if !errors.Is(err, repo.ErrSemesterClearCommitUnknown) || commits != 1 || rollbacks != 0 {
					t.Fatal("unknown commit misrepresented", err, state.describe())
				}
			default:
				if scenario == "panic" {
					if !panicked {
						t.Fatal("panic swallowed")
					}
				} else if err == nil {
					t.Fatal("fault accepted")
				}
				if commits != 0 || rollbacks != 1 {
					t.Fatal("rollback boundary", err, state.describe())
				}
			}
			if scenario == "foreign-physical-owner" || scenario == "wrong-physical-record" {
				if invoked || writes != 0 {
					t.Fatal("foreign physical row reached callback")
				}
			}
		})
	}
}
