package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	platformdb "longheng.io/server/internal/platform/db"
)

// raidLedgerSQLState models one physical MySQL connection at a time. The real
// mysqlAccountCharacterAssetUnitOfWork and every production repository execute
// unchanged; this driver only makes transaction ownership observable.
type raidLedgerSQLState struct {
	nextConnID, txConnID               int
	begins, commits, rollbacks, writes int
	outsideTxOperations                int
	characterAccount                   string
	queries, writeSQL                  []string
}

type raidLedgerSQLConnector struct{ state *raidLedgerSQLState }

func (c raidLedgerSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return c.state.connection(), nil
}

func (c raidLedgerSQLConnector) Driver() driver.Driver {
	return raidLedgerSQLDriver{state: c.state}
}

type raidLedgerSQLDriver struct{ state *raidLedgerSQLState }

func (d raidLedgerSQLDriver) Open(string) (driver.Conn, error) {
	return d.state.connection(), nil
}

func (s *raidLedgerSQLState) connection() *raidLedgerSQLConn {
	s.nextConnID++
	return &raidLedgerSQLConn{state: s, id: s.nextConnID}
}

type raidLedgerSQLConn struct {
	state *raidLedgerSQLState
	id    int
	inTx  bool
}

func (c *raidLedgerSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepared statement")
}

func (c *raidLedgerSQLConn) Close() error { return nil }

func (c *raidLedgerSQLConn) Begin() (driver.Tx, error) {
	return c.begin()
}

func (c *raidLedgerSQLConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.begin()
}

func (c *raidLedgerSQLConn) begin() (driver.Tx, error) {
	if c.inTx || c.state.txConnID != 0 {
		return nil, errors.New("overlapping physical transaction")
	}
	c.inTx = true
	c.state.txConnID = c.id
	c.state.begins++
	return &raidLedgerSQLTx{conn: c}, nil
}

func (c *raidLedgerSQLConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !c.transactional() {
		c.state.outsideTxOperations++
		return nil, errors.New("query outside physical transaction")
	}
	c.state.queries = append(c.state.queries, query)
	return raidLedgerRowsFor(query, c.state.characterAccount, args), nil
}

func (c *raidLedgerSQLConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if !c.transactional() {
		c.state.outsideTxOperations++
		return nil, errors.New("write outside physical transaction")
	}
	c.state.writes++
	c.state.writeSQL = append(c.state.writeSQL, query)
	return driver.RowsAffected(1), nil
}

func (c *raidLedgerSQLConn) transactional() bool {
	return c.inTx && c.state.txConnID == c.id
}

type raidLedgerSQLTx struct {
	conn *raidLedgerSQLConn
	done bool
}

func (tx *raidLedgerSQLTx) Commit() error { return tx.finish(true) }

func (tx *raidLedgerSQLTx) Rollback() error { return tx.finish(false) }

func (tx *raidLedgerSQLTx) finish(commit bool) error {
	if tx.done || !tx.conn.transactional() {
		return errors.New("inactive physical transaction")
	}
	tx.done = true
	tx.conn.inTx = false
	tx.conn.state.txConnID = 0
	if commit {
		tx.conn.state.commits++
	} else {
		tx.conn.state.rollbacks++
	}
	return nil
}

type raidLedgerSQLRows struct {
	columns []string
	rows    [][]driver.Value
	next    int
}

func (r *raidLedgerSQLRows) Columns() []string { return r.columns }
func (r *raidLedgerSQLRows) Close() error      { return nil }

func (r *raidLedgerSQLRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	row := r.rows[r.next]
	r.next++
	if len(dest) != len(row) {
		return fmt.Errorf("row has %d columns, destination has %d", len(row), len(dest))
	}
	copy(dest, row)
	return nil
}

func raidLedgerRowsFor(query, characterAccount string, args []driver.NamedValue) driver.Rows {
	columns := raidLedgerQueryColumns(query)
	rows := &raidLedgerSQLRows{columns: columns}
	if characterAccount == "" {
		characterAccount = "a"
	}
	values := func() []driver.Value {
		result := make([]driver.Value, len(columns))
		for index := range result {
			result[index] = int64(0)
		}
		return result
	}()

	switch raidLedgerQueryTable(query) {
	case mysqlAccountTable:
		values[0], values[1], values[3] = "a", "active", nil
		values[4], values[5] = nil, nil
		rows.rows = [][]driver.Value{values}
	case mysqlCharTable:
		values[0], values[1], values[3], values[4] = "19", characterAccount, "raid-ledger", "0"
		values[2], values[5] = int64(0), int64(90)
		values[len(values)-2], values[len(values)-1] = nil, nil
		rows.rows = [][]driver.Value{values}
	case mysqlAccountInventoryTable:
		values[0], values[1] = "a", nil
		rows.rows = [][]driver.Value{values}
	case mysqlBagTable, mysqlEquipmentTable, mysqlMailboxTable:
		values[0], values[1] = "19", nil
		rows.rows = [][]driver.Value{values}
	case mysqlSettingsTable:
		if len(args) > 0 {
			values[0] = args[0].Value
		}
		values[1] = nil
		rows.rows = [][]driver.Value{values}
	}
	return rows
}

func raidLedgerQueryColumns(query string) []string {
	prefix := strings.TrimPrefix(query, "SELECT ")
	if end := strings.Index(prefix, " FROM "); end >= 0 {
		prefix = prefix[:end]
	}
	return strings.Split(prefix, ",")
}

func raidLedgerQueryTable(query string) string {
	for _, table := range []string{
		mysqlAccountTable,
		mysqlAccountInventoryTable,
		mysqlCharTable,
		mysqlBagTable,
		mysqlEquipmentTable,
		mysqlMailboxTable,
		mysqlSettingsTable,
	} {
		if strings.Contains(query, "`dnf_"+table+"`") {
			return table
		}
	}
	return ""
}

func raidLedgerTestUnitOfWork(db *sql.DB) *mysqlAccountCharacterAssetUnitOfWork {
	return &mysqlAccountCharacterAssetUnitOfWork{router: mysqlRouter{
		db:          SQLDBAdapter{DB: db},
		readDBs:     []string{"readonly"},
		writeDBs:    []string{"shard_a", "shard_b"},
		tablePrefix: "dnf",
		now:         time.Now,
	}}
}

func raidLedgerLoadAndSaveAll(
	ctx context.Context,
	accounts repo.AccountRepository,
	accountInventory repo.AccountInventoryRepository,
	characters repo.CharacterRepository,
	inventories repo.InventoryRepository,
	equipment repo.EquipmentRepository,
	mailboxes repo.MailboxRepository,
) error {
	account, found, err := accounts.Load(ctx, "a")
	if err != nil {
		return fmt.Errorf("load account: %w", err)
	}
	if !found {
		return errors.New("account a not found")
	}
	if err := accounts.Save(ctx, account); err != nil {
		return fmt.Errorf("save account: %w", err)
	}

	accountItems, found, err := accountInventory.Load(ctx, "a")
	if err != nil {
		return fmt.Errorf("load account inventory: %w", err)
	}
	if !found {
		return errors.New("account inventory a not found")
	}
	if err := accountInventory.Save(ctx, accountItems); err != nil {
		return fmt.Errorf("save account inventory: %w", err)
	}

	character, found, err := characters.Load(ctx, "19")
	if err != nil {
		return fmt.Errorf("load character: %w", err)
	}
	if !found {
		return errors.New("character 19 not found")
	}
	if err := characters.Save(ctx, character); err != nil {
		return fmt.Errorf("save character: %w", err)
	}

	inventory, found, err := inventories.Load(ctx, "19")
	if err != nil {
		return fmt.Errorf("load inventory: %w", err)
	}
	if !found {
		return errors.New("inventory 19 not found")
	}
	if err := inventories.Save(ctx, inventory); err != nil {
		return fmt.Errorf("save inventory: %w", err)
	}

	equipmentRecord, found, err := equipment.Load(ctx, "19")
	if err != nil {
		return fmt.Errorf("load equipment: %w", err)
	}
	if !found {
		return errors.New("equipment 19 not found")
	}
	if err := equipment.Save(ctx, equipmentRecord); err != nil {
		return fmt.Errorf("save equipment: %w", err)
	}

	mailbox, found, err := mailboxes.Load(ctx, "19")
	if err != nil {
		return fmt.Errorf("load mailbox: %w", err)
	}
	if !found {
		return errors.New("mailbox 19 not found")
	}
	if err := mailboxes.Save(ctx, mailbox); err != nil {
		return fmt.Errorf("save mailbox: %w", err)
	}
	return nil
}

func raidLedgerAssertFirstAccountLock(t *testing.T, state *raidLedgerSQLState) {
	t.Helper()
	if len(state.queries) == 0 {
		t.Fatal("transaction issued no account lock query")
	}
	if table := raidLedgerQueryTable(state.queries[0]); table != mysqlAccountTable {
		t.Fatalf("first locked table=%q, want account; query=%s", table, state.queries[0])
	}
	if !strings.HasSuffix(state.queries[0], " FOR UPDATE") {
		t.Fatalf("first account query is not locked: %s", state.queries[0])
	}
}

func raidLedgerQueryTouchesTable(query, table string) bool {
	return strings.Contains(query, "`dnf_"+table+"`")
}

func raidLedgerLockedAggregate(query string) string {
	if !strings.HasPrefix(query, "SELECT ") || !strings.HasSuffix(query, " FOR UPDATE") {
		return ""
	}
	for _, table := range []string{
		mysqlAccountTable,
		mysqlAccountInventoryTable,
		mysqlCharTable,
		mysqlBagTable,
		mysqlEquipmentTable,
		mysqlMailboxTable,
		mysqlSettingsTable,
	} {
		if raidLedgerQueryTouchesTable(query, table) {
			return table
		}
	}
	return ""
}

// The item-stack helper intentionally reads child rows under an already locked
// parent row. Keep that exception narrow: only the known account-bag and
// character-bag child tables may use it, and only after their parent lock.
func raidLedgerChildAggregate(query string) string {
	if !strings.HasPrefix(query, "SELECT ") {
		return ""
	}
	if raidLedgerQueryTouchesTable(query, mysqlAccountInventoryItemsTable) ||
		raidLedgerQueryTouchesTable(query, mysqlAccountInventoryExtraTable) {
		return mysqlAccountInventoryTable
	}
	if raidLedgerQueryTouchesTable(query, mysqlInventoryItemsTable) ||
		raidLedgerQueryTouchesTable(query, mysqlInventoryExtraTable) {
		return mysqlBagTable
	}
	return ""
}

func raidLedgerAssertOnlyTransaction(t *testing.T, state *raidLedgerSQLState) {
	t.Helper()
	if state.outsideTxOperations != 0 {
		t.Fatalf("operations outside the physical transaction=%d", state.outsideTxOperations)
	}
	lockedAggregates := map[string]bool{}
	for _, query := range state.queries {
		if strings.Contains(query, "readonly") {
			t.Fatalf("query used read-only router: %s", query)
		}
		if !strings.HasPrefix(query, "SELECT ") {
			continue
		}
		if aggregate := raidLedgerLockedAggregate(query); aggregate != "" {
			lockedAggregates[aggregate] = true
			continue
		}
		if strings.HasSuffix(query, " FOR UPDATE") {
			// Locked companion tables (metadata/stats/location/roster/mail)
			// do not establish a new aggregate root of their own.
			continue
		}
		aggregate := raidLedgerChildAggregate(query)
		if aggregate == "" {
			t.Fatalf("transactional root read is not locked: %s", query)
		}
		if !lockedAggregates[aggregate] {
			t.Fatalf("child read %s happened before its %s parent lock", query, aggregate)
		}
	}
}

func raidLedgerSawTable(statements []string, table string) bool {
	for _, statement := range statements {
		if raidLedgerQueryTouchesTable(statement, table) {
			return true
		}
	}
	return false
}

func TestMySQLAccountCharacterLedgerMailboxUoWCommitsAllRepositoriesInOneSQLTx(t *testing.T) {
	state := &raidLedgerSQLState{characterAccount: "a"}
	db := sql.OpenDB(raidLedgerSQLConnector{state: state})
	defer db.Close()
	uow := raidLedgerTestUnitOfWork(db)

	err := uow.WithinAccountCharacterLedgerMailboxAssets(
		context.Background(), "a", "19",
		func(
			accounts repo.AccountRepository,
			accountInventory repo.AccountInventoryRepository,
			characters repo.CharacterRepository,
			inventories repo.InventoryRepository,
			equipment repo.EquipmentRepository,
			mailboxes repo.MailboxRepository,
		) error {
			return raidLedgerLoadAndSaveAll(context.Background(), accounts, accountInventory, characters, inventories, equipment, mailboxes)
		},
	)
	if err != nil {
		t.Fatalf("successful ledger mailbox transaction: %v", err)
	}
	if state.begins != 1 || state.commits != 1 || state.rollbacks != 0 {
		t.Fatalf("transaction counts begin/commit/rollback=%d/%d/%d", state.begins, state.commits, state.rollbacks)
	}
	raidLedgerAssertFirstAccountLock(t, state)
	raidLedgerAssertOnlyTransaction(t, state)
	for _, table := range []string{mysqlAccountTable, mysqlAccountInventoryTable, mysqlCharTable, mysqlBagTable, mysqlEquipmentTable, mysqlMailboxTable} {
		if !raidLedgerSawTable(state.queries, table) {
			t.Errorf("repository %s did not read through the transaction", table)
		}
		if !raidLedgerSawTable(state.writeSQL, table) {
			t.Errorf("repository %s did not write through the transaction", table)
		}
	}
}

func TestMySQLAccountCharacterLedgerMailboxUoWRejectsForeignCharacterAccount(t *testing.T) {
	state := &raidLedgerSQLState{characterAccount: "b"}
	db := sql.OpenDB(raidLedgerSQLConnector{state: state})
	defer db.Close()
	uow := raidLedgerTestUnitOfWork(db)
	called := false

	err := uow.WithinAccountCharacterLedgerMailboxAssets(
		context.Background(), "a", "19",
		func(repo.AccountRepository, repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.MailboxRepository) error {
			called = true
			return nil
		},
	)
	if !errors.Is(err, repo.ErrAccountCharacterMailboxOwnership) {
		t.Fatalf("foreign character account error=%v, want %v", err, repo.ErrAccountCharacterMailboxOwnership)
	}
	if called {
		t.Fatal("callback ran after ownership rejection")
	}
	if state.begins != 1 || state.commits != 0 || state.rollbacks != 1 {
		t.Fatalf("transaction counts begin/commit/rollback=%d/%d/%d", state.begins, state.commits, state.rollbacks)
	}
	raidLedgerAssertFirstAccountLock(t, state)
	raidLedgerAssertOnlyTransaction(t, state)
}

type raidLedgerScopeProbe func(
	context.Context,
	repo.AccountRepository,
	repo.AccountInventoryRepository,
	repo.CharacterRepository,
	repo.InventoryRepository,
	repo.EquipmentRepository,
	repo.MailboxRepository,
) error

func TestMySQLAccountCharacterLedgerMailboxUoWRejectsCrossScopeAccess(t *testing.T) {
	probes := []struct {
		name  string
		probe raidLedgerScopeProbe
	}{
		{name: "account", probe: func(ctx context.Context, accounts repo.AccountRepository, _ repo.AccountInventoryRepository, _ repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository, _ repo.MailboxRepository) error {
			_, _, err := accounts.Load(ctx, "other-account")
			return err
		}},
		{name: "account_inventory", probe: func(ctx context.Context, _ repo.AccountRepository, accounts repo.AccountInventoryRepository, _ repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository, _ repo.MailboxRepository) error {
			_, _, err := accounts.Load(ctx, "other-account")
			return err
		}},
		{name: "character", probe: func(ctx context.Context, _ repo.AccountRepository, _ repo.AccountInventoryRepository, characters repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository, _ repo.MailboxRepository) error {
			_, _, err := characters.Load(ctx, "other-character")
			return err
		}},
		{name: "inventory", probe: func(ctx context.Context, _ repo.AccountRepository, _ repo.AccountInventoryRepository, _ repo.CharacterRepository, inventories repo.InventoryRepository, _ repo.EquipmentRepository, _ repo.MailboxRepository) error {
			_, _, err := inventories.Load(ctx, "other-character")
			return err
		}},
		{name: "equipment", probe: func(ctx context.Context, _ repo.AccountRepository, _ repo.AccountInventoryRepository, _ repo.CharacterRepository, _ repo.InventoryRepository, equipment repo.EquipmentRepository, _ repo.MailboxRepository) error {
			_, _, err := equipment.Load(ctx, "other-character")
			return err
		}},
		{name: "mailbox", probe: func(ctx context.Context, _ repo.AccountRepository, _ repo.AccountInventoryRepository, _ repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository, mailboxes repo.MailboxRepository) error {
			_, _, err := mailboxes.Load(ctx, "other-character")
			return err
		}},
	}

	for _, test := range probes {
		t.Run(test.name, func(t *testing.T) {
			state := &raidLedgerSQLState{characterAccount: "a"}
			db := sql.OpenDB(raidLedgerSQLConnector{state: state})
			defer db.Close()
			uow := raidLedgerTestUnitOfWork(db)
			called := false
			queriesAtCallback, queriesAfterProbe := 0, 0

			err := uow.WithinAccountCharacterLedgerMailboxAssets(
				context.Background(), "a", "19",
				func(accounts repo.AccountRepository, accountInventory repo.AccountInventoryRepository, characters repo.CharacterRepository, inventories repo.InventoryRepository, equipment repo.EquipmentRepository, mailboxes repo.MailboxRepository) error {
					called = true
					queriesAtCallback = len(state.queries)
					err := test.probe(context.Background(), accounts, accountInventory, characters, inventories, equipment, mailboxes)
					queriesAfterProbe = len(state.queries)
					return err
				},
			)
			if !called {
				t.Fatal("callback did not run")
			}
			if !errors.Is(err, platformdb.ErrRecordKeyRequired) {
				t.Fatalf("cross-scope error=%v, want %v", err, platformdb.ErrRecordKeyRequired)
			}
			if queriesAfterProbe != queriesAtCallback {
				t.Fatalf("cross-scope access issued %d SQL statements", queriesAfterProbe-queriesAtCallback)
			}
			if state.begins != 1 || state.commits != 0 || state.rollbacks != 1 {
				t.Fatalf("transaction counts begin/commit/rollback=%d/%d/%d", state.begins, state.commits, state.rollbacks)
			}
			raidLedgerAssertFirstAccountLock(t, state)
			raidLedgerAssertOnlyTransaction(t, state)
		})
	}
}

func TestMySQLAccountCharacterLedgerMailboxUoWRollsBackCallbackError(t *testing.T) {
	state := &raidLedgerSQLState{characterAccount: "a"}
	db := sql.OpenDB(raidLedgerSQLConnector{state: state})
	defer db.Close()
	uow := raidLedgerTestUnitOfWork(db)
	callbackErr := errors.New("ledger mailbox callback failed")

	err := uow.WithinAccountCharacterLedgerMailboxAssets(
		context.Background(), "a", "19",
		func(
			accounts repo.AccountRepository,
			accountInventory repo.AccountInventoryRepository,
			characters repo.CharacterRepository,
			inventories repo.InventoryRepository,
			equipment repo.EquipmentRepository,
			mailboxes repo.MailboxRepository,
		) error {
			if err := raidLedgerLoadAndSaveAll(context.Background(), accounts, accountInventory, characters, inventories, equipment, mailboxes); err != nil {
				return err
			}
			return callbackErr
		},
	)
	if !errors.Is(err, callbackErr) {
		t.Fatalf("callback error=%v, want %v", err, callbackErr)
	}
	if state.begins != 1 || state.commits != 0 || state.rollbacks != 1 {
		t.Fatalf("transaction counts begin/commit/rollback=%d/%d/%d", state.begins, state.commits, state.rollbacks)
	}
	if state.writes == 0 {
		t.Fatal("callback rollback did not follow any transactional writes")
	}
	raidLedgerAssertFirstAccountLock(t, state)
	raidLedgerAssertOnlyTransaction(t, state)
}
