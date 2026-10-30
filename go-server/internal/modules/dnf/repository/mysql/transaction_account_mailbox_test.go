package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
	"time"
)

type accountMailSQLState struct {
	begins, commits, rollbacks, writes int
	queries                            []string
	scenario                           string
}
type accountMailSQLConnector struct{ s *accountMailSQLState }

func (c accountMailSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return accountMailSQLConn{c.s}, nil
}
func (c accountMailSQLConnector) Driver() driver.Driver { return accountMailSQLDriver{c.s} }

type accountMailSQLDriver struct{ s *accountMailSQLState }

func (d accountMailSQLDriver) Open(string) (driver.Conn, error) { return accountMailSQLConn{d.s}, nil }

type accountMailSQLConn struct{ s *accountMailSQLState }

func (c accountMailSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (c accountMailSQLConn) Close() error { return nil }
func (c accountMailSQLConn) Begin() (driver.Tx, error) {
	c.s.begins++
	return accountMailSQLTx{c.s}, nil
}
func (c accountMailSQLConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}
func (c accountMailSQLConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	c.s.queries = append(c.s.queries, q)
	if c.s.begins != 1 {
		return nil, errors.New("query outside one physical transaction")
	}
	fields := strings.Split(strings.Split(strings.TrimPrefix(q, "SELECT "), " FROM ")[0], ",")
	rows := &pairSQLRows{columns: fields}
	if strings.HasPrefix(q, "SELECT character_id, account_id,") {
		vals := make([]driver.Value, len(fields))
		for i := range vals {
			vals[i] = int64(0)
		}
		vals[0], vals[1], vals[2], vals[3], vals[4], vals[5] = "19", "a", int64(0), "name", int64(0), int64(90)
		vals[len(vals)-1], vals[len(vals)-2] = nil, nil
		if c.s.scenario == "foreign_owner" {
			vals[1] = "b"
		}
		rows.rows = [][]driver.Value{vals}
	} else if strings.HasPrefix(q, "SELECT account_id,") {
		if c.s.scenario != "account_missing" {
			rows.rows = [][]driver.Value{{"a", "active", int64(0), "account", nil, nil}}
		}
	} else if strings.HasPrefix(q, "SELECT character_id, updated_at") {
		rows.rows = [][]driver.Value{{"19", nil}}
	}
	return rows, nil
}
func (c accountMailSQLConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	c.s.writes++
	if c.s.scenario == "write_failure" {
		return nil, errors.New("write failed")
	}
	return driver.RowsAffected(1), nil
}

type accountMailSQLTx struct{ s *accountMailSQLState }

func (tx accountMailSQLTx) Commit() error {
	tx.s.commits++
	if tx.s.scenario == "commit_failure" {
		return errors.New("commit failed")
	}
	return nil
}
func (tx accountMailSQLTx) Rollback() error { tx.s.rollbacks++; return nil }
func TestMySQLAccountMailboxOnePhysicalTransaction(t *testing.T) {
	for _, scenario := range []string{"success", "foreign_owner", "account_missing", "account_scope", "mail_scope", "callback_failure", "write_failure", "commit_failure", "panic", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			s := &accountMailSQLState{scenario: scenario}
			db := sql.OpenDB(accountMailSQLConnector{s})
			defer db.Close()
			u := &mysqlMailboxAssetUnitOfWork{router: mysqlRouter{db: SQLDBAdapter{DB: db}, readDBs: []string{"readonly"}, writeDBs: []string{"shard_a", "shard_b"}, tablePrefix: "dnf_", now: time.Now}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			invoke := func() error {
				return u.WithinAccountMailbox(ctx, "a", "19", func(a repo.AccountRepository, m repo.MailboxRepository) error {
					if scenario == "account_scope" {
						_, _, err := a.Load(ctx, "b")
						return err
					}
					if scenario == "mail_scope" {
						_, _, err := m.Load(ctx, "20")
						return err
					}
					account, _, err := a.Load(ctx, "a")
					if err != nil {
						return err
					}
					if err := repo.SaveAccountMetadataEntry(ctx, a, account, "attendance", "claimed", time.Now()); err != nil {
						return err
					}
					if _, err := repo.AppendSystemMail(ctx, m, repo.SystemMailDelivery{RecipientCharacterID: "19", Source: "attendance", Attachments: []repo.MailAttachment{{ItemID: 490010051, Count: 1}}}); err != nil {
						return err
					}
					if scenario == "callback_failure" {
						return errors.New("callback failed")
					}
					if scenario == "panic" {
						panic("callback panic")
					}
					if scenario == "cancel" {
						cancel()
					}
					return nil
				})
			}
			var err error
			panicked := false
			func() {
				defer func() {
					if recover() != nil {
						panicked = true
					}
				}()
				err = invoke()
			}()
			if s.begins != 1 {
				t.Fatalf("physical begins=%d", s.begins)
			}
			for _, q := range s.queries {
				if strings.Contains(q, "readonly") || !strings.HasSuffix(q, " FOR UPDATE") {
					t.Fatalf("query escaped write transaction or unlocked: %s", q)
				}
			}
			if scenario == "success" {
				if err != nil || s.commits != 1 || s.rollbacks != 0 || s.writes < 2 {
					t.Fatalf("success err=%v state=%+v", err, s)
				}
				return
			}
			if err == nil && !panicked {
				t.Fatal("invalid operation accepted")
			}
			if scenario == "commit_failure" {
				if s.commits != 1 {
					t.Fatal("commit failure not tested")
				}
				return
			}
			if s.commits != 0 || s.rollbacks != 1 {
				t.Fatalf("failed operation transaction state=%+v err=%v", s, err)
			}
		})
	}
}

func TestMySQLAccountMailboxLocksAccountBeforeCharacter(t *testing.T) {
	state := &accountMailSQLState{scenario: "success"}
	database := sql.OpenDB(accountMailSQLConnector{state})
	defer database.Close()
	uow := &mysqlMailboxAssetUnitOfWork{router: mysqlRouter{
		db:          SQLDBAdapter{DB: database},
		readDBs:     []string{"readonly"},
		writeDBs:    []string{"shard_a", "shard_b"},
		tablePrefix: "dnf_",
		now:         time.Now,
	}}
	if err := uow.WithinAccountMailbox(context.Background(), "a", "19", func(repo.AccountRepository, repo.MailboxRepository) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	accountLocked, characterLocked := -1, -1
	for index, query := range state.queries {
		if !strings.HasSuffix(query, " FOR UPDATE") {
			continue
		}
		if accountLocked < 0 && strings.HasPrefix(query, "SELECT account_id,") {
			accountLocked = index
		}
		if characterLocked < 0 && strings.HasPrefix(query, "SELECT character_id,") {
			characterLocked = index
		}
	}
	if accountLocked != 0 || characterLocked <= accountLocked {
		t.Fatalf("account mailbox lock order account=%d character=%d queries=%v", accountLocked, characterLocked, state.queries)
	}
}
