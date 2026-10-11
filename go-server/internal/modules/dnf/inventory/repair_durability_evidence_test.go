package inventory

import (
	"context"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 回归钉（现场：GM 面板发的装备 0 耐久、领到背包后修不了）：
// 领取端落地到背包的堆栈必须带 durability + max_durability 两条证据，修理才能成立。
// 这里用 alignedcmd.RepairCostResolver 风格的桩（与线上 dnfbridge 的
// currentRepairCostCatalog.resolve 同形）验证"从不足恢复到 max + 金币扣减正确"。
func TestRepairRestoresMailClaimedEquipmentDurability(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{CharacterID: "77", Stats: map[string]int64{"gold": 5000}})
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 9): {
				ItemID: 9001,
				Count:  1,
				// 邮件领取落地的形状：耐久证据来自运行时 PVF 的 [durability] 57。
				Extra: map[string]string{
					"item_kind":      "equipment",
					"pvf_path":       "equipment/weapon/test_sword.equ",
					"durability":     "5",
					"max_durability": "57",
				},
			},
		},
	})

	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatalf("NewOwner error = %v", err)
	}
	// 86JP 公式：6400*(20+5)/10=16000；16000*0.08/57*52 = 1167.7 -> 1167。
	result, err := owner.Repair(ctx, NewRepairCommand(alignedcmd.Request{
		AccountID:           "acc",
		SelectedCharacterID: 77,
	}, RepairEquipmentRequest{
		InvenType: listTypeMain,
		SlotIndex: 9,
	}), inventoryRepairTestResolver(57, 6400, 20))
	if err != nil {
		t.Fatalf("Repair error = %v", err)
	}
	if !result.Changed || result.OldDurability != 5 || result.NewDurability != 57 {
		t.Fatalf("result = %+v, want durability 5 -> 57", result)
	}
	if result.Cost != 1167 || result.UpdatedGold != 5000-1167 {
		t.Fatalf("result cost=%d gold=%d, want cost=1167 gold=%d", result.Cost, result.UpdatedGold, 5000-1167)
	}
	loaded := loadTestInventory(t, ctx, repos, "77")
	if got := loaded.Slots[slotKey(listTypeMain, 9)].Extra["durability"]; got != "57" {
		t.Fatalf("persisted durability = %q, want 57", got)
	}
	if got := loaded.Slots[slotKey(listTypeMain, 9)].Extra["max_durability"]; got != "57" {
		t.Fatalf("persisted max_durability = %q, want 57", got)
	}
	character, _, _ := repos.Character.Load(ctx, "77")
	if character.Stats["gold"] != 5000-1167 {
		t.Fatalf("persisted gold = %d, want %d", character.Stats["gold"], 5000-1167)
	}
}

// 缺耐久证据时必须继续失败关闭：修复的方式是"把证据补齐"，**不是**让
// repairDurabilityOf 在缺证据时返回 max。两条键缺任何一条都必须拒绝，且不得改动资产。
func TestRepairStillRequiresDurabilityEvidence(t *testing.T) {
	for name, extra := range map[string]map[string]string{
		"no extra at all":     nil,
		"only max_durability": {"max_durability": "57"},
		"only durability":     {"durability": "5"},
		"empty durability":    {"durability": "", "max_durability": "57"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{CharacterID: "77", Stats: map[string]int64{"gold": 5000}})
			saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
				CharacterID: "77",
				Slots: map[string]dnfrepo.ItemStack{
					slotKey(listTypeMain, 9): {ItemID: 9001, Count: 1, Extra: extra},
				},
			})

			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatalf("NewOwner error = %v", err)
			}
			_, err = owner.Repair(ctx, NewRepairCommand(alignedcmd.Request{
				AccountID:           "acc",
				SelectedCharacterID: 77,
			}, RepairEquipmentRequest{
				InvenType: listTypeMain,
				SlotIndex: 9,
			}), inventoryRepairTestResolver(57, 6400, 20))
			if !errors.Is(err, ErrRepairDurabilityMissing) && !errors.Is(err, ErrRepairDurabilityInvalid) {
				t.Fatalf("Repair error = %v, want a durability-evidence rejection", err)
			}
			character, _, _ := repos.Character.Load(ctx, "77")
			if character.Stats["gold"] != 5000 {
				t.Fatalf("rejected repair mutated gold: %d", character.Stats["gold"])
			}
			loaded := loadTestInventory(t, ctx, repos, "77")
			if got := loaded.Slots[slotKey(listTypeMain, 9)].Extra["durability"]; got == "57" {
				t.Fatalf("rejected repair filled the durability evidence to max: %q", got)
			}
		})
	}
}
