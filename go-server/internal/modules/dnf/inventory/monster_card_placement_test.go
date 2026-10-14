package inventory

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestMonsterCardUpgradeUsesPurchasedExpansionWithoutCargoDependencies(t *testing.T) {
	for _, expanded := range []bool{false, true} {
		t.Run(fmt.Sprintf("已扩容=%t", expanded), func(t *testing.T) {
			slots := map[string]repo.ItemStack{}
			for slot := int16(233); slot <= 264; slot++ {
				slots[slotKey(0, slot)] = cardTestStack(120, 1, 0)
			}
			slots["0:233"] = cardTestStack(110, 3, 0)
			slots["0:234"] = cardTestStack(111, 2, 0)
			o, repos := cardTestOwner(t, slots)
			ctx := context.Background()
			if expanded {
				settings := testInitialContainerState("77", time.Now())
				settings.Values["main_list_param16"] = "8"
				if err := repos.Settings.Save(ctx, settings); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := repos.Inventory.Load(ctx, "77")
			rolls := 0
			cmd := MonsterCardCommand{CharacterID: 77, TargetSlot: 233, MaterialSlot: 234, MaterialCount: 1, Random: func(int) int { rolls++; return 0 }}
			result, err := o.UpgradeMonsterCard(ctx, cmd, cardTestResolve, cardTestRules())
			if err != nil {
				t.Fatal(err)
			}
			after, _, _ := repos.Inventory.Load(ctx, "77")
			if !expanded {
				if result.Applied || rolls != 0 || !reflect.DeepEqual(before, after) {
					t.Fatal("基础页满时进入了未解锁格或消耗资产", result, rolls)
				}
			} else {
				level, valid := repo.MonsterCardUpgradeCount(after.Slots["0:265"])
				if !result.Upgraded || result.ResultSlot != 265 || rolls != 1 || !valid || level != 1 || after.Slots["0:233"].Count != 2 || after.Slots["0:234"].Count != 1 {
					t.Fatal("已购扩容未用于升级分格或消耗不准确", result, after)
				}
			}
		})
	}
}

func TestMonsterCardCompoundRejectsBinderInUnopenedSlotWithoutLoss(t *testing.T) {
	o, repos := cardTestOwner(t, map[string]repo.ItemStack{"0:97": {ItemID: 900, Count: 1}, "0:233": cardTestStack(100, 2, 0)})
	ctx := context.Background()
	before, _, _ := repos.Inventory.Load(ctx, "77")
	rolls := 0
	result, err := o.CompoundMonsterCards(ctx, MonsterCardCommand{CharacterID: 77, BinderSlot: 97, InputSlots: []int16{233, 233}, Random: func(int) int { rolls++; return 0 }}, cardTestResolve, cardTestRules())
	after, _, _ := repos.Inventory.Load(ctx, "77")
	if err != nil || result.Applied || rolls != 0 || !reflect.DeepEqual(before, after) {
		t.Fatal("未开放的合成器来源格未拒绝或资产改变", result, err)
	}
}

func TestMonsterCardInvalidContainerSettingsCannotConsumeMaterials(t *testing.T) {
	o, repos := cardTestOwner(t, map[string]repo.ItemStack{"0:233": cardTestStack(110, 2, 0), "0:234": cardTestStack(111, 1, 0)})
	ctx := context.Background()
	settings := testInitialContainerState("77", time.Now())
	settings.Values["main_list_param16"] = "无效值"
	if err := repos.Settings.Save(ctx, settings); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repos.Inventory.Load(ctx, "77")
	result, err := o.UpgradeMonsterCard(ctx, MonsterCardCommand{CharacterID: 77, TargetSlot: 233, MaterialSlot: 234, MaterialCount: 1, Random: func(int) int { t.Fatal("无效容量不应抽样"); return 0 }}, cardTestResolve, cardTestRules())
	after, _, _ := repos.Inventory.Load(ctx, "77")
	if err == nil || result.Applied || !reflect.DeepEqual(before, after) {
		t.Fatal("容量解析失败未保持材料", result, err)
	}
}
