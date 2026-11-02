package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	repository "longheng.io/server/internal/modules/dnf/repository"
)

type disjointerSQLCall struct {
	connection int
	query      string
	args       []driver.NamedValue
}

type disjointerSQLState struct {
	connections, begins, commits, rollbacks int
	calls                                   []disjointerSQLCall
	charAccounts                            map[string]string
}

type disjointerSQLConnector struct{ state *disjointerSQLState }

func (c disjointerSQLConnector) Connect(context.Context) (driver.Conn, error) {
	c.state.connections++
	return &disjointerSQLConn{state: c.state, id: c.state.connections}, nil
}

func (c disjointerSQLConnector) Driver() driver.Driver { return disjointerSQLDriver{c.state} }

type disjointerSQLDriver struct{ state *disjointerSQLState }

func (d disjointerSQLDriver) Open(string) (driver.Conn, error) {
	return d.stateConnector().Connect(context.Background())
}

func (d disjointerSQLDriver) stateConnector() disjointerSQLConnector {
	return disjointerSQLConnector{state: d.state}
}

type disjointerSQLConn struct {
	state *disjointerSQLState
	id    int
	tx    *disjointerSQLTx
}

func (c *disjointerSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected disjointer SQL prepare")
}

func (c *disjointerSQLConn) Close() error { return nil }

func (c *disjointerSQLConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *disjointerSQLConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if c.tx != nil {
		return nil, errors.New("nested disjointer SQL transaction")
	}
	c.state.begins++
	c.tx = &disjointerSQLTx{conn: c}
	return c.tx, nil
}

func (c *disjointerSQLConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.tx == nil {
		return nil, errors.New("disjointer query escaped its physical transaction")
	}
	c.state.calls = append(c.state.calls, disjointerSQLCall{
		connection: c.id,
		query:      query,
		args:       append([]driver.NamedValue(nil), args...),
	})

	columns := disjointerSQLColumns(query)
	switch {
	case strings.HasPrefix(query, "SELECT account_id, state, honor_exp, represent_account_name, created_at, updated_at FROM "):
		values := make([]driver.Value, len(columns))
		copy(values, []driver.Value{"a", "active", int64(0), "account", nil, nil})
		return &disjointerSQLRows{columns: columns, values: [][]driver.Value{values}}, nil
	case strings.HasPrefix(query, "SELECT character_id, account_id, "):
		characterID := disjointerSQLStringArg(args, 0)
		accountID := c.state.charAccounts[characterID]
		if accountID == "" {
			return &disjointerSQLRows{columns: columns}, nil
		}
		values := make([]driver.Value, len(columns))
		for index := range values {
			values[index] = int64(0)
		}
		copy(values, []driver.Value{characterID, accountID, int64(0), "character", "0", int64(90)})
		if len(values) >= 2 {
			values[len(values)-2], values[len(values)-1] = nil, nil
		}
		return &disjointerSQLRows{columns: columns, values: [][]driver.Value{values}}, nil
	default:
		// Account metadata and character supplement tables have no rows in this
		// fixture. Their selected columns are still returned so empty Query and
		// tolerated ErrNoRows scans exercise the same paths as a real database.
		return &disjointerSQLRows{columns: columns}, nil
	}
}

func (c *disjointerSQLConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	if c.tx == nil {
		return nil, errors.New("disjointer write escaped its physical transaction")
	}
	return driver.RowsAffected(1), nil
}

type disjointerSQLTx struct{ conn *disjointerSQLConn }

func (tx *disjointerSQLTx) Commit() error {
	tx.conn.state.commits++
	tx.conn.tx = nil
	return nil
}

func (tx *disjointerSQLTx) Rollback() error {
	tx.conn.state.rollbacks++
	tx.conn.tx = nil
	return nil
}

type disjointerSQLRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *disjointerSQLRows) Columns() []string { return r.columns }
func (r *disjointerSQLRows) Close() error      { return nil }

func (r *disjointerSQLRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

func disjointerSQLColumns(query string) []string {
	query = strings.TrimPrefix(query, "SELECT ")
	if from := strings.Index(query, " FROM "); from >= 0 {
		query = query[:from]
	}
	return strings.Split(query, ",")
}

func disjointerSQLStringArg(args []driver.NamedValue, index int) string {
	if index >= len(args) {
		return ""
	}
	value, _ := args[index].Value.(string)
	return value
}

func newDisjointerSQLFixture(charAccounts map[string]string) (*mysqlCharacterTradeUnitOfWork, *sql.DB, *disjointerSQLState) {
	state := &disjointerSQLState{charAccounts: charAccounts}
	database := sql.OpenDB(disjointerSQLConnector{state: state})
	uow := &mysqlCharacterTradeUnitOfWork{router: mysqlRouter{
		db:          SQLDBAdapter{DB: database},
		readDBs:     []string{"replica"},
		writeDBs:    []string{"shard_a", "shard_b"},
		tablePrefix: "dnf_",
		now:         time.Now,
	}}
	return uow, database, state
}

func assertDisjointerOnePhysicalTransaction(t *testing.T, state *disjointerSQLState) {
	t.Helper()
	if state.connections != 1 || state.begins != 1 {
		t.Fatalf("physical connections/begins=%d/%d, want 1/1", state.connections, state.begins)
	}
	if len(state.calls) == 0 {
		t.Fatal("transaction issued no reads")
	}
	for _, call := range state.calls {
		if call.connection != 1 {
			t.Fatalf("query used connection %d, want the transaction connection 1", call.connection)
		}
		if strings.Contains(call.query, "replica") || !strings.HasSuffix(call.query, " FOR UPDATE") {
			t.Fatalf("query escaped the write transaction or did not lock: %s", call.query)
		}
	}
}

func disjointerCharacterLockIDs(state *disjointerSQLState) []string {
	ids := make([]string, 0, 2)
	for _, call := range state.calls {
		if strings.HasPrefix(call.query, "SELECT character_id, account_id, ") {
			ids = append(ids, disjointerSQLStringArg(call.args, 0))
		}
	}
	return ids
}

func TestMySQLDisjointerAssetsLocksAccountBeforeSortedCharactersInOneTransaction(t *testing.T) {
	uow, database, state := newDisjointerSQLFixture(map[string]string{"19": "a", "20": "a"})
	defer database.Close()

	callbackRan := false
	err := uow.WithinDisjointerAssets(context.Background(), "a", "20", "19", func(
		ledger repository.AccountRepository,
		_ repository.CharacterRepository,
		_ repository.InventoryRepository,
		_ repository.AccountInventoryRepository,
	) error {
		callbackRan = true
		account, found, loadErr := ledger.Load(context.Background(), "a")
		if loadErr != nil {
			return loadErr
		}
		if !found || account.AccountID != "a" {
			return errors.New("callback account read escaped fixture")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !callbackRan || state.commits != 1 || state.rollbacks != 0 {
		t.Fatalf("callback/commit/rollback=%t/%d/%d", callbackRan, state.commits, state.rollbacks)
	}
	assertDisjointerOnePhysicalTransaction(t, state)

	accountIndex := -1
	firstCharacterIndex := -1
	for index, call := range state.calls {
		if accountIndex < 0 && strings.HasPrefix(call.query, "SELECT account_id, state, honor_exp,") {
			accountIndex = index
		}
		if firstCharacterIndex < 0 && strings.HasPrefix(call.query, "SELECT character_id, account_id, ") {
			firstCharacterIndex = index
		}
	}
	if accountIndex != 0 || firstCharacterIndex <= accountIndex {
		t.Fatalf("account/character lock indexes=%d/%d calls=%+v", accountIndex, firstCharacterIndex, state.calls)
	}
	if got, want := disjointerCharacterLockIDs(state), []string{"19", "20"}; !equalStrings(got, want) {
		t.Fatalf("character lock order=%v, want %v", got, want)
	}
}

func TestMySQLDisjointerAssetsDeduplicatesSameRequesterAndOwner(t *testing.T) {
	uow, database, state := newDisjointerSQLFixture(map[string]string{"19": "a"})
	defer database.Close()

	if err := uow.WithinDisjointerAssets(context.Background(), "a", "19", "19", func(
		repository.AccountRepository,
		repository.CharacterRepository,
		repository.InventoryRepository,
		repository.AccountInventoryRepository,
	) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertDisjointerOnePhysicalTransaction(t, state)
	if got, want := disjointerCharacterLockIDs(state), []string{"19"}; !equalStrings(got, want) {
		t.Fatalf("self-use character locks=%v, want %v", got, want)
	}
	if state.commits != 1 || state.rollbacks != 0 {
		t.Fatalf("commit/rollback=%d/%d", state.commits, state.rollbacks)
	}
}

func TestMySQLDisjointerAssetsRejectsForeignRequesterAccount(t *testing.T) {
	uow, database, state := newDisjointerSQLFixture(map[string]string{"19": "b"})
	defer database.Close()

	callbackRan := false
	err := uow.WithinDisjointerAssets(context.Background(), "a", "19", "19", func(
		repository.AccountRepository,
		repository.CharacterRepository,
		repository.InventoryRepository,
		repository.AccountInventoryRepository,
	) error {
		callbackRan = true
		return nil
	})
	if !errors.Is(err, repository.ErrEpicBookOwnership) {
		t.Fatalf("error=%v, want %v", err, repository.ErrEpicBookOwnership)
	}
	if callbackRan {
		t.Fatal("ownership rejection reached callback")
	}
	if state.commits != 0 || state.rollbacks != 1 {
		t.Fatalf("commit/rollback=%d/%d, want 0/1", state.commits, state.rollbacks)
	}
	assertDisjointerOnePhysicalTransaction(t, state)
}

func TestMySQLDisjointerAssetsRollsBackCallbackFailure(t *testing.T) {
	uow, database, state := newDisjointerSQLFixture(map[string]string{"19": "a", "20": "a"})
	defer database.Close()

	callbackErr := errors.New("injected disjointer projection failure")
	err := uow.WithinDisjointerAssets(context.Background(), "a", "19", "20", func(
		repository.AccountRepository,
		repository.CharacterRepository,
		repository.InventoryRepository,
		repository.AccountInventoryRepository,
	) error {
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("error=%v, want %v", err, callbackErr)
	}
	if state.commits != 0 || state.rollbacks != 1 {
		t.Fatalf("commit/rollback=%d/%d, want 0/1", state.commits, state.rollbacks)
	}
	assertDisjointerOnePhysicalTransaction(t, state)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
