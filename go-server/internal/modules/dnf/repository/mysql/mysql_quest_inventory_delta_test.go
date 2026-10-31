package mysql

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/repository"
)

func questDeltaSeedInventory(t *testing.T, router mysqlRouter) repository.InventoryRecord {
	t.Helper()
	record := repository.InventoryRecord{CharacterID: "77", UpdatedAt: time.Unix(10000, 0).UTC(), Slots: map[string]repository.ItemStack{},
		Warehouse: map[string]repository.ItemStack{"2:1": {ItemID: 9001, Count: 3, Extra: map[string]string{"仓库属性": "必须保留"}}}}
	for i := 0; i < 200; i++ {
		extra := map[string]string{}
		for j := 0; j < 10; j++ {
			extra[fmt.Sprintf("实例属性%d", j)] = fmt.Sprintf("槽%d原值", i)
		}
		record.Slots[fmt.Sprintf("0:%d", i)] = repository.ItemStack{ItemID: int64(10000 + i), Count: 2, Bind: true, RawEntry: []byte{1, 2, 3, byte(i)}, Extra: extra}
	}
	if err := newMySQLGroupFromRouter(router, false).Inventory.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	return record
}

func questDeltaMutation(scenario string) dungeon.InventoryMutation {
	return func(record *repository.InventoryRecord) (bool, error) {
		stack := record.Slots["0:184"]
		switch scenario {
		case "不发放":
			return false, nil
		case "新增任务物品":
			record.Slots["0:240"] = repository.ItemStack{ItemID: 10164913, Count: 1, RawEntry: []byte{8, 9}, Extra: map[string]string{"任务": "2976"}}
		case "删除与重定向":
			delete(record.Slots, "0:184")
			record.Slots["0:240"] = stack
		case "只改实例":
			stack.RawEntry[0] = 9
			stack.Extra["实例属性0"] = "新实例值"
			stack.ExpireAt = time.Unix(30000, 0).UTC()
			record.Slots["0:184"] = stack
		default:
			stack.Count++
			stack.Extra["实例属性0"] = "任务叠堆"
			record.Slots["0:184"] = stack
			if scenario == "多任务物品" {
				other := record.Slots["0:185"]
				other.Count += 2
				record.Slots["0:185"] = other
			}
		}
		if scenario == "回调失败" {
			return false, errors.New("任务发放回调失败")
		}
		return true, nil
	}
}

func TestQuestInventoryDeltaMatchesFullRowsAndPreservesUnrelatedItems(t *testing.T) {
	t.Setenv(repository.InventorySaveScopeEnvName, repository.SaveScopeSlot)
	t.Setenv("DNFBRIDGE_MYSQL_INSERT_BATCH", "200")
	for _, scenario := range []string{"已有任务物品", "新增任务物品", "多任务物品", "删除与重定向", "只改实例", "不发放", "回调失败"} {
		t.Run(scenario, func(t *testing.T) {
			var reference map[string][]string
			var writes []int
			for _, optimized := range []bool{false, true} {
				s, _, router := newDeathDeltaSQL(t)
				seed := questDeltaSeedInventory(t, router)
				initial := deathDeltaSnapshot(s)
				s.calls, s.writes, s.commits = nil, 0, 0
				group := newMySQLGroupFromRouter(router, true)
				owner, err := dungeon.NewOwner(group)
				if err != nil {
					t.Fatal(err)
				}
				result, err := owner.MutateInventory(context.Background(), dungeon.InventoryMutationCommand{CharacterID: "77", UpdatedAt: seed.UpdatedAt,
					SaveChangedSlots: optimized, Apply: questDeltaMutation(scenario)})
				if scenario == "回调失败" {
					if err == nil || s.commits != 0 || !reflect.DeepEqual(initial, deathDeltaSnapshot(s)) || result.Changed {
						t.Fatal("回调失败必须完整回滚", err)
					}
				} else if err != nil || s.commits != 1 || result.Changed != (scenario != "不发放") {
					t.Fatal("结算结果或事务次数变化", err, result.Changed, s.commits)
				}
				state := deathDeltaSnapshot(s)
				if !optimized {
					reference = state
				} else if !reflect.DeepEqual(reference, state) {
					t.Fatal("增量与原完整保存的全部关系行不同")
				}
				writes = append(writes, s.writes)
				if optimized {
					for _, call := range s.calls {
						for _, arg := range call.Args {
							key, ok := arg.(string)
							if _, existing := seed.Slots[key]; ok && existing && key != "0:184" && !(scenario == "多任务物品" && key == "0:185") {
								t.Fatal("增量SQL触及无关槽", key)
							}
						}
					}
				}
			}
			if scenario != "不发放" && scenario != "回调失败" && writes[1] >= writes[0] {
				t.Fatal("未减少无关物品写调用", writes)
			}
			t.Logf("同一事务、同一最终关系行，写调用：%d→%d", writes[0], writes[1])
		})
	}
}

type questInventoryWithoutDelta struct{ repository.InventoryRepository }

func (r questInventoryWithoutDelta) SaveFields(ctx context.Context, record repository.InventoryRecord, fields ...repository.InventoryField) error {
	return repository.SaveInventoryFields(ctx, r.InventoryRepository, record, fields...)
}

type questItemsWithoutDelta struct {
	repository.CharacterItemUnitOfWork
}

func (u questItemsWithoutDelta) WithinCharacterItems(ctx context.Context, id string, apply func(repository.InventoryRepository, repository.EquipmentRepository) error) error {
	return u.CharacterItemUnitOfWork.WithinCharacterItems(ctx, id, func(i repository.InventoryRepository, e repository.EquipmentRepository) error {
		return apply(questInventoryWithoutDelta{i}, e)
	})
}

func TestQuestInventoryDeltaFallbackKeepsOriginalSQL(t *testing.T) {
	for _, scope := range []string{repository.SaveScopeCollection, "未知配置", repository.SaveScopeSlot} {
		t.Run(scope, func(t *testing.T) {
			t.Setenv(repository.InventorySaveScopeEnvName, scope)
			var reference []fakeSQLCall
			for _, optimized := range []bool{false, true} {
				s, _, router := newDeathDeltaSQL(t)
				seed := questDeltaSeedInventory(t, router)
				s.calls, s.writes = nil, 0
				group := newMySQLGroupFromRouter(router, true)
				if scope == repository.SaveScopeSlot {
					group.CharacterItems = questItemsWithoutDelta{group.CharacterItems}
				}
				owner, _ := dungeon.NewOwner(group)
				_, err := owner.MutateInventory(context.Background(), dungeon.InventoryMutationCommand{CharacterID: "77", UpdatedAt: seed.UpdatedAt,
					SaveChangedSlots: optimized, Apply: questDeltaMutation("已有任务物品")})
				if err != nil {
					t.Fatal(err)
				}
				if !optimized {
					reference = s.calls
				} else if !reflect.DeepEqual(reference, s.calls) {
					t.Fatal("回退必须保持原逐条SQL")
				}
			}
		})
	}
}

func TestQuestInventoryDeltaRollsBackEveryWriteFailure(t *testing.T) {
	t.Setenv(repository.InventorySaveScopeEnvName, repository.SaveScopeSlot)
	for failAt := 1; failAt <= 5; failAt++ {
		t.Run(fmt.Sprintf("写%d失败", failAt), func(t *testing.T) {
			s, _, router := newDeathDeltaSQL(t)
			seed := questDeltaSeedInventory(t, router)
			initial := deathDeltaSnapshot(s)
			s.calls, s.writes, s.commits, s.failAt = nil, 0, 0, failAt
			owner, _ := dungeon.NewOwner(newMySQLGroupFromRouter(router, true))
			result, err := owner.MutateInventory(context.Background(), dungeon.InventoryMutationCommand{CharacterID: "77", UpdatedAt: seed.UpdatedAt,
				SaveChangedSlots: true, Apply: questDeltaMutation("已有任务物品")})
			if err == nil || s.writes != failAt || s.commits != 0 || result.Changed || !reflect.DeepEqual(initial, deathDeltaSnapshot(s)) {
				t.Fatal("任一步写入失败都必须完整回滚，不能发布成功结果", err, s.writes, s.commits)
			}
		})
	}
}

func TestDungeonExperiencePlanningSQLPointReadKeepsFullReadValidation(t *testing.T) {
	for _, scenario := range []string{"正常双技能页", "历史无技能页", "无效技能页", "技能记录缺失"} {
		t.Run(scenario, func(t *testing.T) {
			s, _, router := newDeathDeltaSQL(t)
			seedPointLedgerSkills(t, router, 90)
			if scenario == "无效技能页" {
				table, _ := router.writeTable(mysqlSkillTreeTable, "77")
				for _, row := range s.rows[rowModelTableName(table)] {
					row["active_tree"] = int64(2)
				}
			}
			if scenario == "历史无技能页" || scenario == "技能记录缺失" {
				tableName := mysqlSkillTreeTable
				if scenario == "技能记录缺失" {
					tableName = mysqlSkillTable
				}
				table, _ := router.writeTable(tableName, "77")
				s.rows[rowModelTableName(table)] = map[string]map[string]any{}
			}
			initial := deathDeltaSnapshot(s)
			reader := pointLedgerReader(router).Skill
			s.calls, s.writes = nil, 0
			full, fullFound, fullErr := reader.Load(context.Background(), "77")
			fullReads := len(s.calls)
			s.calls = nil
			points, found, err := reader.(repository.SkillPointLedgerRepository).LoadPointLedger(context.Background(), "77")
			if fullFound != found || (fullErr == nil) != (err == nil) || err == nil && found && (full.CharacterID != points.CharacterID || full.Points != points.Points || !full.UpdatedAt.Equal(points.UpdatedAt)) {
				t.Fatal("规划窄读取改变了技能点、有效页码校验或缺失语义", fullErr, err)
			}
			if s.writes != 0 || !reflect.DeepEqual(initial, deathDeltaSnapshot(s)) {
				t.Fatal("规划读取不能修改任何关系行")
			}
			if scenario == "正常双技能页" && fullReads-len(s.calls) != 4 || scenario == "历史无技能页" && fullReads-len(s.calls) != 3 {
				t.Fatal("未减少预期的无关技能集合读取", fullReads, len(s.calls))
			}
			t.Logf("经验规划技能读取：%d→%d，无写入且校验相同", fullReads, len(s.calls))
		})
	}
}
