package equipmentcompound

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/repository"
)

func TestEquipmentCompound90MissingSamePoolRegeneratesSourceWithPVFWeights(t *testing.T) {
	for _, family := range []struct {
		id                  int64
		rarity              int
		same, soul, crystal int64
	}{
		{1001, 3, 90000, 9, 150},
		{1101, 3, 90000, 9, 150},
		{2001, 6, 95000, 6, 300},
	} {
		for _, same := range []int64{family.same, 37} {
			plus := int64(100000) - same
			if same == 37 {
				plus = 63
			}
			for _, roll := range []int64{0, same - 1, same, same + plus - 1} {
				t.Run(fmt.Sprintf("%d_%d_%d_%d", family.id, same, plus, roll), func(t *testing.T) {
					archive := testCatalog(t).source.(testSource)
					original := fmt.Sprintf("%d 85 %d %d", family.rarity, family.same, 100000-family.same)
					configured := fmt.Sprintf("%d 85 %d %d", family.rarity, same, plus)
					archive[LevelsPath] = strings.Replace(testLevels, original, configured, 1)
					catalog, err := NewCatalog(archive)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = catalog.Preload(context.Background()); err != nil {
						t.Fatal(err)
					}
					source, err := catalog.Resolve(family.id)
					if err != nil {
						t.Fatal(err)
					}
					delete(catalog.buckets, bucketKey{family.rarity, 85, 1})
					pools, err := catalog.Pools(source, 1)
					if err != nil || len(pools) != 2 || pools[0].Weight != same || pools[1].Weight != plus || len(pools[0].Candidates) != 1 || pools[0].Candidates[0].Definition != source {
						t.Fatalf("缺同级池没有用合法来源模板保留PVF等级权重：%+v %v", pools, err)
					}
					sourceWeight := WeightUnit
					if weight, ok := catalog.Rules.Weights[family.id]; ok {
						sourceWeight = weight
					}
					if pools[0].Candidates[0].Weight != sourceWeight {
						t.Fatal("来源模板的PVF特殊权重丢失")
					}
					selected := pools[0]
					if roll >= same {
						selected = pools[1]
					}
					var itemTotal int64
					for _, candidate := range selected.Candidates {
						itemTotal += candidate.Weight
					}
					owner, group, cmd := testOwner(t, family.id)
					cmd.Catalog = catalog
					calls, builds := 0, 0
					cmd.Random = func(bound int64) int64 {
						calls++
						if calls == 1 {
							if bound != same+plus {
								t.Fatal("缺同级池改变了PVF总权重，可能变成必升级")
							}
							return roll
						}
						if calls != 2 || bound != itemTotal {
							t.Fatal("缺同级池的单件抽取范围或次数错误")
						}
						return 0
					}
					build := cmd.BuildStack
					cmd.BuildStack = func(slot int16, definition Definition, original repository.ItemStack) (repository.ItemStack, error) {
						builds++
						return build(slot, definition, original)
					}
					result, err := owner.Compound(context.Background(), cmd)
					if err != nil || calls != 2 || builds != 1 || result.Output != selected.Candidates[0].Definition || result.LevelWeight != selected.Weight || result.TotalLevelWeight != same+plus {
						t.Fatalf("兜底结果没有按原概率生成新模板：%+v %v，抽样%d 构建%d", result, err, calls, builds)
					}
					inventory, _, _ := group.Inventory.Load(context.Background(), "2")
					account, _, _ := group.AccountInventory.Load(context.Background(), "account-1")
					character, _, _ := group.Character.Load(context.Background(), "2")
					soulSlot := "0:363"
					if family.rarity == 6 {
						soulSlot = "0:364"
					}
					stack := inventory.Slots["0:9"]
					if stack.ItemID != result.Output.ItemID || stack.Extra["old_option"] != "" || stack.Extra["source"] != "合成新实例" || inventory.Slots["0:121"].Count != 1000-family.crystal || account.Slots[soulSlot].Count != 100-family.soul || character.Stats["gold"] != 1234 {
						t.Fatal("同ID兜底没有重新生成、残留旧属性或没有正常扣材料")
					}
					if _, found := catalog.buckets[bucketKey{family.rarity, 85, 1}]; found {
						t.Fatal("兜底意外改写启动目录")
					}
				})
			}
		}
	}
}

func TestEquipmentCompound90MissingSamePoolDoesNotBypassCachedSource(t *testing.T) {
	for _, change := range []string{"缺来源", "改来源", "排除来源"} {
		t.Run(change, func(t *testing.T) {
			catalog := testCatalog(t)
			source, _ := catalog.Resolve(1001)
			delete(catalog.buckets, bucketKey{3, 85, 1})
			want := ErrCandidates
			switch change {
			case "缺来源":
				delete(catalog.definitions, source.ItemID)
			case "改来源":
				source.Name = "不是启动读取的模板"
			case "排除来源":
				catalog.Rules.Excluded[source.ItemID] = true
				want = ErrSource
			}
			if _, err := catalog.Pools(source, 1); !errors.Is(err, want) {
				t.Fatalf("兜底绕过真实缓存来源或资格：%v", err)
			}
		})
	}
}

func TestEquipmentCompound90MissingBothPoolsRegeneratesKnownSource(t *testing.T) {
	for _, id := range []int64{1001, 1101, 2001} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			catalog := testCatalog(t)
			source, _ := catalog.Resolve(id)
			delete(catalog.buckets, bucketKey{source.Rarity, 85, 1})
			delete(catalog.buckets, bucketKey{source.Rarity, 90, 1})
			pools, err := catalog.Pools(source, 1)
			if err != nil || len(pools) != 1 || len(pools[0].Candidates) != 1 || pools[0].MinimumLevel != 85 || pools[0].Candidates[0].Definition != source {
				t.Fatalf("双池缺失时没有按缓存合法来源同级生成：%+v %v", pools, err)
			}
		})
	}
}

func TestEquipmentCompound90ZeroSameWeightDoesNotAddSourceFallback(t *testing.T) {
	catalog := testCatalog(t)
	source, _ := catalog.Resolve(1001)
	catalog.Rules.Levels[3][85] = LevelWeights{Same: 0, PlusFive: 19}
	delete(catalog.buckets, bucketKey{3, 85, 1})
	pools, err := catalog.Pools(source, 1)
	if err != nil || len(pools) != 1 || pools[0].MinimumLevel != 90 || pools[0].Weight != 19 {
		t.Fatalf("兜底错误覆盖PVF显式零同级权重：%+v %v", pools, err)
	}
}

func TestEquipmentCompound90BroadCategoriesAndSharedUniquePool(t *testing.T) {
	for _, tc := range []struct {
		name, from, to       string
		part                 uint16
		rarity               int
		fromLegacy, toLegacy bool
	}{
		{"武器跨职业", "katana", "knuckle", 1, 6, false, false},
		{"拳套到光剑", "knuckle", "beamswd", 1, 6, false, false},
		{"布甲上衣到重甲鞋", "cl coat", "ha shoes", 2, 3, false, false},
		{"项链到手镯", "amulet", "wrist", 3, 6, false, false},
		{"耳环到魔法石", "earring", "magic stone", 4, 6, false, false},
		{"普通神器到圣物", "katana", "knuckle", 1, 3, false, true},
		{"圣物到普通神器", "cl coat", "ha shoes", 2, 3, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := strings.ReplaceAll(testRules, "`katana` `knuckle`", "`katana` `knuckle` `beamswd`")
			rules = strings.ReplaceAll(rules, "`cl coat`", "`cl coat` `ha shoes`")
			rules = strings.ReplaceAll(rules, "`amulet`", "`amulet` `wrist`")
			rules = strings.ReplaceAll(rules, "`earring`", "`earring` `magic stone`")
			source := testSource{RulesPath: rules, LevelsPath: testLevels, "equipment/equipment.lst": "5000 `5000.equ`\n5001 `5001.equ`\n"}
			for i, id := range []int{5000, 5001} {
				group, legacy := tc.from, tc.fromLegacy
				if i == 1 {
					group, legacy = tc.to, tc.toLegacy
				}
				category := ""
				if legacy {
					category = "[item category]\n`legacy`\n"
				}
				source[fmt.Sprintf("equipment/%d.equ", id)] = fmt.Sprintf("[name]\n`合法装备%d`\n[rarity]\n%d\n[minimum level]\n85\n[attach type]\n`[sealing]`\n[creation rate]\n100\n[item group name]\n`%s`\n%s", id, tc.rarity, group, category)
			}
			catalog, err := NewCatalog(source)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = catalog.Preload(context.Background()); err != nil {
				t.Fatal(err)
			}
			owner, group, cmd := testOwner(t, 5000)
			cmd.Catalog, cmd.Request.Part = catalog, tc.part
			calls := 0
			cmd.Random = func(int64) int64 {
				calls++
				if calls == 1 {
					return 0
				}
				return WeightUnit
			}
			result, err := owner.Compound(context.Background(), cmd)
			if err != nil || result.Output.ItemID != 5001 || result.Output.Group != tc.to || result.Output.Legacy() != tc.toLegacy || result.Output.Rarity != tc.rarity || calls != 2 {
				t.Fatalf("90大类随机或品质3共池错误：%+v %v", result, err)
			}
			inventory, _, _ := group.Inventory.Load(context.Background(), "2")
			if inventory.Slots["0:9"].ItemID != 5001 {
				t.Fatal("跨细类产物未保存")
			}
		})
	}
}

func TestEquipmentCompound90DropMagicAndObsoleteMetadata(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rarity  int
		extra   string
		allowed bool
	}{
		{"普通神器正掉落", 3, "[creation rate]\n100\n", true},
		{"普通神器零掉落", 3, "[creation rate]\n0\n", false},
		{"独立掉落传说", 6, "[creation rate]\n0\n", true},
		{"(旧) 传说", 6, "[creation rate]\n0\n", true},
		{"古旧之刃", 6, "[creation rate]\n0\n", true},
		{"魔法封印随机选项", 3, "[creation rate]\n100\n[random option]\n1\n", false},
		{"普通神器转化规则不等于假粉", 3, "[creation rate]\n500\n[Force Result Item Rule]\n1 0\n", true},
		{"普通神器零生成率与转化规则", 3, "[creation rate]\n0\n[Force Result Item Rule]\n1 0\n", false},
		{"魔法封印正生成率与转化规则", 3, "[creation rate]\n500\n[random option]\n1\n[Force Result Item Rule]\n1 0\n", false},
		{"圣物正掉落", 3, "[creation rate]\n125\n[item category]\n`legacy`\n", true},
		{"圣物明确零掉落", 3, "[creation rate]\n0\n[item category]\n`legacy`\n", false},
		{"圣物缺掉落字段", 3, "[item category]\n`legacy`\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testCatalog(t)
			source := c.source.(testSource)
			source["equipment/5000.equ"] = fmt.Sprintf("[name]\n`%s`\n[rarity]\n%d\n[minimum level]\n85\n[attach type]\n`[sealing]`\n[item group name]\n`katana`\n%s", tc.name, tc.rarity, tc.extra)
			d, err := c.readEquipment(pvf.ListEntry{ID: 5000, Path: "5000.equ"})
			if err != nil || c.Rules.eligible(d) != tc.allowed {
				t.Fatalf("资格判断错误：%+v %v", d, err)
			}
		})
	}
}

func TestEquipmentCompound90UnsealedSourceNeverConsumes(t *testing.T) {
	for _, missingCheck := range []bool{false, true} {
		t.Run(fmt.Sprint(missingCheck), func(t *testing.T) {
			o, g, cmd := testOwner(t, 2001)
			ctx := context.Background()
			inventory, _, _ := g.Inventory.Load(ctx, "2")
			stack := inventory.Slots["0:9"]
			stack.Extra["seal_flag"] = "0"
			inventory.Slots["0:9"] = stack
			if err := g.Inventory.Save(ctx, inventory); err != nil {
				t.Fatal(err)
			}
			if missingCheck {
				cmd.SourceSealed = nil
			}
			called := false
			cmd.Random = func(int64) int64 { called = true; return 0 }
			before, _, _ := g.Inventory.Load(ctx, "2")
			account, _, _ := g.AccountInventory.Load(ctx, "account-1")
			if _, err := o.Compound(ctx, cmd); err == nil {
				t.Fatal("解封来源或缺封装核对仍被放行")
			}
			after, _, _ := g.Inventory.Load(ctx, "2")
			accountAfter, _, _ := g.AccountInventory.Load(ctx, "account-1")
			if called || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(account, accountAfter) {
				t.Fatal("拒绝解封来源仍抽样或扣资产")
			}
		})
	}
}

// 改变PVF配置后重新预载，用事务实际抽样确认等级与单件权重均跟随配置。
// 测试值有意不同于当前正式PVF，避免只验证某份表的固定数值。
func TestEquipmentCompound90PVFChangesDriveSharedPoolProbabilities(t *testing.T) {
	for _, tc := range []struct {
		name, itemWeight, holyWeight string
		same, up, item, holy         int64
	}{
		{"配置甲", "0.375", "2.5", 37, 63, 375000, 2500000},
		{"配置乙", "1.75", "0.125", 61, 39, 1750000, 125000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pvfSource := testCatalog(t).source.(testSource)
			pvfSource[LevelsPath] = strings.Replace(pvfSource[LevelsPath], "3 85 90000 10000", fmt.Sprintf("3 85 %d %d", tc.same, tc.up), 1)
			pvfSource[RulesPath] = strings.Replace(pvfSource[RulesPath], "1001 0.25 2001 0.50", "1001 "+tc.itemWeight+" 1101 "+tc.holyWeight+" 2001 0.50", 1)
			catalog, err := NewCatalog(pvfSource)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = catalog.Preload(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, sourceID := range []int64{1001, 1101} {
				for _, levelCase := range []struct {
					roll  int64
					level int
				}{{tc.same - 1, 85}, {tc.same, 90}} {
					owner, _, cmd := testOwner(t, sourceID)
					cmd.Catalog = catalog
					calls := 0
					cmd.Random = func(bound int64) int64 {
						calls++
						if calls == 1 {
							if bound != tc.same+tc.up {
								t.Fatalf("等级总权重未随PVF变化：%d", bound)
							}
							return levelCase.roll
						}
						return 0
					}
					result, err := owner.Compound(context.Background(), cmd)
					if err != nil || result.Output.MinimumLevel != levelCase.level || calls != 2 {
						t.Fatalf("等级实际抽样未随PVF变化，来源%d：%+v %v", sourceID, result, err)
					}
				}
				for _, itemCase := range []struct{ roll, itemID int64 }{
					{tc.item - 1, 1001},
					{tc.item, 1005},
					{tc.item + WeightUnit, 1101},
					{tc.item + WeightUnit + tc.holy - 1, 1101},
				} {
					owner, _, cmd := testOwner(t, sourceID)
					cmd.Catalog = catalog
					calls := 0
					cmd.Random = func(bound int64) int64 {
						calls++
						if calls == 1 {
							return 0
						}
						if bound != tc.item+WeightUnit+tc.holy {
							t.Fatalf("同级物品总权重未随PVF变化：%d", bound)
						}
						return itemCase.roll
					}
					result, err := owner.Compound(context.Background(), cmd)
					if err != nil || result.Output.ItemID != itemCase.itemID || calls != 2 {
						t.Fatalf("品质3共池特殊权重实际抽样未随PVF变化，来源%d：%+v %v", sourceID, result, err)
					}
				}
			}
		})
	}
}

func TestEquipmentCompound90PVFExclusionAndLevelCapReload(t *testing.T) {
	pvfSource := testCatalog(t).source.(testSource)
	pvfSource[RulesPath] = strings.Replace(pvfSource[RulesPath], "9001\n", "9001 2001\n", 1)
	pvfSource[LevelsPath] = strings.Replace(pvfSource[LevelsPath], "3 90 100000 0", "3 90 12 88 3 95 100000 0", 1)
	pvfSource["equipment/equipment.lst"] += "5000 `5000.equ`\n"
	pvfSource["equipment/5000.equ"] = "[name]\n`PVF新增等级神器`\n[rarity]\n3\n[minimum level]\n95\n[creation rate]\n1\n[attach type]\n`sealing`\n[item group name]\n`katana`\n"
	catalog, err := NewCatalog(pvfSource)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.Preload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.Resolve(2001); err == nil {
		t.Fatal("PVF新增排除ID未作用于来源")
	}
	for _, d := range catalog.Definitions() {
		if d.ItemID == 2001 {
			t.Fatal("PVF新增排除ID仍在产出目录")
		}
	}
	source, err := catalog.Resolve(1002)
	if err != nil {
		t.Fatal(err)
	}
	pools, err := catalog.Pools(source, 1)
	if err != nil || len(pools) != 2 || pools[1].MinimumLevel != 95 || pools[1].Weight != 88 || pools[1].Candidates[0].Definition.ItemID != 5000 {
		t.Fatalf("PVF新等级上限或升级权重被固定90覆盖：%+v %v", pools, err)
	}
}
