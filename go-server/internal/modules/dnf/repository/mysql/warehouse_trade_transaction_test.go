package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

// 本文件覆盖账号仓库交易的单事务入口 WithinCharacterWarehouseTrade
// （实现见 mysql_warehouse_trade_transaction.go）。
//
// fake driver 直接复用 transaction_group_inventory_test.go 的 inventoryGroupConnector /
// inventoryGroupSQLState，只在外面套一层很薄的读记录壳：记下每条读落在第几条物理连接上、
// 以及它带的 owner 参数。有了这两样，就能回答本项要求的两件事——"两个账号与两个角色是不是
// 在同一条连接（= 同一个事务）上读的"、"两账号两角色是不是各自都读到了"。没有新框架，
// 产品代码与其它测试文件都不动。
type warehouseTradeTestCase struct {
	name       string
	characters []string
	accounts   []string
	body       string
	commitErr  error

	wantPanic          bool
	wantCallbackFailed bool
	wantUnavailable    bool
	wantCommitFailed   bool
	wantBegin          int
	wantCommit         int
	wantRollback       int
	wantWrites         bool
}

// warehouseTradeTestRead 是一条经过 fake driver 的读。
type warehouseTradeTestRead struct {
	query      string
	owner      string
	connection int64
}

// warehouseTradeTestConn 把读转发给共享 fake 连接并顺手记录；ExecContext 也必须显式透传，
// 否则 database/sql 会退回到 Prepare（fake 只支持直接执行与直接查询）。
type warehouseTradeTestConn struct {
	driver.Conn
	connection int64
	reads      *[]warehouseTradeTestRead
	writes     *[]warehouseTradeTestRead
}

func (c warehouseTradeTestConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	owner := ""
	if len(args) > 0 {
		owner = fmt.Sprint(args[0].Value)
	}
	*c.reads = append(*c.reads, warehouseTradeTestRead{query: query, owner: owner, connection: c.connection})
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c warehouseTradeTestConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.writes != nil {
		owner := ""
		if len(args) > 0 {
			owner = fmt.Sprint(args[0].Value)
		}
		*c.writes = append(*c.writes, warehouseTradeTestRead{query: query, owner: owner, connection: c.connection})
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

type warehouseTradeTestConnector struct {
	base   inventoryGroupConnector
	reads  *[]warehouseTradeTestRead
	writes *[]warehouseTradeTestRead
	next   int64
}

func (c *warehouseTradeTestConnector) Connect(context.Context) (driver.Conn, error) {
	connection := atomic.AddInt64(&c.next, 1)
	return warehouseTradeTestConn{Conn: inventoryGroupConn{state: c.base.state}, connection: connection, reads: c.reads, writes: c.writes}, nil
}

func (c *warehouseTradeTestConnector) Driver() driver.Driver { return c.base.Driver() }

const (
	warehouseTradeBodyTwoParticipants = "two_participants"
	warehouseTradeBodyCallbackError   = "callback_error"
	warehouseTradeBodyCommitError     = "commit_error"
	warehouseTradeBodyPanic           = "panic"
	warehouseTradeBodyThirdCharacter  = "third_character"
	warehouseTradeBodyThirdAccount    = "third_account"
)

func TestMySQLCharacterWarehouseTradeScopesTwoCharactersAndTwoAccounts(t *testing.T) {
	commitFailure := errors.New("commit failed")
	cases := []warehouseTradeTestCase{
		{
			name: "commit", characters: []string{"20", "19"}, accounts: []string{"acc-2", "acc-1"},
			body: warehouseTradeBodyTwoParticipants, wantBegin: 1, wantCommit: 1, wantWrites: true,
		},
		{
			name: "callback_error", characters: []string{"20", "19"}, accounts: []string{"acc-2", "acc-1"},
			body: warehouseTradeBodyCallbackError, wantBegin: 1, wantRollback: 1, wantWrites: true, wantCallbackFailed: true,
		},
		{
			name: "commit_error", characters: []string{"20", "19"}, accounts: []string{"acc-2", "acc-1"},
			body: warehouseTradeBodyCommitError, commitErr: commitFailure, wantBegin: 1, wantCommit: 1, wantWrites: true, wantCommitFailed: true,
		},
		{
			name: "panic_rolls_back", characters: []string{"20", "19"}, accounts: []string{"acc-2", "acc-1"},
			body: warehouseTradeBodyPanic, wantBegin: 1, wantRollback: 1, wantWrites: true, wantPanic: true,
		},
		{
			name: "third_character_rejected", characters: []string{"20", "19"}, accounts: []string{"acc-2", "acc-1"},
			body: warehouseTradeBodyThirdCharacter, wantBegin: 1, wantRollback: 1, wantWrites: true, wantUnavailable: true,
		},
		{
			name: "third_account_rejected", characters: []string{"20", "19"}, accounts: []string{"acc-2", "acc-1"},
			body: warehouseTradeBodyThirdAccount, wantBegin: 1, wantRollback: 1, wantWrites: true, wantUnavailable: true,
		},
		{
			name: "empty_characters_rejected", characters: nil, accounts: []string{"acc-2", "acc-1"},
			body: warehouseTradeBodyTwoParticipants, wantUnavailable: true,
		},
		{
			name: "empty_accounts_rejected", characters: []string{"20", "19"}, accounts: nil,
			body: warehouseTradeBodyTwoParticipants, wantUnavailable: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := &inventoryGroupSQLState{commitErr: tc.commitErr}
			reads := make([]warehouseTradeTestRead, 0, 8)
			writes := make([]warehouseTradeTestRead, 0, 8)
			db := sql.OpenDB(&warehouseTradeTestConnector{base: inventoryGroupConnector{state: state}, reads: &reads, writes: &writes})
			defer db.Close()
			u := &mysqlCharacterTradeUnitOfWork{router: mysqlRouter{
				db:          SQLDBAdapter{DB: db},
				readDBs:     []string{"read_only"},
				writeDBs:    []string{"shard_a", "shard_b"},
				tablePrefix: "dnf",
			}}
			ctx := context.Background()
			callbackRefused := errors.New("warehouse trade callback refused")

			// 与 itemtrade 的账号仓库交易同序：账号仓库先锁，再读两个角色与它们的背包。
			inScope := func(c repository.CharacterRepository, i repository.InventoryRepository, a repository.AccountInventoryRepository) error {
				for _, id := range []string{"acc-1", "acc-2"} {
					record, _, err := a.Load(ctx, id)
					if err != nil {
						return err
					}
					record.AccountID = id
					record.Slots = map[string]repository.ItemStack{"0:365": {ItemID: 10158124, Count: 1}}
					if err := a.Save(ctx, record); err != nil {
						return err
					}
				}
				for _, id := range []string{"19", "20"} {
					if _, _, err := c.Load(ctx, id); err != nil {
						return err
					}
					inventory, _, err := i.Load(ctx, id)
					if err != nil {
						return err
					}
					inventory.CharacterID = id
					inventory.Warehouse = map[string]repository.ItemStack{"2:0": {ItemID: 500001, Count: 2}}
					if err := repository.SaveInventoryFields(ctx, i, inventory, repository.InventoryFieldWarehouse); err != nil {
						return err
					}
				}
				return nil
			}

			invoked := false
			invoke := func() error {
				return u.WithinCharacterWarehouseTrade(ctx, tc.characters, tc.accounts, func(c repository.CharacterRepository, i repository.InventoryRepository, a repository.AccountInventoryRepository) error {
					invoked = true
					if len(reads) != 0 || len(writes) != 2 {
						t.Fatalf("warehouse parents must be locked before any balance read: reads=%+v writes=%+v", reads, writes)
					}
					for n, id := range []string{"acc-1", "acc-2"} {
						if writes[n].owner != id || !strings.Contains(writes[n].query, "ON DUPLICATE KEY UPDATE account_id = account_id") || strings.Contains(writes[n].query, "read_only") {
							t.Fatalf("initial warehouse lock %d=%+v", n, writes[n])
						}
					}
					if state.begins != 1 || state.commits != 0 || state.rollbacks != 0 {
						t.Fatalf("callback ran outside the single open transaction: %+v", state)
					}
					if err := inScope(c, i, a); err != nil {
						return err
					}
					switch tc.body {
					case warehouseTradeBodyCallbackError:
						return callbackRefused
					case warehouseTradeBodyPanic:
						panic("warehouse trade callback panicked")
					case warehouseTradeBodyThirdCharacter:
						// 第三个角色（以及它的背包）必须被拒，而且一条 SQL 都不发。
						queries, writes := len(state.queries), state.writes
						if _, _, err := c.Load(ctx, "99"); !errors.Is(err, repository.ErrCharacterTradeTransactionUnavailable) {
							t.Fatalf("third character load err=%v", err)
						}
						if _, _, err := i.Load(ctx, "99"); !errors.Is(err, repository.ErrCharacterTradeTransactionUnavailable) {
							t.Fatalf("third inventory load err=%v", err)
						}
						if err := repository.SaveInventoryFields(ctx, i, repository.InventoryRecord{CharacterID: "99"}, repository.InventoryFieldWarehouse); !errors.Is(err, repository.ErrCharacterTradeTransactionUnavailable) {
							t.Fatalf("third inventory save err=%v", err)
						}
						if len(state.queries) != queries || state.writes != writes {
							t.Fatalf("out of scope character reached SQL: queries=%d writes=%d", len(state.queries), state.writes)
						}
						return repository.ErrCharacterTradeTransactionUnavailable
					case warehouseTradeBodyThirdAccount:
						// 第三个账号仓库同样必须被拒，而且一条 SQL 都不发。
						queries, writes := len(state.queries), state.writes
						if _, _, err := a.Load(ctx, "acc-3"); !errors.Is(err, repository.ErrCharacterTradeTransactionUnavailable) {
							t.Fatalf("third account load err=%v", err)
						}
						if err := a.Save(ctx, repository.AccountInventoryRecord{AccountID: "acc-3"}); !errors.Is(err, repository.ErrCharacterTradeTransactionUnavailable) {
							t.Fatalf("third account save err=%v", err)
						}
						if len(state.queries) != queries || state.writes != writes {
							t.Fatalf("out of scope account reached SQL: queries=%d writes=%d", len(state.queries), state.writes)
						}
						return repository.ErrCharacterTradeTransactionUnavailable
					}
					return nil
				})
			}

			var err error
			func() {
				defer func() {
					recovered := recover()
					if tc.wantPanic {
						if recovered == nil {
							t.Error("expected the callback panic to escape the transaction")
						}
						return
					}
					if recovered != nil {
						t.Errorf("unexpected panic: %v", recovered)
					}
				}()
				err = invoke()
			}()

			if !tc.wantPanic {
				switch {
				case tc.wantUnavailable:
					if !errors.Is(err, repository.ErrCharacterTradeTransactionUnavailable) {
						t.Fatalf("err=%v, want the unavailable sentinel", err)
					}
				case tc.wantCallbackFailed:
					if !errors.Is(err, callbackRefused) {
						t.Fatalf("err=%v, want the callback error to survive the rollback", err)
					}
				case tc.wantCommitFailed:
					if err == nil || !strings.Contains(err.Error(), commitFailure.Error()) {
						t.Fatalf("err=%v, want the commit failure", err)
					}
				default:
					if err != nil {
						t.Fatalf("err=%v", err)
					}
				}
			}
			if invoked != (tc.wantBegin == 1) {
				t.Fatalf("callback invoked=%v, want %v", invoked, tc.wantBegin == 1)
			}
			if state.begins != tc.wantBegin || state.commits != tc.wantCommit || state.rollbacks != tc.wantRollback {
				t.Fatalf("transaction counts %+v, want begins=%d commits=%d rollbacks=%d", state, tc.wantBegin, tc.wantCommit, tc.wantRollback)
			}
			if tc.wantWrites != (state.writes > 0) {
				t.Fatalf("writes=%d, want writes=%v", state.writes, tc.wantWrites)
			}
			if tc.wantBegin == 0 {
				if len(reads) != 0 {
					t.Fatalf("a rejected scope still ran SQL: %+v", reads)
				}
				return
			}

			if len(reads) != 6 {
				t.Fatalf("recorded %d reads (%+v), want 2 account, 2 character and 2 inventory reads", len(reads), reads)
			}
			connection := reads[0].connection
			owners := map[string]int{}
			characters, inventories, accountInventories := 0, 0, 0
			for _, read := range reads {
				if read.connection != connection {
					t.Fatalf("read %q ran on connection %d instead of the transaction connection %d", read.query, read.connection, connection)
				}
				if !strings.HasSuffix(read.query, " FOR UPDATE") {
					t.Fatalf("read without a row lock inside the warehouse trade transaction: %s", read.query)
				}
				if strings.Contains(read.query, "read_only") {
					t.Fatalf("read served by the read replica instead of a write schema: %s", read.query)
				}
				if !strings.Contains(read.query, "shard_a") && !strings.Contains(read.query, "shard_b") {
					t.Fatalf("read not qualified with a write schema: %s", read.query)
				}
				owners[read.owner]++
				switch {
				case strings.Contains(read.query, "dnf_account_inventories"):
					accountInventories++
				case strings.Contains(read.query, "dnf_inventories"):
					inventories++
				case strings.Contains(read.query, "dnf_characters"):
					characters++
				default:
					t.Fatalf("unexpected read target: %s", read.query)
				}
			}
			if characters != 2 || inventories != 2 || accountInventories != 2 {
				t.Fatalf("reads per table characters=%d inventories=%d account_inventories=%d, want 2/2/2", characters, inventories, accountInventories)
			}
			for _, owner := range []string{"19", "20", "acc-1", "acc-2"} {
				if owners[owner] == 0 {
					t.Fatalf("owner %q was never read on the transaction connection: %v", owner, owners)
				}
			}
			if len(owners) != 4 {
				t.Fatalf("reads touched owners outside the scope: %v", owners)
			}
		})
	}
}
