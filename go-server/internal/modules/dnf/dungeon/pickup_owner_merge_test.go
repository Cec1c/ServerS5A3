package dungeon

import (
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 「都要堆叠」：副本掉落/拾取落库路径（PlacePickupItem）在并入既有行之前必须过
// dnfrepo.CanMergeItemStack —— 同 item 但身份键（pvf_path / stackable_type）或
// 实例到期时间冲突的行不得并，只能另开一格。
func TestPlacePickupItemMergesOnlySameIdentityStack(t *testing.T) {
	const (
		itemID  uint32 = 3227
		oldPath        = "stackable/material/old_drop.stk"
		newPath        = "stackable/material/new_drop.stk"
	)
	definition := PickupItemDefinition{
		ItemID:     itemID,
		Kind:       PickupItemStackable,
		StackLimit: 999,
		SlotStart:  121,
		SlotEnd:    176,
	}
	placement := func(identity dnfrepo.ItemStack) PickupItemPlacement {
		return PickupItemPlacement{
			Definition:    definition,
			Amount:        1,
			MergeIdentity: identity,
			BuildNew: func(slot int16) (dnfrepo.ItemStack, error) {
				return dnfrepo.ItemStack{
					ItemID: int64(itemID),
					Count:  1,
					Extra:  map[string]string{"pvf_path": newPath},
				}, nil
			},
		}
	}

	tests := []struct {
		name        string
		existing    dnfrepo.ItemStack
		identity    dnfrepo.ItemStack
		wantSlot    uint16
		wantOld     int64
		wantNewSlot bool
	}{
		{
			name:     "同身份（pvf_path 相同）→ 并入既有行",
			existing: dnfrepo.ItemStack{ItemID: int64(itemID), Count: 5, Extra: map[string]string{"pvf_path": oldPath}},
			identity: dnfrepo.ItemStack{ItemID: int64(itemID), Extra: map[string]string{"pvf_path": oldPath}},
			wantSlot: 121, wantOld: 6,
		},
		{
			name:     "同 item 但 pvf_path 不同 → 不得并，另开一格",
			existing: dnfrepo.ItemStack{ItemID: int64(itemID), Count: 5, Extra: map[string]string{"pvf_path": oldPath}},
			identity: dnfrepo.ItemStack{ItemID: int64(itemID), Extra: map[string]string{"pvf_path": newPath}},
			wantSlot: 122, wantOld: 5, wantNewSlot: true,
		},
		{
			name:     "实例到期时间冲突 → 不得并",
			existing: dnfrepo.ItemStack{ItemID: int64(itemID), Count: 5, Extra: map[string]string{"pvf_path": oldPath, "expire_time": "100"}},
			identity: dnfrepo.ItemStack{ItemID: int64(itemID), Extra: map[string]string{"pvf_path": oldPath, "expire_time": "200"}},
			wantSlot: 122, wantOld: 5, wantNewSlot: true,
		},
		{
			name:     "既有行没有身份证据（老数据）→ 缺省即兼容，并入",
			existing: dnfrepo.ItemStack{ItemID: int64(itemID), Count: 5},
			identity: dnfrepo.ItemStack{ItemID: int64(itemID), Extra: map[string]string{"pvf_path": newPath}},
			wantSlot: 121, wantOld: 6,
		},
		{
			name:     "绑定不同 → 不得并",
			existing: dnfrepo.ItemStack{ItemID: int64(itemID), Count: 5, Extra: map[string]string{"pvf_path": oldPath}},
			identity: dnfrepo.ItemStack{ItemID: int64(itemID), Bind: true, Extra: map[string]string{"pvf_path": oldPath}},
			wantSlot: 122, wantOld: 5, wantNewSlot: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			record := dnfrepo.InventoryRecord{
				CharacterID: "19",
				Slots:       map[string]dnfrepo.ItemStack{"0:121": tc.existing},
			}
			slot, stack, err := PlacePickupItem(&record, placement(tc.identity))
			if err != nil {
				t.Fatalf("PlacePickupItem error = %v", err)
			}
			if slot != tc.wantSlot {
				t.Fatalf("slot = %d, want %d (inventory=%+v)", slot, tc.wantSlot, record.Slots)
			}
			if got := record.Slots["0:121"].Count; got != tc.wantOld {
				t.Fatalf("existing row count = %d, want %d", got, tc.wantOld)
			}
			if tc.wantNewSlot {
				if stack.ItemID != int64(itemID) || stack.Count != 1 {
					t.Fatalf("new row stack = %+v", stack)
				}
				if _, occupied := record.Slots["0:121"]; !occupied {
					t.Fatal("existing row disappeared")
				}
			} else if stack.Count != tc.wantOld {
				t.Fatalf("merged stack = %+v, want count %d", stack, tc.wantOld)
			}
		})
	}
}
