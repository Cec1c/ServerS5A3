package inventory

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestSortRequestedConditionChangesConsumableOrder(t *testing.T) {
	for _, tc := range []struct {
		name      string
		condition byte
		want      []int64
	}{
		{"slot", 0, []int64{100, 200, 300}},
		{"type", 3, []int64{300, 100, 200}},
		{"expiry", 4, []int64{200, 300, 100}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			slots := map[string]r.ItemStack{
				"0:65": {ItemID: 100, Count: 8, Extra: map[string]string{"item_kind": "stackable", "stackable_type": "[z type]"}},
				"0:68": {ItemID: 200, Count: 2, ExpireAt: time.Unix(100, 0), Extra: map[string]string{"item_kind": "stackable", "stackable_type": "[z type]"}},
				"0:70": {ItemID: 300, Count: 3, ExpireAt: time.Unix(200, 0), Extra: map[string]string{"item_kind": "stackable", "stackable_type": "[a type]"}},
				"0:66": {ItemID: 999, Count: 1, Extra: map[string]string{r.SortLockExtraKey: "1"}},
				"0:3":  {ItemID: 888, Count: 20}, "0:121": {ItemID: 777, Count: 11},
			}
			saveTestInventory(t, ctx, repos, r.InventoryRecord{CharacterID: "77", Slots: slots})
			owner, _ := NewOwner(repos)
			cmd := NewSortCommand(alignedcmd.Request{SelectedCharacterID: 77}, SortItemRequest{ListType: 0, Category: 2, Condition: tc.condition})
			if _, err := owner.Sort(ctx, cmd); err != nil {
				t.Fatal(err)
			}
			got := loadTestInventory(t, ctx, repos, "77").Slots
			for i, slot := range []int16{65, 67, 68} {
				if got[slotKey(0, slot)].ItemID != tc.want[i] {
					t.Fatalf("condition=%d sorted items=%v want=%v", tc.condition, got, tc.want)
				}
			}
			for _, key := range []string{"0:66", "0:3", "0:121"} {
				if !reflect.DeepEqual(got[key], slots[key]) {
					t.Fatalf("unrelated/locked slot changed: %s", key)
				}
			}
			for _, item := range slots {
				found := false
				for _, after := range got {
					if reflect.DeepEqual(item, after) {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("instance lost or changed: %+v", item)
				}
			}
			again, err := owner.Sort(ctx, cmd)
			if err != nil || again.Changed {
				t.Fatalf("repeat changed=%v err=%v", again.Changed, err)
			}
		})
	}
}

func TestSortEquipmentConditionsAndResolverFailureAreAtomic(t *testing.T) {
	for _, tc := range []struct {
		condition byte
		want      []int64
	}{
		{0, []int64{100, 200, 300}}, {1, []int64{300, 200, 100}}, {2, []int64{200, 100, 300}}, {3, []int64{200, 300, 100}},
	} {
		t.Run(fmt.Sprint(tc.condition), func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			record := r.InventoryRecord{CharacterID: "77", Slots: map[string]r.ItemStack{"0:10": {ItemID: 100, Count: 1}, "0:12": {ItemID: 200, Count: 1}, "0:15": {ItemID: 300, Count: 1}}}
			saveTestInventory(t, ctx, repos, record)
			owner, _ := NewOwner(repos)
			calls := 0
			cmd := NewSortCommand(alignedcmd.Request{SelectedCharacterID: 77, InventorySortKeys: func(stack r.ItemStack) (alignedcmd.InventorySortKeys, error) {
				calls++
				return map[int64]alignedcmd.InventorySortKeys{100: {Rarity: 3, MinimumLevel: 50, PartRank: 18}, 200: {Rarity: 6, MinimumLevel: 90, PartRank: 12}, 300: {Rarity: 4, MinimumLevel: 1, PartRank: 14}}[stack.ItemID], nil
			}}, SortItemRequest{ListType: 0, Category: 1, Condition: tc.condition})
			if _, err := owner.Sort(ctx, cmd); err != nil {
				t.Fatal(err)
			}
			got := loadTestInventory(t, ctx, repos, "77").Slots
			for i, id := range tc.want {
				if got[slotKey(0, int16(9+i))].ItemID != id {
					t.Fatalf("condition %d: %v", tc.condition, got)
				}
			}
			if tc.condition != 0 && calls != 3 {
				t.Fatalf("resolver calls=%d", calls)
			}
			before := loadTestInventory(t, ctx, repos, "77")
			cmd.Condition = 3
			sentinel := errors.New("PVF unavailable")
			cmd.SortKeys = func(r.ItemStack) (alignedcmd.InventorySortKeys, error) {
				return alignedcmd.InventorySortKeys{}, sentinel
			}
			if _, err := owner.Sort(ctx, cmd); !errors.Is(err, sentinel) {
				t.Fatalf("lookup error=%v", err)
			}
			if !reflect.DeepEqual(before, loadTestInventory(t, ctx, repos, "77")) {
				t.Fatal("failed sort changed inventory")
			}
		})
	}
}
