package chronicle

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

func wornRefineFixture(t *testing.T) (repository.Group, *Owner, Command) {
	t.Helper()
	g, o, c := fixture(t)
	ctx := context.Background()
	inv, _, _ := g.Inventory.Load(ctx, "2")
	target := inv.Slots["0:9"]
	delete(inv.Slots, "0:9")
	inv.Slots["0:14"] = repository.ItemStack{ItemID: 999, Count: 1, Extra: map[string]string{"keep": "背包同号物品"}}
	if err := g.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if err := g.Equipment.Save(ctx, repository.EquipmentRecord{CharacterID: "2", Entries: map[string]repository.EquipmentEntry{
		"14": {SlotIndex: 14, ItemID: target.ItemID, Extra: target.Extra},
	}}); err != nil {
		t.Fatal(err)
	}
	c.Request.TargetList, c.Request.TargetSlot = 3, 14
	c.WornTarget = func(eq repository.EquipmentRecord, slot int16) (string, repository.ItemStack, error) {
		e, ok := eq.Entries["14"]
		if !ok || slot != 14 {
			return "", repository.ItemStack{}, ErrTarget
		}
		return "14", repository.ItemStack{ItemID: e.ItemID, Count: 1, Extra: e.Extra}, nil
	}
	c.WornUpdate = func(e repository.EquipmentEntry, s repository.ItemStack) repository.EquipmentEntry {
		e.Extra = s.Extra
		return e
	}
	return g, o, c
}

func TestWornRefineSuccessAndFailureUseEquipmentTransaction(t *testing.T) {
	g, o, c := wornRefineFixture(t)
	ctx := context.Background()
	first, err := o.Refine(ctx, c)
	if err != nil || !first.Succeeded || first.MaterialRemaining != 2 {
		t.Fatalf("穿戴首次精炼：%+v %v", first, err)
	}
	eq, _, _ := g.Equipment.Load(ctx, "2")
	encoded, _ := json.Marshal(eq)
	var cold repository.EquipmentRecord
	if err := json.Unmarshal(encoded, &cold); err != nil {
		t.Fatal(err)
	}
	if cold.Entries["14"].Extra["options"] == "" {
		t.Fatal("穿戴属性冷读取丢失")
	}
	c.Request.Option = 9
	second, err := o.Refine(ctx, c)
	if err != nil || second.Succeeded || second.MaterialRemaining != 1 || len(second.Rewards) != 1 {
		t.Fatalf("穿戴失败碎装：%+v %v", second, err)
	}
	eq, _, _ = g.Equipment.Load(ctx, "2")
	inv, _, _ := g.Inventory.Load(ctx, "2")
	acc, _, _ := g.AccountInventory.Load(ctx, "a")
	if _, ok := eq.Entries["14"]; ok {
		t.Fatal("精炼失败未销毁穿戴装备")
	}
	if inv.Slots["0:14"].ItemID != 999 || acc.Slots["0:354"].ItemID != 3311 {
		t.Fatal("误删同号背包或返材丢失")
	}
}

func TestWornRefineRejectionsAndLateSaveFailurePreserveAllAssets(t *testing.T) {
	for _, name := range []string{"保存失败", "锁定", "模板不符", "装备缺失", "部位不符", "返材失败"} {
		t.Run(name, func(t *testing.T) {
			g, o, c := wornRefineFixture(t)
			ctx := context.Background()
			switch name {
			case "保存失败":
				g.AccountAssets = wornFailAssets{AccountCharacterAssetUnitOfWork: g.AccountAssets}
				o, _ = NewOwner(g)
			case "锁定":
				eq, _, _ := g.Equipment.Load(ctx, "2")
				e := eq.Entries["14"]
				e.Extra["locked"] = "1"
				eq.Entries["14"] = e
				if err := g.Equipment.Save(ctx, eq); err != nil {
					t.Fatal(err)
				}
			case "模板不符":
				c.Request.TargetID++
			case "装备缺失":
				c.Request.TargetSlot = 15
			case "部位不符":
				c.Request.TargetSlot = 15
				old := c.WornTarget
				c.WornTarget = func(r repository.EquipmentRecord, _ int16) (string, repository.ItemStack, error) { return old(r, 14) }
			case "返材失败":
				if _, err := o.Refine(ctx, c); err != nil {
					t.Fatal(err)
				}
				c.Request.Option = 9
				c.GrantRewards = func(*repository.InventoryRecord, *repository.AccountInventoryRecord, []Reward) ([]RewardSlot, bool, error) {
					return nil, false, errors.New("返材写入失败")
				}
			}
			beforeInv, _, _ := g.Inventory.Load(ctx, "2")
			beforeEq, _, _ := g.Equipment.Load(ctx, "2")
			beforeAcc, _, _ := g.AccountInventory.Load(ctx, "a")
			if _, err := o.Refine(ctx, c); err == nil {
				t.Fatal("非法目标或存储故障被放行")
			}
			afterInv, _, _ := g.Inventory.Load(ctx, "2")
			afterEq, _, _ := g.Equipment.Load(ctx, "2")
			afterAcc, _, _ := g.AccountInventory.Load(ctx, "a")
			if !reflect.DeepEqual(beforeInv, afterInv) || !reflect.DeepEqual(beforeEq, afterEq) || !reflect.DeepEqual(beforeAcc, afterAcc) {
				t.Fatal("穿戴、材料或账号返材未整体回滚")
			}
		})
	}
}

type wornFailAssets struct {
	repository.AccountCharacterAssetUnitOfWork
}

func (w wornFailAssets) WithinAccountCharacterAssets(ctx context.Context, account, character string, apply func(repository.AccountInventoryRepository, repository.CharacterRepository, repository.InventoryRepository, repository.EquipmentRepository) error) error {
	return w.AccountCharacterAssetUnitOfWork.WithinAccountCharacterAssets(ctx, account, character, func(a repository.AccountInventoryRepository, c repository.CharacterRepository, i repository.InventoryRepository, e repository.EquipmentRepository) error {
		return apply(a, c, wornFailInventory{i}, e)
	})
}

type wornFailInventory struct{ repository.InventoryRepository }

func (w wornFailInventory) Save(context.Context, repository.InventoryRecord) error {
	return errors.New("装备保存后的背包写入失败")
}
