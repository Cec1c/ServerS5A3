package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
	"time"
)

type semesterSQLState struct {
	begins, commits, rollbacks, writes int
	queries                            []string
	scenario                           string
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
func (c semesterSQLConn) Close() error              { return nil }
func (c semesterSQLConn) Begin() (driver.Tx, error) { c.s.begins++; return semesterSQLTx{c.s}, nil }
func (c semesterSQLConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}
func (c semesterSQLConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.s.queries = append(c.s.queries, q)
	if c.s.begins != 1 {
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
	if c.s.begins != 1 || c.s.commits != 0 || c.s.rollbacks != 0 {
		return nil, errors.New("write escaped transaction")
	}
	if strings.Contains(q, "readonly") {
		return nil, errors.New("write escaped router")
	}
	c.s.writes++
	if c.s.scenario == "write-failure" {
		return nil, errors.New("write-failure")
	}
	return driver.RowsAffected(1), nil
}

type semesterSQLTx struct{ s *semesterSQLState }

func (t semesterSQLTx) Commit() error {
	t.s.commits++
	if t.s.scenario == "commit-unknown" {
		return errors.New("commit outcome unknown")
	}
	return nil
}
func (t semesterSQLTx) Rollback() error { t.s.rollbacks++; return nil }

func TestSemesterClearSQLSamePhysicalTransaction(t *testing.T) {
	scenarios := []string{"success", "foreign-physical-owner", "wrong-physical-record", "foreign-char", "foreign-account", "foreign-quest", "foreign-scope", "callback-failure", "write-failure", "cancel", "panic", "commit-unknown", "pre-cancel"}
	for _, scenario := range scenarios {
		t.Run(scenario, func(t *testing.T) {
			state := &semesterSQLState{scenario: scenario}
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
			switch scenario {
			case "success":
				if err != nil || state.begins != 1 || state.commits != 1 || state.rollbacks != 0 || state.writes < 6 {
					t.Fatalf("success err=%v state=%+v", err, state)
				}
			case "pre-cancel":
				if err == nil || invoked || state.begins != 0 {
					t.Fatal("pre-cancel", err, state)
				}
			case "commit-unknown":
				if !errors.Is(err, repo.ErrSemesterClearCommitUnknown) || state.commits != 1 || state.rollbacks != 0 {
					t.Fatal("unknown commit misrepresented", err, state)
				}
			default:
				if scenario == "panic" {
					if !panicked {
						t.Fatal("panic swallowed")
					}
				} else if err == nil {
					t.Fatal("fault accepted")
				}
				if state.commits != 0 || state.rollbacks != 1 {
					t.Fatal("rollback boundary", err, state)
				}
			}
			if scenario == "foreign-physical-owner" || scenario == "wrong-physical-record" {
				if invoked || state.writes != 0 {
					t.Fatal("foreign physical row reached callback")
				}
			}
		})
	}
}
