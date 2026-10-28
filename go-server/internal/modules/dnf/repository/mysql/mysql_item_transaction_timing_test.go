package mysql

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

func TestMySQLItemTimingKeepsSQLResultsAndRollback(t *testing.T) {
	for _, scenario := range []string{"成功", "回调失败", "写入失败"} {
		t.Run(scenario, func(t *testing.T) {
			var referenceSQL []fakeSQLCall
			var referenceState map[string][]string
			var referenceCommits int
			for _, enabled := range []bool{false, true} {
				s, _, router := newDeathDeltaSQL(t)
				group := newMySQLGroupFromRouter(router, false)
				ctx := context.Background()
				if err := group.Inventory.Save(ctx, repository.InventoryRecord{CharacterID: "77", Slots: map[string]repository.ItemStack{
					"0:184": {ItemID: 10164911, Count: 1}, "0:9": {ItemID: 101, Count: 2},
				}}); err != nil {
					t.Fatal(err)
				}
				initial := deathDeltaSnapshot(s)
				s.calls, s.writes, s.commits = nil, 0, 0
				if scenario == "写入失败" {
					s.failAt = 2
				}
				var timing *repository.TransactionTiming
				if enabled {
					ctx, timing = repository.WithTransactionTiming(ctx)
				}
				rejected := errors.New("任务入包隔离回调拒绝")
				err := (&mysqlCharacterItemUnitOfWork{router: router}).WithinCharacterItems(ctx, "77", func(inventories repository.InventoryRepository, _ repository.EquipmentRepository) error {
					inventory, found, err := inventories.Load(ctx, "77")
					if err != nil || !found {
						t.Fatal("背包读取失败", err)
					}
					if scenario == "回调失败" {
						return rejected
					}
					stack := inventory.Slots["0:184"]
					stack.Count++
					inventory.Slots["0:184"] = stack
					return repository.SaveInventoryFields(ctx, inventories, inventory, repository.InventoryFieldSlots)
				})
				state := deathDeltaSnapshot(s)
				if scenario == "成功" {
					if err != nil || s.commits != 1 {
						t.Fatal("任务入包未成功提交", err)
					}
				} else if err == nil || s.commits != 0 || !reflect.DeepEqual(initial, state) {
					t.Fatal("失败未完整回滚", err)
				}
				if scenario == "回调失败" && !errors.Is(err, rejected) {
					t.Fatal("回调错误丢失", err)
				}
				if !enabled {
					referenceSQL, referenceState, referenceCommits = s.calls, state, s.commits
					continue
				}
				if !reflect.DeepEqual(referenceSQL, s.calls) || !reflect.DeepEqual(referenceState, state) || referenceCommits != s.commits {
					t.Fatal("计时改变了SQL、最终关系行或事务次数")
				}
				v := timing.Snapshot()
				if v.Calls[repository.TransactionBegin] != 1 || v.Calls[repository.TransactionLockRead] == 0 ||
					v.Calls[repository.TransactionLockRead]+v.Calls[repository.TransactionRead]+v.Calls[repository.TransactionWrite] != len(s.calls) {
					t.Fatal("事务内部计数遗漏或重复", v, len(s.calls))
				}
				if scenario == "成功" && v.Calls[repository.TransactionCommit] != 1 || scenario != "成功" && v.Calls[repository.TransactionRollback] != 1 {
					t.Fatal("提交或回滚计时与原事务不一致", v)
				}
			}
		})
	}
}
