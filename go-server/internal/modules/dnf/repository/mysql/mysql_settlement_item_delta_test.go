package mysql

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

// 账号归属读取用固定角色夹具；背包、账号仓库、SQL事务和回滚均走生产实现。
// 不连接玩家数据库，比较最终每一条关系行和实际SQL参数量。
type settlementDeltaCharacterFixture struct{ repository.CharacterRepository }

func (settlementDeltaCharacterFixture) Load(_ context.Context, id string) (repository.CharacterRecord, bool, error) {
	if id != "77" {
		return repository.CharacterRecord{}, false, fmt.Errorf("错误的角色归属读取：%s", id)
	}
	return repository.CharacterRecord{CharacterID: id, AccountID: "acct-77"}, true, nil
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
				items, db := cargoDeltaUOW(state)
				u := &mysqlCharacterSettlementUnitOfWork{router: items.router}
				if scenario == "失败回滚" {
					state.failAt = 2
				}
				err := u.WithinCharacterSettlement(context.Background(), "77", func(tx repository.Group) error {
					bag := tx.Inventory.(*characterSettlementScopedInventoryRepository).base.(*mysqlBagStore)
					shared := tx.AccountInventory.(*characterSettlementAccountInventoryRepository)
					shared.characters = settlementDeltaCharacterFixture{}
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
