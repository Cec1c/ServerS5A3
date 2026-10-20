package itemgrade

import (
	"context"
	"errors"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 耳环报障（2026-09-25）的判据回归：材料自己声明的 [usable equip type] 白名单
// 必须生效，表为空 = 不限部位，目标部位解析不出来时不新拦。
//
// 真实 PVF 事实（runtime\data\dnf\Script.pvf，27 个带该段的物品）：
//
//	15       装备品级调整箱                     无该段         → 不限部位
//	2683897  黄金品级调整箱 (首饰)                [amulet] [ring] [wrist]
//	2683898  黄金品级调整箱 (特殊装备)             [support] [magic stone]
//	490701689 黄金品级调整箱 (Lv1~90)             … [support] [magic stone] [earring]
//	490701814 黄金品级调整箱 (特殊装备)             [support] [magic stone] [earring]
func TestAllowsTargetEquipmentTypeUsesPVFUsableEquipType(t *testing.T) {
	tests := []struct {
		name       string
		definition ItemDefinition
		target     string
		want       bool
	}{
		{
			name:       "表为空 = 不限部位（普通装备品级调整箱）",
			definition: ItemDefinition{StackableType: "`[etc]`"},
			target:     "`[earring]`",
			want:       true,
		},
		{
			name:       "首饰箱不含耳环",
			definition: ItemDefinition{UsableEquipTypes: []string{"`[amulet]`", "`[ring]`", "`[wrist]`"}},
			target:     "`[earring]`",
			want:       false,
		},
		{
			name:       "首饰箱含手镯",
			definition: ItemDefinition{UsableEquipTypes: []string{"`[amulet]`", "`[ring]`", "`[wrist]`"}},
			target:     " `[wrist]` ",
			want:       true,
		},
		{
			name:       "特殊装备箱显式列了耳环",
			definition: ItemDefinition{UsableEquipTypes: []string{"`[support]`", "`[magic stone]`", "`[earring]`"}},
			target:     "`[earring]`",
			want:       true,
		},
		{
			name:       "武器箱不含戒指",
			definition: ItemDefinition{UsableEquipTypes: []string{"`[weapon]`"}},
			target:     "`[ring]`",
			want:       false,
		},
		{
			name:       "目标部位解析不出来时不新拦",
			definition: ItemDefinition{UsableEquipTypes: []string{"`[weapon]`"}},
			target:     "",
			want:       true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := AllowsTargetEquipmentType(test.definition, test.target); got != test.want {
				t.Fatalf("AllowsTargetEquipmentType(%v, %q) = %t, want %t",
					test.definition.UsableEquipTypes, test.target, got, test.want)
			}
		})
	}
}

func TestOwnerAdjustRejectsTargetOutsideMaterialUsableEquipType(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			// 目标 = 耳环（100390011，PVF [equipment type] `[earring]`）
			"0:64": {ItemID: 100390011, Count: 1},
			// 材料 = 黄金品级调整箱 (首饰) 2683897
			"0:79": {ItemID: 2683897, Count: 1},
		},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	owner, err := NewOwner(repos, catalogStub{
		2683897: {StackableType: "`[gold kaleido]`", UsableEquipTypes: []string{"`[amulet]`", "`[ring]`", "`[wrist]`"}},
	}, func() (uint32, error) { return 7, nil })
	if err != nil {
		t.Fatalf("NewOwner: %v", err)
	}
	_, err = owner.Adjust(ctx, Command{
		SelectedCharacterID: 19,
		TargetSlot:          64,
		TargetItemID:        100390011,
		MaterialSlot:        79,
		TargetEquipmentType: "`[earring]`",
	})
	if !errors.Is(err, ErrTargetTypeNotUsable) {
		t.Fatalf("Adjust error = %v, want ErrTargetTypeNotUsable", err)
	}
	inventory, _, err := repos.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	if inventory.Slots["0:79"].Count != 1 || inventory.Slots["0:64"].Extra["quality_seed"] != "" {
		t.Fatalf("rejected adjust must not consume or rewrite: %+v", inventory.Slots)
	}
}

func TestOwnerAdjustAcceptsEarringWhenMaterialDeclaresIt(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"0:64": {ItemID: 100390011, Count: 1},
			"0:80": {ItemID: 490701814, Count: 1},
		},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	owner, err := NewOwner(repos, catalogStub{
		490701814: {StackableType: "`[gold kaleido]`", UsableEquipTypes: []string{"`[support]`", "`[magic stone]`", "`[earring]`"}},
	}, func() (uint32, error) {
		t.Fatal("random seed generator called for gold kaleido")
		return 0, nil
	})
	if err != nil {
		t.Fatalf("NewOwner: %v", err)
	}
	result, err := owner.Adjust(ctx, Command{
		SelectedCharacterID: 19,
		TargetSlot:          64,
		TargetItemID:        100390011,
		MaterialSlot:        80,
		TargetEquipmentType: "`[earring]`",
	})
	if err != nil {
		t.Fatalf("Adjust: %v", err)
	}
	if !result.GoldKaleido || !result.MaterialRemoved {
		t.Fatalf("result = %+v", result)
	}
}

// 材料不在 catalog 里（PVF 读失败）时不得因为白名单而拒绝：只能退化到旧行为。
func TestOwnerAdjustKeepsWorkingWhenMaterialDefinitionIsUnresolved(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"0:42": {ItemID: 1001, Count: 1},
			"0:75": {ItemID: 2001, Count: 1},
		},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	owner, err := NewOwner(repos, catalogStub{}, func() (uint32, error) { return 4242, nil })
	if err != nil {
		t.Fatalf("NewOwner: %v", err)
	}
	if _, err := owner.Adjust(ctx, Command{
		SelectedCharacterID: 19,
		TargetSlot:          42,
		TargetItemID:        1001,
		MaterialSlot:        75,
		TargetEquipmentType: "`[earring]`",
	}); err != nil {
		t.Fatalf("Adjust with unresolved material: %v", err)
	}
}
