package equipmentcompound

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type testSource map[string]string

func (s testSource) ReadText(name string) (string, error) {
	text, found := s[name]
	if !found {
		return "", fmt.Errorf("缺少%s", name)
	}
	return text, nil
}

const testRules = "[choose part]\n1 `武器` `katana` `knuckle`\n[/choose part]\n[choose part]\n2 `防具` `cl coat`\n[/choose part]\n[choose part]\n3 `首饰` `amulet`\n[/choose part]\n[choose part]\n4 `特殊装备` `earring`\n[/choose part]\n" +
	"[compound need material for specific]\n3 2 10099774 3 3167 50 6 2 10099775 2 3167 100\n[/compound need material for specific]\n" +
	"[compound specific apply cost by level]\n3 1 1.00 3 85 3.00 3 90 4.00 6 1 1.00 6 85 3.00 6 90 4.00\n[/compound specific apply cost by level]\n" +
	"[except item index]\n9001\n[/except item index]\n[probability exception handling]\n1001 0.25 2001 0.50\n[/probability exception handling]\n"
const testLevels = "[regen level limit]\n3 1 100000 0 3 85 90000 10000 3 90 100000 0 6 1 100000 0 6 85 95000 5000 6 90 100000 0\n"

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	source := testSource{RulesPath: testRules, LevelsPath: testLevels}
	list := ""
	for _, row := range []struct {
		id            int64
		rarity, level int
		legacy        bool
		extra         string
	}{
		{1001, 3, 85, false, ""}, {1002, 3, 90, false, ""}, {1005, 3, 85, false, ""}, {1006, 3, 90, false, ""},
		{1101, 3, 85, true, ""}, {1102, 3, 90, true, ""},
		{2001, 6, 85, false, ""}, {2002, 6, 90, false, ""}, {2003, 6, 85, false, ""}, {2004, 6, 90, false, ""},
		{9001, 3, 85, false, ""}, {9002, 3, 85, false, "[creation rate]\n0\n[force result item]\n1 0\n"},
		{9003, 3, 85, false, "[attach type]\n`[account]`\n"}, {9004, 3, 85, false, "[usable period]\n1\n"},
	} {
		name := fmt.Sprintf("equipment/%d.equ", row.id)
		list += fmt.Sprintf("%d `%d.equ`\n", row.id, row.id)
		category := ""
		if row.legacy {
			category = "[item category]\n`legacy`\n"
		}
		text := fmt.Sprintf("[name]\n`装备%d`\n[rarity]\n%d\n[minimum level]\n%d\n[creation rate]\n100\n[attach type]\n`[sealing]`\n[item group name]\n`katana`\n[equipment type]\n`[weapon]`\n%s", row.id, row.rarity, row.level, category)
		if row.id == 9002 {
			text = strings.Replace(text, "[creation rate]\n100\n", "", 1)
		}
		if row.id == 9003 {
			text = strings.Replace(text, "[attach type]\n`[sealing]`\n", "", 1)
		}
		source[name] = text + row.extra
	}
	source["equipment/equipment.lst"] = list
	catalog, err := NewCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.Preload(context.Background()); err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestEquipmentCompound90DecimalMaterialsAndUpgrade(t *testing.T) {
	c := testCatalog(t)
	for _, tc := range []struct {
		rarity, level int
		want          []Material
		same, up      int64
	}{
		{3, 85, []Material{{10099774, 9}, {3167, 150}}, 90000, 10000},
		{6, 85, []Material{{10099775, 6}, {3167, 300}}, 95000, 5000},
		{3, 90, []Material{{10099774, 12}, {3167, 200}}, 100000, 0},
		{6, 90, []Material{{10099775, 8}, {3167, 400}}, 100000, 0},
	} {
		requirements, err := c.Rules.Requirements(tc.rarity, tc.level)
		if err != nil || !reflect.DeepEqual(requirements, tc.want) {
			t.Fatalf("材料%+v，错误%v", requirements, err)
		}
		levels, err := c.Rules.TargetLevels(tc.rarity, tc.level)
		if err != nil || levels[0] != (Level{tc.level, tc.same}) {
			t.Fatalf("等级%+v %v", levels, err)
		}
		if tc.up > 0 && (len(levels) != 2 || levels[1] != (Level{90, tc.up})) {
			t.Fatalf("85→90等级%+v", levels)
		}
		if tc.up == 0 && len(levels) != 1 {
			t.Fatal("90级仍有继续升级池")
		}
	}
}

func TestEquipmentCompound90BadConfigurationRejected(t *testing.T) {
	for name, text := range map[string]string{
		"倍率不能用整数正则拆分": strings.Replace(testRules, "3.00", "3.50", 1),
		"材料重复":        strings.Replace(testRules, "3 2 10099774 3 3167 50", "3 2 10099774 3 10099774 50", 1),
		"旧86部位":       strings.Replace(testRules, "4 `特殊装备`", "14 `特殊装备`", 1),
		"缺传说材料":       strings.Replace(testRules, " 6 2 10099775 2 3167 100", "", 1),
		"坏权重":         strings.Replace(testRules, "0.25", "-0.25", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadRules(testSource{RulesPath: text, LevelsPath: testLevels}); !errors.Is(err, ErrRules) {
				t.Fatalf("规则被放行：%v", err)
			}
		})
	}
}

func TestEquipmentCompound90FamilyPoolsAndExclusions(t *testing.T) {
	c := testCatalog(t)
	for _, id := range []int64{1001, 1101, 2001} {
		source, err := c.Resolve(id)
		if err != nil {
			t.Fatal(err)
		}
		pools, err := c.Pools(source, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, pool := range pools {
			for _, candidate := range pool.Candidates {
				if candidate.Definition.Rarity != source.Rarity || !c.Rules.Parts[1][candidate.Definition.Group] {
					t.Fatal("混入其他类别")
				}
			}
		}
	}
	for _, id := range []int64{9001, 9002, 9003, 9004} {
		if _, err := c.Resolve(id); !errors.Is(err, ErrSource) {
			t.Fatalf("无效装备%d进池：%v", id, err)
		}
	}
	if c.Rules.Weights[1001] != 250000 || c.Rules.Weights[2001] != 500000 {
		t.Fatal("小数权重转换错误")
	}
	source, _ := c.Resolve(1001)
	if _, err := c.Pools(source, 3); !errors.Is(err, ErrSource) {
		t.Fatal("把90首饰组当成86鬼剑武器组")
	}
	delete(c.buckets, bucketKey{3, 90, 1})
	if pools, err := c.Pools(source, 1); err != nil || len(pools) != 1 || pools[0].MinimumLevel != 85 {
		t.Fatal("缺合法升级候选时没有保留同级合成")
	}
	delete(c.buckets, bucketKey{3, 85, 1})
	if pools, err := c.Pools(source, 1); err != nil || len(pools) != 1 || len(pools[0].Candidates) != 1 || pools[0].Candidates[0].Definition != source {
		t.Fatal("缺同级池时没有用已加载的合法来源模板生成新装备")
	}
	delete(c.definitions, source.ItemID)
	if _, err := c.Pools(source, 1); !errors.Is(err, ErrCandidates) {
		t.Fatal("未加载来源模板却放行空产出池")
	}
}

// 当前90的30级传说生成率为0，35级对应项链只有不可生成的体验模板。
// 生成率是自然掉落配置，不能照搬86的85级特例来限制本版本低级传说。
func TestEquipmentCompound90LowLegendAndSparseUpgradePool(t *testing.T) {
	for _, upgradeAvailable := range []bool{false, true} {
		t.Run(fmt.Sprint(upgradeAvailable), func(t *testing.T) {
			source := testCatalog(t).source.(testSource)
			source[LevelsPath] = strings.Replace(testLevels, "6 1 100000 0", "6 1 100000 0 6 30 95000 5000", 1)
			for _, id := range []int{4000, 4001, 4002} {
				level, attach := 30, "sealing"
				if id == 4002 {
					level, attach = 35, "trade"
					if upgradeAvailable {
						attach = "sealing"
					}
				}
				source["equipment/equipment.lst"] += fmt.Sprintf("%d `%d.equ`\n", id, id)
				source[fmt.Sprintf("equipment/%d.equ", id)] = fmt.Sprintf("[name]\n`低级传说%d`\n[rarity]\n6\n[minimum level]\n%d\n[creation rate]\n0\n[attach type]\n`[%s]`\n[item group name]\n`amulet`\n[equipment type]\n`[amulet]`\n", id, level, attach)
			}
			catalog, err := NewCatalog(source)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = catalog.Preload(context.Background()); err != nil {
				t.Fatal(err)
			}
			definition, err := catalog.Resolve(4000)
			if err != nil {
				t.Fatalf("30级合法传说被拒绝：%v", err)
			}
			pools, err := catalog.Pools(definition, 3)
			if err != nil || len(pools) == 0 || pools[0].MinimumLevel != 30 || len(pools[0].Candidates) != 2 {
				t.Fatalf("30级同类池错误：%+v %v", pools, err)
			}
			if upgradeAvailable {
				if len(pools) != 2 || pools[0].Weight != 95000 || pools[1].Weight != 5000 || pools[1].Candidates[0].Definition.ItemID != 4002 {
					t.Fatal("有合法升级产出时没有保留95%/5%")
				}
			} else if len(pools) != 1 || pools[0].Weight != 95000 {
				t.Fatal("不存在合法35级项链时生成了体验模板或虚构升级")
			}
			owner, group, cmd := testOwner(t, 4000)
			cmd.Catalog, cmd.Request.Part = catalog, 3
			calls := 0
			cmd.Random = func(limit int64) int64 {
				calls++
				if calls == 1 {
					return 0
				}
				return WeightUnit
			}
			result, err := owner.Compound(context.Background(), cmd)
			if err != nil || result.Output.ItemID != 4001 || calls != 2 {
				t.Fatalf("30级合成事务错误：产出%d 抽样%d 错误%v", result.Output.ItemID, calls, err)
			}
			inventory, _, _ := group.Inventory.Load(context.Background(), "2")
			account, _, _ := group.AccountInventory.Load(context.Background(), "account-1")
			if inventory.Slots["0:9"].ItemID != 4001 || inventory.Slots["0:121"].Count != 900 || account.Slots["0:364"].Count != 98 {
				t.Fatal("30级合成没有准确扣2传说灵魂与100结晶")
			}
			delete(catalog.buckets, bucketKey{6, 30, 3})
			pools, err = catalog.Pools(definition, 3)
			if err != nil || len(pools) == 0 || pools[0].Weight != 95000 || len(pools[0].Candidates) != 1 || pools[0].Candidates[0].Definition != definition {
				t.Fatal("同级池缺失没有保留合法来源模板与PVF权重")
			}
			if upgradeAvailable && (len(pools) != 2 || pools[1].Weight != 5000) || !upgradeAvailable && len(pools) != 1 {
				t.Fatal("缺同级池改变了可用升级分支")
			}
		})
	}
}

func TestEquipmentCompound90TradableFamiliesAcrossLevels(t *testing.T) {
	for _, level := range []int{1, 13, 30, 45, 60, 70, 80, 85, 86, 89, 90} {
		for _, family := range []struct {
			name   string
			rarity int
			legacy bool
		}{{"粉装", 3, false}, {"圣物", 3, true}, {"传说", 6, false}} {
			t.Run(fmt.Sprintf("%s/%d", family.name, level), func(t *testing.T) {
				source := testSource{RulesPath: testRules, LevelsPath: testLevels,
					"equipment/equipment.lst": "5000 `5000.equ`\n5001 `5001.equ`\n"}
				category := ""
				if family.legacy {
					category = "[item category]\n`legacy`\n"
				}
				for _, id := range []int{5000, 5001} {
					source[fmt.Sprintf("equipment/%d.equ", id)] = fmt.Sprintf("[name]\n`可交易装备%d`\n[rarity]\n%d\n[minimum level]\n%d\n[creation rate]\n100\n[attach type]\n`[sealing]`\n[item group name]\n`katana`\n[equipment type]\n`[weapon]`\n%s", id, family.rarity, level, category)
				}
				catalog, err := NewCatalog(source)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = catalog.Preload(context.Background()); err != nil {
					t.Fatal(err)
				}
				owner, group, cmd := testOwner(t, 5000)
				cmd.Catalog = catalog
				calls := 0
				cmd.Random = func(limit int64) int64 {
					calls++
					if calls == 1 {
						return 0
					}
					return WeightUnit
				}
				result, err := owner.Compound(context.Background(), cmd)
				if err != nil || result.Output.ItemID != 5001 || result.Output.MinimumLevel != level || calls != 2 {
					t.Fatalf("可交易三类装备不同等级合成错误：%+v %v", result, err)
				}
				requirements, err := catalog.Rules.Requirements(family.rarity, level)
				if err != nil {
					t.Fatal(err)
				}
				inventory, _, _ := group.Inventory.Load(context.Background(), "2")
				account, _, _ := group.AccountInventory.Load(context.Background(), "account-1")
				for _, material := range requirements {
					if material.ItemID == 3167 && inventory.Slots["0:121"].Count != 1000-material.Count {
						t.Fatal("等级倍率没有准确扣除结晶")
					}
					if material.ItemID == 10099774 && account.Slots["0:363"].Count != 100-material.Count {
						t.Fatal("等级倍率没有准确扣除神器灵魂")
					}
					if material.ItemID == 10099775 && account.Slots["0:364"].Count != 100-material.Count {
						t.Fatal("等级倍率没有准确扣除传说灵魂")
					}
				}
			})
		}
	}
}

func testOwner(t *testing.T, sourceID int64) (*Owner, repository.Group, Command) {
	t.Helper()
	group := memory.NewMemoryGroup()
	var err error
	ctx := context.Background()
	if err = group.Character.Save(ctx, repository.CharacterRecord{CharacterID: "2", AccountID: "account-1", Stats: map[string]int64{"gold": 1234}}); err != nil {
		t.Fatal(err)
	}
	source := repository.ItemStack{ItemID: sourceID, Count: 1, Extra: map[string]string{"item_kind": "equipment", "seal_flag": "1", "old_option": "保持源数据用于失败回滚"}}
	if err = group.Inventory.Save(ctx, repository.InventoryRecord{CharacterID: "2", Slots: map[string]repository.ItemStack{"0:9": source, "0:121": {ItemID: 3167, Count: 1000}}}); err != nil {
		t.Fatal(err)
	}
	if err = group.AccountInventory.Save(ctx, repository.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]repository.ItemStack{"0:363": {ItemID: 10099774, Count: 100}, "0:364": {ItemID: 10099775, Count: 100}}}); err != nil {
		t.Fatal(err)
	}
	o, err := NewOwner(group)
	if err != nil {
		t.Fatal(err)
	}
	cmd := Command{AccountID: "account-1", CharacterID: "2", Request: Request{9, 0, 1}, Catalog: testCatalog(t), EquipmentPolicy: itemplacement.SlotPolicy{ListType: 0, Start: 9, End: 64, AllowQuickSlots: true}, MaterialPolicies: map[int64]itemplacement.SlotPolicy{3167: {ListType: 0, Start: 121, End: 176, AllowQuickSlots: true}}, SourceSealed: func(stack repository.ItemStack) bool { return stack.Extra["seal_flag"] == "1" }, Random: func(int64) int64 { return 0 }, BuildStack: func(slot int16, d Definition, source repository.ItemStack) (repository.ItemStack, error) {
		return repository.ItemStack{ItemID: d.ItemID, Count: 1, Bind: source.Bind, Extra: map[string]string{"item_kind": "equipment", "source": "合成新实例"}}, nil
	}}
	return o, group, cmd
}

func TestEquipmentCompound90ProbabilityBoundariesAndAtomicConsumption(t *testing.T) {
	for _, tc := range []struct {
		id            int64
		roll          int64
		level         int
		soulSlot      string
		soul, crystal int64
	}{
		{1001, 89999, 85, "0:363", 9, 150}, {1001, 90000, 90, "0:363", 9, 150},
		{1101, 90000, 90, "0:363", 9, 150}, {2001, 94999, 85, "0:364", 6, 300}, {2001, 95000, 90, "0:364", 6, 300},
		{2002, 99999, 90, "0:364", 8, 400},
	} {
		t.Run(fmt.Sprintf("%d_%d", tc.id, tc.roll), func(t *testing.T) {
			o, g, cmd := testOwner(t, tc.id)
			calls := 0
			cmd.Random = func(bound int64) int64 {
				calls++
				if calls == 1 {
					if bound != 100000 {
						t.Fatal("等级概率被候选数加权")
					}
					return tc.roll
				}
				return 0
			}
			result, err := o.Compound(context.Background(), cmd)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 2 || result.Output.MinimumLevel != tc.level || result.Output.Rarity != result.Source.Rarity {
				t.Fatalf("结果%+v，随机次数%d", result.Output, calls)
			}
			inv, _, _ := g.Inventory.Load(context.Background(), "2")
			acc, _, _ := g.AccountInventory.Load(context.Background(), "account-1")
			character, _, _ := g.Character.Load(context.Background(), "2")
			if inv.Slots["0:121"].Count != 1000-tc.crystal || acc.Slots[tc.soulSlot].Count != 100-tc.soul || character.Stats["gold"] != 1234 {
				t.Fatal("材料数量或钱包被错误修改")
			}
			if inv.Slots["0:9"].Extra["old_option"] != "" {
				t.Fatal("把来源实例的旧附魔等属性复制到产物")
			}
		})
	}
}

func TestEquipmentCompound90IndividualWeightBoundaries(t *testing.T) {
	for _, tc := range []struct{ roll, want int64 }{{249999, 1001}, {250000, 1005}, {1249999, 1005}, {1250000, 1101}, {2249999, 1101}} {
		t.Run(fmt.Sprint(tc.roll), func(t *testing.T) {
			o, _, cmd := testOwner(t, 1001)
			calls := 0
			cmd.Random = func(bound int64) int64 {
				calls++
				if calls == 1 {
					return 0
				}
				if bound != 2250000 {
					t.Fatalf("物品总权重%d", bound)
				}
				return tc.roll
			}
			result, err := o.Compound(context.Background(), cmd)
			if err != nil {
				t.Fatal(err)
			}
			if result.Output.ItemID != tc.want {
				t.Fatalf("抽中%d，预期%d", result.Output.ItemID, tc.want)
			}
		})
	}
}

func TestEquipmentCompound90InactiveDeadlineAndCancelledCatalog(t *testing.T) {
	c := testCatalog(t)
	c.source.(testSource)["equipment/9004.equ"] = strings.Replace(c.source.(testSource)["equipment/9004.equ"], "[usable period]\n1\n", "[usable period]\n0\n[daily delete item]\n0\n", 1)
	for _, entry := range c.refs {
		if entry.ID == 9004 {
			definition, err := c.readEquipment(entry)
			if err != nil || definition.Temporary || !c.Rules.eligible(definition) {
				t.Fatalf("永久装备的0期限标记被误拒：%+v %v", definition, err)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Preload(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("取消请求仍使用了目录")
	}
}

func TestEquipmentCompound90RejectedOperationsPreserveAssets(t *testing.T) {
	for _, name := range []string{"账号不符", "锁定来源", "来源过期", "未开放来源格", "错误材料页", "灵魂角色副本", "材料不足", "锁定灵魂", "随机越界", "创建失败", "旧随机模式", "错误部位"} {
		t.Run(name, func(t *testing.T) {
			o, g, cmd := testOwner(t, 2001)
			ctx := context.Background()
			inv, _, _ := g.Inventory.Load(ctx, "2")
			acc, _, _ := g.AccountInventory.Load(ctx, "account-1")
			switch name {
			case "账号不符":
				cmd.AccountID = "other"
			case "锁定来源":
				s := inv.Slots["0:9"]
				s.Extra["equipment_lock_state"] = "locked"
				inv.Slots["0:9"] = s
			case "来源过期":
				s := inv.Slots["0:9"]
				s.ExpireAt = time.Now().Add(-time.Second)
				inv.Slots["0:9"] = s
			case "未开放来源格":
				inv.Slots["0:41"] = inv.Slots["0:9"]
				delete(inv.Slots, "0:9")
				cmd.Request.SourceSlot = 41
			case "错误材料页":
				inv.Slots["0:70"] = inv.Slots["0:121"]
				delete(inv.Slots, "0:121")
			case "灵魂角色副本":
				inv.Slots["0:364"] = acc.Slots["0:364"]
				delete(acc.Slots, "0:364")
			case "材料不足":
				s := inv.Slots["0:121"]
				s.Count = 299
				inv.Slots["0:121"] = s
			case "锁定灵魂":
				s := acc.Slots["0:364"]
				s.Extra = map[string]string{"equipment_lock_state": "locked"}
				acc.Slots["0:364"] = s
			case "随机越界":
				cmd.Random = func(bound int64) int64 { return bound }
			case "创建失败":
				cmd.BuildStack = func(int16, Definition, repository.ItemStack) (repository.ItemStack, error) {
					return repository.ItemStack{}, fmt.Errorf("模拟创建失败")
				}
			case "旧随机模式":
				cmd.Request.Mode = 1
			case "错误部位":
				cmd.Request.Part = 3
			}
			g.Inventory.Save(ctx, inv)
			g.AccountInventory.Save(ctx, acc)
			beforeInv, _, _ := g.Inventory.Load(ctx, "2")
			beforeAcc, _, _ := g.AccountInventory.Load(ctx, "account-1")
			if _, err := o.Compound(ctx, cmd); err == nil {
				t.Fatal("应当拒绝")
			}
			afterInv, _, _ := g.Inventory.Load(ctx, "2")
			afterAcc, _, _ := g.AccountInventory.Load(ctx, "account-1")
			if !reflect.DeepEqual(beforeInv, afterInv) || !reflect.DeepEqual(beforeAcc, afterAcc) {
				t.Fatal("失败后来源或材料变化")
			}
		})
	}
}

func TestEquipmentCompound90FullBagReusesLegalSourceSlot(t *testing.T) {
	o, g, cmd := testOwner(t, 1001)
	ctx := context.Background()
	inv, _, _ := g.Inventory.Load(ctx, "2")
	for slot := int16(3); slot <= 64; slot++ {
		if slot == 9 {
			continue
		}
		inv.Slots[slotKey(slot)] = repository.ItemStack{ItemID: 8000 + int64(slot), Count: 1}
	}
	g.Inventory.Save(ctx, inv)
	result, err := o.Compound(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if result.ResultSlot != 9 {
		t.Fatal("满包没有复用被消耗的合法来源格")
	}
	if result.Inventory.Slots["0:41"].ItemID != 8041 {
		t.Fatal("修改未开放的历史行")
	}
}

type faultAssets struct {
	repository.AccountCharacterAssetUnitOfWork
	account bool
}
type faultInventory struct{ repository.InventoryRepository }

func (f faultInventory) Save(context.Context, repository.InventoryRecord) error {
	return fmt.Errorf("模拟库存保存失败")
}

type faultAccount struct {
	repository.AccountInventoryRepository
}

func (f faultAccount) Save(context.Context, repository.AccountInventoryRecord) error {
	return fmt.Errorf("模拟账号保存失败")
}
func (f faultAssets) WithinAccountCharacterAssets(ctx context.Context, a, c string, apply func(repository.AccountInventoryRepository, repository.CharacterRepository, repository.InventoryRepository, repository.EquipmentRepository) error) error {
	return f.AccountCharacterAssetUnitOfWork.WithinAccountCharacterAssets(ctx, a, c, func(acc repository.AccountInventoryRepository, ch repository.CharacterRepository, inv repository.InventoryRepository, eq repository.EquipmentRepository) error {
		if f.account {
			return apply(faultAccount{acc}, ch, inv, eq)
		}
		return apply(acc, ch, faultInventory{inv}, eq)
	})
}

func TestEquipmentCompound90SaveFailuresRollbackBothOwners(t *testing.T) {
	for _, accountFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(accountFailure), func(t *testing.T) {
			o, g, cmd := testOwner(t, 2001)
			o.assets = faultAssets{g.AccountAssets, accountFailure}
			ctx := context.Background()
			beforeInv, _, _ := g.Inventory.Load(ctx, "2")
			beforeAcc, _, _ := g.AccountInventory.Load(ctx, "account-1")
			if _, err := o.Compound(ctx, cmd); err == nil {
				t.Fatal("保存失败被当成成功")
			}
			afterInv, _, _ := g.Inventory.Load(ctx, "2")
			afterAcc, _, _ := g.AccountInventory.Load(ctx, "account-1")
			if !reflect.DeepEqual(beforeInv, afterInv) || !reflect.DeepEqual(beforeAcc, afterAcc) {
				t.Fatal("两种资产没有整体回滚")
			}
		})
	}
}

func TestEquipmentCompound90ConcurrentMaterialsNotDoubleSpent(t *testing.T) {
	o, g, cmd := testOwner(t, 1001)
	ctx := context.Background()
	inv, _, _ := g.Inventory.Load(ctx, "2")
	inv.Slots["0:121"] = repository.ItemStack{ItemID: 3167, Count: 150}
	g.Inventory.Save(ctx, inv)
	acc, _, _ := g.AccountInventory.Load(ctx, "account-1")
	acc.Slots["0:363"] = repository.ItemStack{ItemID: 10099774, Count: 9}
	g.AccountInventory.Save(ctx, acc)
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() { defer wait.Done(); _, err := o.Compound(context.Background(), cmd); results <- err }()
	}
	wait.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("提交次数%d", success)
	}
	acc, _, _ = g.AccountInventory.Load(context.Background(), "account-1")
	if _, exists := acc.Slots["0:363"]; exists {
		t.Fatal("一次材料预算没有准确消耗")
	}
}
