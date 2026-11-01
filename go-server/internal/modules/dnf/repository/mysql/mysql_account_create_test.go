package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/repository"
)

type accountCreateSQLState struct {
	existing, foreignDuplicate, failOwner, failMetadata, failCommit bool
	calls                                                           []string
	commits, rollbacks, metadataWrites                              int
}

type accountCreateConnector struct{ state *accountCreateSQLState }
type accountCreateDriver struct{ state *accountCreateSQLState }
type accountCreateConn struct{ state *accountCreateSQLState }
type accountCreateTx struct{ state *accountCreateSQLState }

func (c accountCreateConnector) Connect(context.Context) (driver.Conn, error) {
	return accountCreateConn{c.state}, nil
}
func (c accountCreateConnector) Driver() driver.Driver { return accountCreateDriver{c.state} }
func (d accountCreateDriver) Open(string) (driver.Conn, error) {
	return accountCreateConn{d.state}, nil
}
func (c accountCreateConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("不应预备语句")
}
func (c accountCreateConn) Close() error              { return nil }
func (c accountCreateConn) Begin() (driver.Tx, error) { return accountCreateTx{c.state}, nil }
func (c accountCreateConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}

func (c accountCreateConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.state.calls = append(c.state.calls, query)
	if strings.Contains(query, "`dnf_accounts`") {
		if c.state.existing || c.state.foreignDuplicate {
			return nil, &mysqldriver.MySQLError{Number: 1062, Message: "审计注入唯一键冲突"}
		}
		if c.state.failOwner {
			return nil, errors.New("账号写入失败")
		}
	}
	if strings.Contains(query, "`dnf_account_metadata`") {
		c.state.metadataWrites++
		if c.state.failMetadata {
			return nil, errors.New("元数据写入失败")
		}
	}
	return driver.RowsAffected(1), nil
}

func (c accountCreateConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.calls = append(c.state.calls, query)
	rows := &pairSQLRows{columns: []string{"account_id"}}
	if c.state.existing {
		rows.rows = [][]driver.Value{{"a"}}
	}
	return rows, nil
}

func (t accountCreateTx) Commit() error {
	t.state.commits++
	if t.state.failCommit {
		return errors.New("提交失败")
	}
	return nil
}
func (t accountCreateTx) Rollback() error { t.state.rollbacks++; return nil }

func TestMySQLAccountCreateIfAbsentOwnsMetadataAndPropagatesFailures(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		state                              accountCreateSQLState
		created, failure                   bool
		commits, rollbacks, metadataWrites int
	}{
		{name: "首次原子创建", created: true, commits: 1, metadataWrites: 2},
		{name: "已存在零覆盖", state: accountCreateSQLState{existing: true}, rollbacks: 1},
		{name: "其他唯一键冲突不吞错", state: accountCreateSQLState{foreignDuplicate: true}, failure: true, rollbacks: 1},
		{name: "账号写入失败回滚", state: accountCreateSQLState{failOwner: true}, failure: true, rollbacks: 1},
		{name: "元数据失败账号也回滚", state: accountCreateSQLState{failMetadata: true}, failure: true, rollbacks: 1, metadataWrites: 1},
		{name: "提交失败不假报创建成功", state: accountCreateSQLState{failCommit: true}, failure: true, commits: 1, metadataWrites: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := sql.OpenDB(accountCreateConnector{&tc.state})
			defer database.Close()
			store := &mysqlAccountStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{
				db: SQLDBAdapter{DB: database}, readDBs: []string{"readonly"}, writeDBs: []string{"writer"}, tablePrefix: "dnf", now: time.Now,
			}}}
			created, err := repository.CreateAccountIfAbsent(context.Background(), store, repository.AccountRecord{AccountID: "a", Metadata: map[string]string{"newbie": "pending", "source": "login"}})
			if created != tc.created || (err != nil) != tc.failure || tc.state.commits != tc.commits || tc.state.rollbacks != tc.rollbacks || tc.state.metadataWrites != tc.metadataWrites {
				t.Fatalf("创建/回滚契约不符：created=%t err=%v state=%+v", created, err, tc.state)
			}
			for _, query := range tc.state.calls {
				if strings.Contains(query, "readonly") || strings.Contains(query, "ON DUPLICATE") || strings.Contains(query, "IGNORE") || strings.HasPrefix(query, "UPDATE") || strings.HasPrefix(query, "DELETE") {
					t.Fatal("原子建档不得读从库、吞错或改写旧记录：", query)
				}
			}
		})
	}
}

func TestMySQLAccountCreateIfAbsentRequiresTransactionAndHonorsCancellation(t *testing.T) {
	fake := &fakeSQLDB{}
	group := newTestMySQLGroup(t, fake)
	if created, err := repository.CreateAccountIfAbsent(context.Background(), group.Account, repository.AccountRecord{AccountID: "a"}); created || !errors.Is(err, repository.ErrAccountCreationUnavailable) || len(fake.execs) != 0 {
		t.Fatal("不支持事务时不得退化写入", created, err, fake.execs)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if created, err := repository.CreateAccountIfAbsent(ctx, group.Account, repository.AccountRecord{AccountID: "a"}); created || !errors.Is(err, context.Canceled) || len(fake.execs) != 0 {
		t.Fatal("取消后仍发出写入", created, err, fake.execs)
	}
}
