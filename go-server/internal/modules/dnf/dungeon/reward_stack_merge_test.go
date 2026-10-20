package dungeon

import (
	"context"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 「都要堆叠」：翻牌/通关卡片奖励（addCardItem）也必须先按身份合并，身份键或
// 实例到期时间冲突时另开一格（旧代码只看 item_id + 绑定，带到期时间的奖励还会把
// 自己的到期时间 project 进已有行，等于把两个实例合成一个）。
func TestGrantCardRewardMergesOnlySameIdentityStack(t *testing.T) {
	base := time.Unix(5000, 0).UTC()
	later := time.Unix(9000, 0).UTC()
	project := func(stack dnfrepo.ItemStack, expiration time.Time) (dnfrepo.ItemStack, error) {
		stack.ExpireAt = expiration
		return stack, nil
	}

	tests := []struct {
		name         string
		existing     dnfrepo.ItemStack
		reward       CardItemReward
		wantSlot     int16
		wantExisting int64
		wantNewCount int64
		// wantExistingExpiry 非零时断言既有行的实例到期时间**没有被奖励改写**
		// （带动画/到期时间的奖励走"另开一格"分支时最容易踩这个坑）。
		wantExistingExpiry time.Time
	}{
		{
			name:     "同身份 → 并入既有行",
			existing: dnfrepo.ItemStack{ItemID: 7001, Count: 2, Extra: map[string]string{"pvf_path": "stackable/reward/a.stk"}},
			reward: CardItemReward{
				Stack:     dnfrepo.ItemStack{ItemID: 7001, Count: 3, Extra: map[string]string{"pvf_path": "stackable/reward/a.stk"}},
				Stackable: true,
			},
			wantSlot: 0, wantExisting: 5,
		},
		{
			name:     "同 item 但 pvf_path 不同 → 另开一格",
			existing: dnfrepo.ItemStack{ItemID: 7001, Count: 2, Extra: map[string]string{"pvf_path": "stackable/reward/a.stk"}},
			reward: CardItemReward{
				Stack:     dnfrepo.ItemStack{ItemID: 7001, Count: 3, Extra: map[string]string{"pvf_path": "stackable/reward/b.stk"}},
				Stackable: true,
			},
			wantSlot: 1, wantExisting: 2, wantNewCount: 3,
		},
		{
			name:     "实例到期时间冲突 → 另开一格且不动原行",
			existing: dnfrepo.ItemStack{ItemID: 7001, Count: 2, ExpireAt: base, Extra: map[string]string{"pvf_path": "stackable/reward/a.stk", "expire_time": "5000"}},
			reward: CardItemReward{
				Stack:     dnfrepo.ItemStack{ItemID: 7001, Count: 3},
				Stackable: true,
				ExpireAt:  later,
			},
			wantSlot: 1, wantExisting: 2, wantNewCount: 3,
			wantExistingExpiry: base,
		},
		{
			name:     "既有行没有身份证据（老数据）→ 缺省即兼容，并入",
			existing: dnfrepo.ItemStack{ItemID: 7001, Count: 2},
			reward: CardItemReward{
				Stack:     dnfrepo.ItemStack{ItemID: 7001, Count: 3, Extra: map[string]string{"pvf_path": "stackable/reward/b.stk"}},
				Stackable: true,
			},
			wantSlot: 0, wantExisting: 5,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			seedDungeonOwnerCharacter(t, ctx, repos, 100)
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
				CharacterID: "19",
				Slots:       map[string]dnfrepo.ItemStack{"0:0": tc.existing},
			}); err != nil {
				t.Fatalf("seed inventory: %v", err)
			}
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatalf("NewOwner: %v", err)
			}
			result, err := owner.GrantCardReward(ctx, CardRewardCommand{
				CharacterID: "19",
				MainSlots:   2,
				Bundle:      CardRewardBundle{Items: []CardItemReward{tc.reward}},
				UpdatedAt:   time.Unix(1000, 0).UTC(),
				Project:     project,
			})
			if err != nil {
				t.Fatalf("GrantCardReward: %v", err)
			}
			if len(result.ItemSlots) != 1 || result.ItemSlots[0] != tc.wantSlot {
				t.Fatalf("ItemSlots = %v, want [%d]", result.ItemSlots, tc.wantSlot)
			}
			loaded, found, err := repos.Inventory.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("load inventory found=%t err=%v", found, err)
			}
			if got := loaded.Slots["0:0"].Count; got != tc.wantExisting {
				t.Fatalf("existing row count = %d, want %d", got, tc.wantExisting)
			}
			if tc.wantNewCount > 0 {
				if got := loaded.Slots["0:1"].Count; got != tc.wantNewCount {
					t.Fatalf("new row count = %d, want %d (inventory=%+v)", got, tc.wantNewCount, loaded.Slots)
				}
			} else {
				if _, occupied := loaded.Slots["0:1"]; occupied {
					t.Fatalf("unexpected extra row: %+v", loaded.Slots["0:1"])
				}
			}
			if !tc.wantExistingExpiry.IsZero() &&
				!loaded.Slots["0:0"].ExpireAt.Equal(tc.wantExistingExpiry) {
				t.Fatalf("existing row expiration was rewritten: %v, want %v",
					loaded.Slots["0:0"].ExpireAt, tc.wantExistingExpiry)
			}
		})
	}
}

// 教程奖励（PlaceTutorialReward）同样按身份合并：身份冲突时不得并进已有行。
func TestPlaceTutorialRewardMergesOnlySameIdentityStack(t *testing.T) {
	reward := TutorialItemReward{
		Progress:      1,
		ItemID:        3227,
		Count:         5,
		Consumable:    true,
		StackLimit:    999,
		SlotStart:     121,
		SlotEnd:       176,
		PVFPath:       "stackable/material/tutorial_new.stk",
		StackableType: "[material]",
	}
	project := func(stack dnfrepo.ItemStack, expiration time.Time) (dnfrepo.ItemStack, error) {
		stack.ExpireAt = expiration
		return stack, nil
	}

	t.Run("同身份 → 并进快捷格", func(t *testing.T) {
		record := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{
			"0:4": {
				ItemID: 3227, Count: 2,
				Extra: map[string]string{"pvf_path": "stackable/material/tutorial_new.stk", "stackable_type": "[material]"},
			},
		}}
		slot, err := PlaceTutorialReward(&record, 1, reward, project)
		if err != nil {
			t.Fatalf("PlaceTutorialReward error = %v", err)
		}
		if slot != 4 || record.Slots["0:4"].Count != 7 {
			t.Fatalf("slot=%d inventory=%+v", slot, record.Slots)
		}
	})

	t.Run("身份冲突 → 另开一格且不动原行", func(t *testing.T) {
		record := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{
			"0:4": {
				ItemID: 3227, Count: 2,
				Extra: map[string]string{"pvf_path": "stackable/material/tutorial_old.stk", "stackable_type": "[material]"},
			},
		}}
		if _, err := PlaceTutorialReward(&record, 1, reward, project); err != nil {
			t.Fatalf("PlaceTutorialReward error = %v", err)
		}
		if got := record.Slots["0:4"].Count; got != 2 {
			t.Fatalf("existing quick-slot row mutated: count=%d", got)
		}
		spilled := false
		for slot := int16(3); slot <= 8; slot++ {
			key := mainSlotKey(uint16(slot))
			stack, occupied := record.Slots[key]
			if !occupied || key == "0:4" {
				continue
			}
			if stack.ItemID != 3227 || stack.Count != 5 {
				t.Fatalf("spilled row %s = %+v, want item 3227 count 5", key, stack)
			}
			spilled = true
		}
		if !spilled {
			t.Fatalf("identity conflict did not open a new row: %+v", record.Slots)
		}
	})
}
