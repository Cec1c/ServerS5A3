package inventory

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func reviveCoinUseTestAssets(t *testing.T) (dnfrepo.Group, ReviveCoinUseCommand) {
	t.Helper()
	repos := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 90, Stats: map[string]int64{"exp": 123, "gold": 999}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{
		"0:68": {ItemID: 42, Count: 2, RawEntry: make([]byte, 0x77), Extra: map[string]string{"count": "2"}},
		"0:1":  {ItemID: 1, Count: 7},
	}}); err != nil {
		t.Fatal(err)
	}
	return repos, ReviveCoinUseCommand{AccountID: "account-1", CharacterID: 19, SlotIndex: 68,
		Definition: alignedcmd.StackableUseProvenanceResolution{ItemID: 42, Found: true, PVFPath: "stackable/cash/coin_general.stk", StackableType: "[waste]"},
		Now:        time.Date(2026, 10, 15, 13, 0, 0, 0, time.UTC)}
}

func TestUseReviveCoinItemRejectsInvalidSourcesWithoutMutation(t *testing.T) {
	for _, name := range []string{"账号不符", "缺少角色", "PVF路径不符", "PVF类型不符", "PVF未确认", "源格为空", "源格物品已变化", "源格未开放", "源物品过期", "元数据期限过期", "钱包格冲突", "钱包溢出", "缺少事务"} {
		t.Run(name, func(t *testing.T) {
			repos, cmd := reviveCoinUseTestAssets(t)
			ctx := context.Background()
			inventory, _, _ := repos.Inventory.Load(ctx, "19")
			switch name {
			case "账号不符":
				cmd.AccountID = "other-account"
			case "缺少角色":
				cmd.CharacterID = 20
			case "PVF路径不符":
				cmd.Definition.PVFPath = "stackable/fake.stk"
			case "PVF类型不符":
				cmd.Definition.StackableType = "[etc]"
			case "PVF未确认":
				cmd.Definition.Found = false
			case "源格为空":
				cmd.SlotIndex = 69
			case "源格物品已变化":
				inventory.Slots["0:68"] = dnfrepo.ItemStack{ItemID: 1031, Count: 2}
			case "源格未开放":
				cmd.SlotIndex = 115
				inventory.Slots["0:115"] = inventory.Slots["0:68"]
			case "源物品过期":
				stack := inventory.Slots["0:68"]
				stack.ExpireAt = cmd.Now
				inventory.Slots["0:68"] = stack
			case "元数据期限过期":
				inventory.Slots["0:68"].Extra["expire_unix"] = "1"
			case "钱包格冲突":
				inventory.Slots["0:1"] = dnfrepo.ItemStack{ItemID: 600, Count: 7}
			case "钱包溢出":
				inventory.Slots["0:1"] = dnfrepo.ItemStack{ItemID: 1, Count: math.MaxInt64}
			case "缺少事务":
				repos.CharacterAssets = nil
			}
			if err := repos.Inventory.Save(ctx, inventory); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Inventory.Load(ctx, "19")
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			result, err := owner.UseReviveCoinItem(ctx, cmd)
			if err == nil || result.Changed || result.ReviveCoinWalletUpdated {
				t.Fatalf("应拒绝且无已提交结果: result=%+v err=%v", result, err)
			}
			after, _, _ := repos.Inventory.Load(ctx, "19")
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("拒绝后库存发生变化: before=%+v after=%+v", before, after)
			}
		})
	}
}

var errReviveCoinTestSave = errors.New("复活币回归注入保存失败")

type reviveCoinUseFailUOW struct {
	base dnfrepo.CharacterAssetUnitOfWork
	late bool
}

func (u reviveCoinUseFailUOW) WithinCharacterAssets(ctx context.Context, id string, apply func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
	return u.base.WithinCharacterAssets(ctx, id, func(c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, e dnfrepo.EquipmentRepository) error {
		if !u.late {
			return apply(c, reviveCoinUseFailSave{i}, e)
		}
		if err := apply(c, i, e); err != nil {
			return err
		}
		return errReviveCoinTestSave
	})
}

type reviveCoinUseFailSave struct{ dnfrepo.InventoryRepository }

func (reviveCoinUseFailSave) Save(context.Context, dnfrepo.InventoryRecord) error {
	return errReviveCoinTestSave
}

func TestUseReviveCoinItemRollsBackSaveAndCommitFailure(t *testing.T) {
	for _, late := range []bool{false, true} {
		repos, cmd := reviveCoinUseTestAssets(t)
		ctx := context.Background()
		before, _, _ := repos.Inventory.Load(ctx, "19")
		repos.CharacterAssets = reviveCoinUseFailUOW{base: repos.CharacterAssets, late: late}
		owner, _ := NewOwner(repos)
		result, err := owner.UseReviveCoinItem(ctx, cmd)
		if !errors.Is(err, errReviveCoinTestSave) || result.Changed || result.ReviveCoinWalletUpdated {
			t.Fatalf("失败结果不匹配: late=%t result=%+v err=%v", late, result, err)
		}
		after, _, _ := repos.Inventory.Load(ctx, "19")
		if !reflect.DeepEqual(before, after) {
			t.Fatal("保存失败后源物品或钱包未回滚")
		}
	}
}

func TestUseReviveCoinItemLastItemCannotBeReusedAndOp44StillRequiresInstance(t *testing.T) {
	repos, cmd := reviveCoinUseTestAssets(t)
	ctx := context.Background()
	inventory, _, _ := repos.Inventory.Load(ctx, "19")
	inventory.Slots["0:68"] = dnfrepo.ItemStack{ItemID: 42, Count: 1}
	_ = repos.Inventory.Save(ctx, inventory)
	owner, _ := NewOwner(repos)
	result, err := owner.UseReviveCoinItem(ctx, cmd)
	if err != nil || result.RemainingCount != 0 || result.ReviveCoinWalletTotal != 8 {
		t.Fatalf("首次使用失败: %+v %v", result, err)
	}
	if _, err := owner.UseReviveCoinItem(ctx, cmd); !errors.Is(err, ErrSlotNotFound) {
		t.Fatalf("空源格应拒绝重放: %v", err)
	}
	if _, err := owner.UseStackable(ctx, Command{SelectedCharacterID: 19, SourceListType: 0, SourceSlotIndex: 68, ItemTemplateID: 42}, nil); !errors.Is(err, ErrUseStackableContractRequired) {
		t.Fatalf("op44实例检查被放宽: %v", err)
	}
}
