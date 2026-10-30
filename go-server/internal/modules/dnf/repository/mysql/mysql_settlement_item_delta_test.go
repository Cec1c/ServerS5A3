package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

// 事务开始前的账号/角色读取使用固定关系行夹具；背包、账号仓库、锁序和回滚走生产实现。
// 不连接玩家数据库，比较最终每一条关系行和实际SQL参数量。
type settlementDeltaConnector struct{ state *cargoDeltaSQLState }
type settlementDeltaDriver struct{ state *cargoDeltaSQLState }
type settlementDeltaConn struct{ cargoDeltaConn }

func (c settlementDeltaConnector) Connect(context.Context) (driver.Conn, error) {
	return settlementDeltaConn{cargoDeltaConn{c.state}}, nil
}
func (c settlementDeltaConnector) Driver() driver.Driver { return settlementDeltaDriver{c.state} }
func (d settlementDeltaDriver) Open(string) (driver.Conn, error) {
	return settlementDeltaConn{cargoDeltaConn{d.state}}, nil
}
func (c settlementDeltaConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	table := raidLedgerQueryTable(query)
	if table != mysqlCharTable && table != mysqlAccountTable {
		return c.cargoDeltaConn.QueryContext(ctx, query, args)
	}
	want := "77"
	if table == mysqlAccountTable {
		want = "acct-77"
		if !strings.HasSuffix(query, " FOR UPDATE") {
			return nil, fmt.Errorf("结算账号读取没有加锁：%s", query)
		}
	}
	if strings.Contains(query, "replica") || len(args) != 1 || args[0].Value != want {
		return nil, fmt.Errorf("错误的结算归属读取：%s %v", query, args)
	}
	c.s.db.calls = append(c.s.db.calls, fakeSQLCall{Query: query, Args: cargoDeltaArgs(args)})
	rows := raidLedgerRowsFor(query, "acct-77", args).(*raidLedgerSQLRows)
	if len(rows.rows) != 1 {
		return nil, fmt.Errorf("缺少结算归属夹具：%s", query)
	}
	rows.rows[0][0] = want
	return rows, nil
}

func settlementDeltaUnitOfWork(state *cargoDeltaSQLState) (*mysqlCharacterSettlementUnitOfWork, *sql.DB) {
	database := sql.OpenDB(settlementDeltaConnector{state})
	u := &mysqlCharacterSettlementUnitOfWork{router: mysqlRouter{
		db: SQLDBAdapter{DB: database}, readDBs: []string{"replica"},
		writeDBs: []string{"dnf_s1_w1"}, tablePrefix: "dnf", now: time.Now,
	}}
	return u, database
}

func assertSettlementDeltaOwnerLocks(t *testing.T, calls []fakeSQLCall) {
	t.Helper()
	account, character := -1, -1
	if len(calls) == 0 || raidLedgerQueryTable(calls[0].Query) != mysqlCharTable || strings.HasSuffix(calls[0].Query, " FOR UPDATE") {
		t.Fatal("缺少事务开始前不加锁的角色归属读取", calls)
	}
	for n, call := range calls {
		if !strings.HasSuffix(call.Query, " FOR UPDATE") {
			continue
		}
		switch raidLedgerQueryTable(call.Query) {
		case mysqlAccountTable:
			if account < 0 {
				account = n
			}
		case mysqlCharTable:
			if character < 0 {
				character = n
			}
		}
	}
	if account < 0 || character <= account {
		t.Fatalf("结算锁顺序错误 account=%d character=%d", account, character)
	}
}

func TestMySQLSettlementItemDeltaMatchesFullSave(t *testing.T) {
	t.Setenv("DNFBRIDGE_MYSQL_INSERT_BATCH", "200")
	for _, scenario := range []string{"提取来源消耗", "原始字段变化", "同事务连续保存", "背包仓库分别保存", "失败回滚", "错误角色拒绝"} {
		t.Run(scenario, func(t *testing.T) {
			var snapshots []map[string][]string
			var writes, parameters []int
			for _, optimized := range []bool{false, true} {
				model, inventory, account := cargoDeltaFixture(t, 80)
				before := model.model.snapshot()
				state := &cargoDeltaSQLState{db: model}
				u, db := settlementDeltaUnitOfWork(state)
				if scenario == "失败回滚" {
					state.failAt = 2
				}
				callbackRan := false
				err := u.WithinCharacterSettlement(context.Background(), "77", func(tx repository.Group) error {
					callbackRan = true
					bag := tx.Inventory.(*characterSettlementScopedInventoryRepository).base.(*mysqlBagStore)
					shared := tx.AccountInventory.(*characterSettlementAccountInventoryRepository)
					if !optimized {
						bag.trackSlotChanges = false
						shared.accounts.(*mysqlAccountInventoryStore).trackSlotChanges = false
					}
					var err error
					inventory, _, err = tx.Inventory.Load(context.Background(), "77")
					if err != nil {
						return err
					}
					account, _, err = tx.AccountInventory.Load(context.Background(), "acct-77")
					if err != nil {
						return err
					}
					delete(inventory.Slots, "0:1")
					stack := account.Slots["12:2"]
					stack.Count++
					account.Slots["12:2"] = stack
					if scenario == "原始字段变化" {
						stack = inventory.Slots["0:3"]
						stack.RawEntry[10] ^= 0x20
						stack.Extra["新增实例字段"] = "保留"
						inventory.Slots["0:3"] = stack
					}
					if scenario == "错误角色拒绝" {
						inventory.CharacterID = "其他角色"
					}
					if err = repository.SaveInventoryFields(context.Background(), tx.Inventory, inventory, repository.InventoryFieldSlots); err != nil {
						return err
					}
					if err = tx.AccountInventory.Save(context.Background(), account); err != nil {
						return err
					}
					if scenario == "同事务连续保存" {
						stack = account.Slots["12:2"]
						stack.Count++
						account.Slots["12:2"] = stack
						return tx.AccountInventory.Save(context.Background(), account)
					}
					if scenario == "背包仓库分别保存" {
						stack = inventory.Warehouse["2:0"]
						stack.Count++
						inventory.Warehouse["2:0"] = stack
						return repository.SaveInventoryFields(context.Background(), tx.Inventory, inventory, repository.InventoryFieldWarehouse)
					}
					return nil
				})
				db.Close()
				if !callbackRan {
					t.Fatal("未进入资产回调，不能证明结算或回滚", err)
				}
				assertSettlementDeltaOwnerLocks(t, model.calls)
				if scenario == "失败回滚" && (state.writes != 2 || err == nil || !strings.Contains(err.Error(), "injected cargo write failure")) {
					t.Fatalf("未触发指定第二次写入失败 writes=%d err=%v", state.writes, err)
				}
				if scenario == "错误角色拒绝" && state.writes != 0 {
					t.Fatal("拒绝错误角色前已写资产", state.writes)
				}
				failed := scenario == "失败回滚" || scenario == "错误角色拒绝"
				if failed {
					if err == nil || !reflect.DeepEqual(before, model.model.snapshot()) || state.commits != 0 {
						t.Fatal("失败改变关系行或错误提交", err)
					}
				} else if err != nil || state.commits != 1 {
					t.Fatal("结算失败或提交次数变化", err, state.commits)
				}
				count, args := 0, 0
				for _, call := range model.calls {
					if !strings.HasPrefix(call.Query, "SELECT ") {
						count++
						args += len(call.Args)
					}
					if optimized && !strings.HasPrefix(call.Query, "SELECT ") {
						for _, arg := range call.Args {
							if arg == "0:79" || arg == "12:79" {
								t.Fatal("重写未变化格子", call.Query)
							}
						}
					}
				}
				snapshots = append(snapshots, model.model.snapshot())
				writes = append(writes, count)
				parameters = append(parameters, args)
			}
			if !reflect.DeepEqual(snapshots[0], snapshots[1]) {
				t.Fatal("差量与完整保存最终关系行不一致")
			}
			if scenario != "失败回滚" && scenario != "错误角色拒绝" && parameters[1]*2 >= parameters[0] {
				t.Fatal("未减少整容器写入放大", parameters)
			}
			t.Logf("80格夹具，写语句%d→%d，写参数%d→%d；最终关系行一致", writes[0], writes[1], parameters[0], parameters[1])
		})
	}
}
