package chronicle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type testSource map[string]string

func (s testSource) ReadText(name string) (string, error) {
	text, ok := s[name]
	if !ok {
		return "", fmt.Errorf("测试缺规则%s", name)
	}
	return text, nil
}
func testCatalog() (*Catalog, testSource) {
	source := testSource{"stackable/red.stk": "[type] 0\n[need material] 3311 10\n[probability] 100 11\n[import script] `rules.stk`\n", "stackable/holy.stk": "[type] 0\n[need material] 3311 40\n[probability] 100 100\n[import script] `rules.stk`\n", "stackable/rules.stk": "[3choro enchant]\n[check]\n14 4 `coat`\n[skill]\n8 `[at priest]` 101\n[/skill]\n[skill]\n9 `[at priest]` 102\n[/skill]\n[/check]\n[check]\n0 1 `weapon`\n[skill]\n8 `[swordman]` 46\n[/skill]\n[/check]\n", "equipment/target.equ": "[rarity] 5\n[durability] 40\n[equipment type] `[coat]` 20\n[usable job] `[at priest]`\n[/usable job]\n[character item check] 14 4 `coat`\n[/character item check]\n"}
	return NewCatalog(source, map[int64]Reference{1254: {Path: "stackable/red.stk"}, 10090758: {Path: "stackable/holy.stk"}, 100080578: {Path: "equipment/target.equ", Equipment: true}}), source
}
func fixture(t *testing.T) (repository.Group, *Owner, Command) {
	t.Helper()
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	if err := g.Character.Save(ctx, repository.CharacterRecord{CharacterID: "2", AccountID: "a", Job: "14", Stats: map[string]int64{"grow_type": 36}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Inventory.Save(ctx, repository.InventoryRecord{CharacterID: "2", Slots: map[string]repository.ItemStack{"0:9": {ItemID: 100080578, Count: 1, Extra: map[string]string{"durability": "40"}}, "0:65": {ItemID: 1254, Count: 3}}}); err != nil {
		t.Fatal(err)
	}
	catalog, _ := testCatalog()
	o, _ := NewOwner(g)
	c := Command{AccountID: "a", CharacterID: "2", Catalog: catalog, Request: Request{MaterialSlot: 65, MaterialID: 1254, TargetSlot: 9, TargetID: 100080578, Option: 8}, MaterialPolicy: itemplacement.SlotPolicy{ListType: 0, Start: 65, End: 120, AllowQuickSlots: true}, Identity: func(r repository.CharacterRecord) (byte, byte, error) {
		if r.Job != "14" {
			return 0, 1, nil
		}
		return 14, byte(r.Stats["grow_type"]) & 15, nil
	}, State: func(s repository.ItemStack) (State, error) {
		st := State{Durability: 40}
		if s.Extra["durability"] == "39" {
			st.Durability = 39
		}
		st.Locked = s.Extra["locked"] == "1"
		st.Unidentified = s.Extra["unidentified"] == "1"
		if text := s.Extra["options"]; text != "" {
			if err := json.Unmarshal([]byte(text), &st.Options); err != nil {
				return st, err
			}
		}
		return st, nil
	}, SetOptions: func(s repository.ItemStack, options []Option) (repository.ItemStack, error) {
		if s.Extra == nil {
			s.Extra = map[string]string{}
		}
		raw, _ := json.Marshal(options)
		s.Extra["options"] = string(raw)
		return s, nil
	}, Encode: func(_ int16, s repository.ItemStack) repository.ItemStack { return s }, Random: func(int) int { return 100 }, FailureRewards: func(_ Equipment, _ State, _ Material) ([]Reward, error) {
		return []Reward{{ItemID: 3311, Count: 1}}, nil
	}, GrantRewards: func(inv *repository.InventoryRecord, acc *repository.AccountInventoryRecord, rewards []Reward) ([]RewardSlot, bool, error) {
		acc.Slots["0:354"] = repository.ItemStack{ItemID: 3311, Count: 1}
		return []RewardSlot{{Slot: 354, ItemID: 3311, Count: 1}}, true, nil
	}}
	return g, o, c
}
func TestCatalogImports90RulesAndKeepsMaterialProbability(t *testing.T) {
	if !JobMatches([]string{"[gun blader]"}, 15) || !JobMatches([]string{"[demonic lancer]"}, 13) || !JobMatches([]string{"[at priest]"}, 14) {
		t.Fatal("90新增职业标签不匹配")
	}
	c, _ := testCatalog()
	e, err := c.Equipment(100080578)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id   int64
		prob int
	}{{1254, 11}, {10090758, 100}} {
		m, err := c.Material(tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if m.Probabilities[1] != tc.prob || !m.Allows(14, 4, e, 8) || m.Allows(0, 1, e, 8) || m.Allows(14, 3, e, 8) || m.Allows(14, 4, e, 31) {
			t.Fatalf("90导入规则未限定职业/转职/选项：%+v", m)
		}
	}
}
func TestRefineSuccessPersistsTwoIndependentOptions(t *testing.T) {
	g, o, c := fixture(t)
	first, err := o.Refine(context.Background(), c)
	if err != nil || !first.Succeeded || first.MaterialRemaining != 2 {
		t.Fatalf("首次精炼%+v %v", first, err)
	}
	c.Request.Option = 9
	c.Random = func(int) int { return 11 }
	second, err := o.Refine(context.Background(), c)
	if err != nil || !second.Succeeded || second.Options != 2 || second.MaterialRemaining != 1 {
		t.Fatalf("二次阈值%+v %v", second, err)
	}
	stored, _, _ := g.Inventory.Load(context.Background(), "2")
	state, err := c.State(stored.Slots["0:9"])
	if err != nil || len(state.Options) != 2 || state.Options[0].AuraID != 1254 || state.Options[0].Job != 14 || state.Options[0].EquipmentType != 14 || state.Options[0].Number != 8 || state.Options[1].Number != 9 {
		t.Fatalf("冷读丢属性%+v %v", state, err)
	}
	before := repository.CloneInventory(stored)
	if _, err = o.Refine(context.Background(), c); !errors.Is(err, ErrTarget) {
		t.Fatal("第三次未拒绝", err)
	}
	after, _, _ := g.Inventory.Load(context.Background(), "2")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("第三次精炼发生消耗")
	}
}
func TestRefineFailureDestroysEquipmentAndCommitsRewards(t *testing.T) {
	g, o, c := fixture(t)
	if _, err := o.Refine(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	c.Request.Option = 9
	c.Random = func(int) int { return 12 }
	r, err := o.Refine(context.Background(), c)
	if err != nil || r.Succeeded || len(r.Rewards) != 1 {
		t.Fatalf("失败精炼%+v %v", r, err)
	}
	inv, _, _ := g.Inventory.Load(context.Background(), "2")
	acc, _, _ := g.AccountInventory.Load(context.Background(), "a")
	if _, ok := inv.Slots["0:9"]; ok || inv.Slots["0:65"].Count != 1 || acc.Slots["0:354"].Count != 1 {
		t.Fatal("碎装/扣气息/返材不一致")
	}
}
func TestRefineGuardsRejectWithoutRandomOrWrites(t *testing.T) {
	for _, name := range []string{"重复属性", "锁定", "红字", "耐久", "职业", "转职", "模板", "锁格", "材料耗尽", "选项"} {
		t.Run(name, func(t *testing.T) {
			g, o, c := fixture(t)
			inv, _, _ := g.Inventory.Load(context.Background(), "2")
			target := inv.Slots["0:9"]
			switch name {
			case "重复属性":
				target, _ = c.SetOptions(target, []Option{{AuraID: 1254, Job: 14, Grow: 4, EquipmentType: 14, Number: 8}})
			case "锁定":
				target.Extra["locked"] = "1"
			case "红字":
				target.Extra["unidentified"] = "1"
			case "耐久":
				target.Extra["durability"] = "39"
			case "职业", "转职":
				ch, _, _ := g.Character.Load(context.Background(), "2")
				if name == "职业" {
					ch.Job = "0"
				} else {
					ch.Stats["grow_type"] = 35
				}
				g.Character.Save(context.Background(), ch)
			case "模板":
				c.Request.TargetID++
			case "锁格":
				inv.Slots["0:50"] = target
				delete(inv.Slots, "0:9")
				c.Request.TargetSlot = 50
			case "材料耗尽":
				delete(inv.Slots, "0:65")
			case "选项":
				c.Request.Option = 31
			}
			if name != "锁格" {
				inv.Slots["0:9"] = target
			}
			g.Inventory.Save(context.Background(), inv)
			before, _, _ := g.Inventory.Load(context.Background(), "2")
			c.Random = func(int) int { t.Fatal("拒绝前使用随机数"); return 0 }
			if _, err := o.Refine(context.Background(), c); err == nil {
				t.Fatal("非法精炼未拒绝")
			}
			after, _, _ := g.Inventory.Load(context.Background(), "2")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("拒绝后背包变化")
			}
		})
	}
}

type failingAccount struct {
	repository.AccountInventoryRepository
}

func (f failingAccount) Save(context.Context, repository.AccountInventoryRecord) error {
	return errors.New("测试账号保存失败")
}

type failingAssets struct {
	repository.AccountCharacterAssetUnitOfWork
}

func (f failingAssets) WithinAccountCharacterAssets(ctx context.Context, a, c string, fn func(repository.AccountInventoryRepository, repository.CharacterRepository, repository.InventoryRepository, repository.EquipmentRepository) error) error {
	return f.AccountCharacterAssetUnitOfWork.WithinAccountCharacterAssets(ctx, a, c, func(acc repository.AccountInventoryRepository, ch repository.CharacterRepository, inv repository.InventoryRepository, e repository.EquipmentRepository) error {
		return fn(failingAccount{acc}, ch, inv, e)
	})
}
func TestRefineRollbackAfterInventoryWriteWhenAccountSaveFails(t *testing.T) {
	g, o, c := fixture(t)
	if _, err := o.Refine(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	before, _, _ := g.Inventory.Load(context.Background(), "2")
	g.AccountAssets = failingAssets{g.AccountAssets}
	o, _ = NewOwner(g)
	c.Request.Option = 9
	c.Random = func(int) int { return 100 }
	if _, err := o.Refine(context.Background(), c); err == nil {
		t.Fatal("保存失败未传播")
	}
	after, _, _ := g.Inventory.Load(context.Background(), "2")
	acc, _, _ := g.AccountInventory.Load(context.Background(), "a")
	if !reflect.DeepEqual(before, after) || len(acc.Slots) != 0 {
		t.Fatal("保存失败后发生部分碎装/扣材")
	}
}

func TestRefineRewardSpaceFailureDoesNotDestroyOrConsume(t *testing.T) {
	g, o, c := fixture(t)
	if _, err := o.Refine(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	before, _, _ := g.Inventory.Load(context.Background(), "2")
	c.Request.Option = 9
	c.Random = func(int) int { return 100 }
	c.GrantRewards = func(inv *repository.InventoryRecord, acc *repository.AccountInventoryRecord, _ []Reward) ([]RewardSlot, bool, error) {
		inv.Slots["0:121"] = repository.ItemStack{ItemID: 3311, Count: 1}
		acc.Slots["0:354"] = repository.ItemStack{ItemID: 3311, Count: 1}
		return nil, false, errors.New("测试奖励空间不足")
	}
	if _, err := o.Refine(context.Background(), c); err == nil {
		t.Fatal("空间不足未拒绝")
	}
	after, _, _ := g.Inventory.Load(context.Background(), "2")
	acc, _, _ := g.AccountInventory.Load(context.Background(), "a")
	if !reflect.DeepEqual(before, after) || len(acc.Slots) != 0 {
		t.Fatal("空间不足时部分扣气息、碎装或返材")
	}
}
