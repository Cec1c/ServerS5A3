package packageitem

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestMagicBoxSingleAndBatchUseSharedSlotPolicy(t *testing.T) {
	for _, batch := range []bool{false, true} {
		for _, scenario := range []struct {
			name     string
			reward   alignedcmd.MagicBoxRewardItem
			fullPage bool
			want     string
		}{
			{"消耗品优先快捷栏", alignedcmd.MagicBoxRewardItem{ItemID: 700, Kind: "stackable", StackLimit: 99, SlotStart: 65, SlotEnd: 120, AllowQuickSlots: true, PreferQuickSlots: true}, false, "0:3"},
			{"材料优先材料页", alignedcmd.MagicBoxRewardItem{ItemID: 700, Kind: "stackable", StackLimit: 99, SlotStart: 121, SlotEnd: 176, AllowQuickSlots: true}, false, "0:121"},
			{"材料满页回落快捷栏", alignedcmd.MagicBoxRewardItem{ItemID: 700, Kind: "stackable", StackLimit: 99, SlotStart: 121, SlotEnd: 176, AllowQuickSlots: true}, true, "0:3"},
			{"宠物物品独立容器", alignedcmd.MagicBoxRewardItem{ItemID: 700, Kind: "stackable", TargetListType: 7, StackLimit: 99, SlotStart: 189, SlotEnd: 238, AllowQuickSlots: true, PreferQuickSlots: true}, false, "7:189"},
		} {
			name := "单开/" + scenario.name
			if batch {
				name = "连开/" + scenario.name
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				repos := dnfrepomemory.NewMemoryGroup()
				slots := map[string]dnfrepo.ItemStack{"0:70": {ItemID: 600, Count: 2}}
				if scenario.fullPage {
					for slot := scenario.reward.SlotStart; slot <= scenario.reward.SlotStart+31; slot++ {
						slots[slotKey(scenario.reward.TargetListType, slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 99}
					}
				}
				saveMagicBoxFixture(t, ctx, repos, slots)
				owner, err := NewOwner(repos)
				if err != nil {
					t.Fatal(err)
				}
				resolution := alignedcmd.MagicBoxResolution{Kind: "random", Groups: []alignedcmd.MagicBoxRewardGroup{{DrawCount: 1, Entries: []alignedcmd.MagicBoxRewardEntry{{ItemID: 700, Count: 2, Weight: 1}}}}}
				resolver := staticMagicBoxRewardResolver(map[int64]alignedcmd.MagicBoxRewardItem{700: scenario.reward})
				var result MagicBoxResult
				wantCount := int64(2)
				if batch {
					result, err = owner.ApplyMagicBoxExpand(ctx, MagicBoxExpandCommand{SelectedCharacterID: 77, RawListType: 4, ListType: 0, SlotIndex: 70, MaterialSlotIndex: -1, OpenCount: 2}, staticMagicBoxResolver(resolution), resolver)
					wantCount = 4
				} else {
					result, err = owner.ApplyMagicBox(ctx, magicBoxTestCommand(70, -1), staticMagicBoxResolver(resolution), resolver)
				}
				if err != nil || !result.Success {
					t.Fatalf("开盒失败：result=%+v err=%v", result, err)
				}
				stored, found, err := repos.Inventory.Load(ctx, "77")
				if err != nil || !found {
					t.Fatalf("读取库存失败：found=%t err=%v", found, err)
				}
				if got := stored.Slots[scenario.want]; got.ItemID != 700 || got.Count != wantCount {
					t.Fatalf("奖励落点错误：want=%s count=%d slots=%+v", scenario.want, wantCount, stored.Slots)
				}
				for key, stack := range stored.Slots {
					if stack.ItemID == 700 && key != scenario.want {
						t.Fatalf("重复发放：key=%s stack=%+v", key, stack)
					}
				}
			})
		}
	}
}

func TestMagicBoxEquipmentFullPageFallsBackWithoutMerging(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "单开"
		if batch {
			name = "连开"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			slots := map[string]dnfrepo.ItemStack{"0:70": {ItemID: 600, Count: 2}, "0:3": {ItemID: 700, Count: 1, Extra: map[string]string{"item_kind": "equipment"}}}
			for slot := int16(9); slot <= 40; slot++ {
				slots[slotKey(0, slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
			}
			saveMagicBoxFixture(t, ctx, repos, slots)
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			resolution := alignedcmd.MagicBoxResolution{Kind: "random", Groups: []alignedcmd.MagicBoxRewardGroup{{DrawCount: 1, Entries: []alignedcmd.MagicBoxRewardEntry{{ItemID: 700, Count: 1, Weight: 1}}}}}
			resolver := staticMagicBoxRewardResolver(map[int64]alignedcmd.MagicBoxRewardItem{700: {ItemID: 700, Kind: "equipment", SlotStart: 9, SlotEnd: 64, StackLimit: 1, AllowQuickSlots: true}})
			var result MagicBoxResult
			last := int16(4)
			if batch {
				last = 5
				result, err = owner.ApplyMagicBoxExpand(ctx, MagicBoxExpandCommand{SelectedCharacterID: 77, RawListType: 4, ListType: 0, SlotIndex: 70, MaterialSlotIndex: -1, OpenCount: 2}, staticMagicBoxResolver(resolution), resolver)
			} else {
				result, err = owner.ApplyMagicBox(ctx, magicBoxTestCommand(70, -1), staticMagicBoxResolver(resolution), resolver)
			}
			if err != nil || !result.Success {
				t.Fatalf("开盒失败：result=%+v err=%v", result, err)
			}
			stored, _, err := repos.Inventory.Load(ctx, "77")
			if err != nil {
				t.Fatal(err)
			}
			for slot := int16(3); slot <= last; slot++ {
				if got := stored.Slots[slotKey(0, slot)]; got.ItemID != 700 || got.Count != 1 {
					t.Fatalf("装备未独立回落快捷格：slot=%d stack=%+v", slot, got)
				}
			}
			if result.OverflowMailID != "" {
				t.Fatal("快捷栏有空格却生成溢出邮件")
			}
		})
	}
}
