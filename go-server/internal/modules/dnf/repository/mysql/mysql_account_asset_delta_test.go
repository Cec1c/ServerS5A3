package mysql

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

// 通过生产资产事务与真实 database/sql 接口复现分解、提取的写入放大；不连接本机玩家库。
func TestMySQLAccountAssetDeltaMatchesFullSave(t *testing.T) {
	for _, scenario := range []string{"分解合并产物", "提取原始字段", "分开保存背包仓库", "同事务二次保存"} {
		t.Run(scenario, func(t *testing.T) {
			var snapshots []map[string][]string
			var writes []int
			for _, transaction := range []bool{false, true} {
				model, inventory, account := cargoDeltaFixture(t, 80)
				state := &cargoDeltaSQLState{db: model}
				items, db := cargoDeltaUOW(state)
				assets := &mysqlAccountCharacterAssetUnitOfWork{router: items.router}
				ctx := context.Background()
				mutate := func() {
					delete(inventory.Slots, "0:1")
					stack := account.Slots["12:2"]
					stack.Count += 3
					account.Slots["12:2"] = stack
					if scenario == "提取原始字段" {
						stack := inventory.Slots["0:2"]
						stack.RawEntry[10] ^= 0x20
						delete(stack.Extra, "pvf_path")
						stack.Extra["新的实例字段"] = "保留"
						inventory.Slots["0:2"] = stack
					}
					if scenario == "分开保存背包仓库" {
						stack := inventory.Warehouse["2:0"]
						stack.Count++
						inventory.Warehouse["2:0"] = stack
					}
				}
				apply := func(ar repository.AccountInventoryRepository, ir repository.InventoryRepository) error {
					mutate()
					if scenario == "分开保存背包仓库" {
						if err := repository.SaveInventoryFields(ctx, ir, inventory, repository.InventoryFieldWarehouse); err != nil {
							return err
						}
					}
					if err := ir.Save(ctx, inventory); err != nil {
						return err
					}
					if err := ar.Save(ctx, account); err != nil {
						return err
					}
					if scenario == "同事务二次保存" {
						stack := account.Slots["12:2"]
						stack.Count++
						account.Slots["12:2"] = stack
						return ar.Save(ctx, account)
					}
					return nil
				}
				var err error
				if transaction {
					err = assets.WithinAccountCharacterAssets(ctx, account.AccountID, inventory.CharacterID, func(ar repository.AccountInventoryRepository, _ repository.CharacterRepository, ir repository.InventoryRepository, _ repository.EquipmentRepository) error {
						var found bool
						inventory, found, err = ir.Load(ctx, inventory.CharacterID)
						if err != nil || !found {
							return fmt.Errorf("背包读取：found=%v err=%v", found, err)
						}
						account, found, err = ar.Load(ctx, account.AccountID)
						if err != nil || !found {
							return fmt.Errorf("账号仓库读取：found=%v err=%v", found, err)
						}
						return apply(ar, ir)
					})
				} else {
					group := newRowModelGroup(t, model, false)
					err = apply(group.AccountInventory, group.Inventory)
				}
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
				snapshots = append(snapshots, model.model.snapshot())
				count := 0
				for _, call := range model.calls {
					if !strings.HasPrefix(call.Query, "SELECT ") {
						count++
						if transaction {
							for _, arg := range call.Args {
								if arg == "0:79" || arg == "12:79" {
									t.Fatal("重写未变化的槽位", call.Query)
								}
							}
						}
					}
				}
				writes = append(writes, count)
			}
			if !reflect.DeepEqual(snapshots[0], snapshots[1]) {
				t.Fatal("资产事务与完整保存的最终关系行不同")
			}
			t.Logf("完整保存SQL=%d，资产事务SQL=%d", writes[0], writes[1])
			if writes[1] >= writes[0]/4 {
				t.Fatalf("仍在逐件重写未变化槽位：完整保存=%d，资产事务=%d", writes[0], writes[1])
			}
		})
	}
}

func TestMySQLAccountAssetDeltaRollbackAtEveryWrite(t *testing.T) {
	// 成功路径先给出实际写语句数，再逐个注入失败；每一步都必须回到原有全部关系行。
	writes := 0
	for failAt := 0; failAt <= writes; failAt++ {
		model, inventory, account := cargoDeltaFixture(t, 10)
		before := model.model.snapshot()
		state := &cargoDeltaSQLState{db: model, failAt: failAt}
		items, db := cargoDeltaUOW(state)
		assets := &mysqlAccountCharacterAssetUnitOfWork{router: items.router}
		ctx := context.Background()
		err := assets.WithinAccountCharacterAssets(ctx, account.AccountID, inventory.CharacterID, func(ar repository.AccountInventoryRepository, _ repository.CharacterRepository, ir repository.InventoryRepository, _ repository.EquipmentRepository) error {
			i, _, err := ir.Load(ctx, inventory.CharacterID)
			if err != nil {
				return err
			}
			a, _, err := ar.Load(ctx, account.AccountID)
			if err != nil {
				return err
			}
			delete(i.Slots, "0:1")
			stack := a.Slots["12:2"]
			stack.Count++
			a.Slots["12:2"] = stack
			if err := repository.SaveInventoryFields(ctx, ir, i, repository.InventoryFieldSlots); err != nil {
				return err
			}
			return ar.Save(ctx, a)
		})
		db.Close()
		if failAt == 0 {
			if err != nil || state.commits != 1 {
				t.Fatalf("成功路径：err=%v commits=%d", err, state.commits)
			}
			writes = state.writes
			continue
		}
		if err == nil || state.commits != 0 || state.rollbacks != 1 || !reflect.DeepEqual(before, model.model.snapshot()) {
			t.Fatalf("第%d条写失败未完整回滚：err=%v commits=%d rollbacks=%d", failAt, err, state.commits, state.rollbacks)
		}
	}
	t.Logf("已核对%d个实际写入失败点的完整回滚", writes)
}

func TestMySQLAccountAssetDeltaSnapshotAndScopeGuards(t *testing.T) {
	for _, scenario := range []string{"未加载快照", "没有槽位变化", "跨账号", "跨角色"} {
		t.Run(scenario, func(t *testing.T) {
			model, inventory, account := cargoDeltaFixture(t, 10)
			before := model.model.snapshot()
			state := &cargoDeltaSQLState{db: model}
			items, db := cargoDeltaUOW(state)
			defer db.Close()
			assets := &mysqlAccountCharacterAssetUnitOfWork{router: items.router}
			ctx := context.Background()
			err := assets.WithinAccountCharacterAssets(ctx, account.AccountID, inventory.CharacterID, func(ar repository.AccountInventoryRepository, _ repository.CharacterRepository, ir repository.InventoryRepository, _ repository.EquipmentRepository) error {
				var err error
				if scenario != "未加载快照" {
					inventory, _, err = ir.Load(ctx, inventory.CharacterID)
					if err != nil {
						return err
					}
					account, _, err = ar.Load(ctx, account.AccountID)
					if err != nil {
						return err
					}
				}
				if scenario == "跨账号" {
					account.AccountID = "其它账号"
					return ar.Save(ctx, account)
				}
				if scenario == "跨角色" {
					inventory.CharacterID = "其它角色"
					return ir.Save(ctx, inventory)
				}
				if err := ir.Save(ctx, inventory); err != nil {
					return err
				}
				return ar.Save(ctx, account)
			})
			if scenario == "跨账号" || scenario == "跨角色" {
				if err == nil || state.commits != 0 || state.rollbacks != 1 || state.writes != 0 {
					t.Fatalf("越权未在写入前拒绝：err=%v state=%+v", err, state)
				}
			} else if err != nil || state.commits != 1 {
				t.Fatalf("合法保存失败：err=%v commits=%d", err, state.commits)
			}
			if !reflect.DeepEqual(before, model.model.snapshot()) {
				t.Fatal("未变化保存或失败请求改变了关系行")
			}
			if scenario == "没有槽位变化" && state.writes != 2 {
				t.Fatalf("无槽位变化应只保存两个父时间戳，实际SQL=%d", state.writes)
			}
			if scenario == "未加载快照" && state.writes < 100 {
				t.Fatalf("没有完整快照时未保持完整保存回退，SQL=%d", state.writes)
			}
		})
	}
}
