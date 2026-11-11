package inventory

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"reflect"
	"testing"
	"time"
)

func TestPrivateGrowthTicketOrdinaryRestrictions(t *testing.T) {
	for id := int64(990033101); id <= 990033105; id++ {
		for _, reason := range []string{"locked", "expired", "wrong_attribute", "wrong_mode", "material_level", "material_list", "write_failure"} {
			t.Run(fmt.Sprintf("%d/%s", id, reason), func(t *testing.T) {
				ctx := context.Background()
				g := memory.NewMemoryGroup()
				target := repo.ItemStack{ItemID: id, Count: 1, RawEntry: make([]byte, 119), Extra: map[string]string{"item_kind": "equipment"}}
				if err := InitializeGrowthTicket(&target, 7); err != nil {
					t.Fatal(err)
				}
				mode, _, _ := GrowthTicketType(id)
				if _, attr, _ := GrowthTicketType(id); attr != 0 {
					setAmplifyState(&target, attr, 7)
				}
				r := alignedcmd.UpgradeTicketResolution{TicketMode: mode, TargetKind: "equipment", TargetMinimumLevel: 90, TargetLevel: 7, SuccessWeight: 100000}
				q := newUpgradeTicketCommand(mode, 9, int32(id), 121)
				switch reason {
				case "locked":
					target.Extra["equipment_lock_state"] = "locked"
				case "expired":
					target.ExpireAt = time.Now().Add(-time.Hour)
				case "wrong_attribute":
					setAmplifyState(&target, 128, 7)
				case "wrong_mode":
					r.TicketMode = "reinforce"
					q.Mode = "reinforce"
					if mode == "reinforce" {
						r.TicketMode = "amplify"
						q.Mode = "amplify"
					}
				case "material_level":
					r.MaterialUsableItemLevelSet = true
					r.MaterialUsableItemLevelMin = 1
					r.MaterialUsableItemLevelMax = 85
				case "material_list":
					r.UsableItemIDs = []int64{700}
				}
				before := repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:9": target, "0:121": {ItemID: 9100, Count: 1, RawEntry: make([]byte, 119), Extra: map[string]string{"item_kind": "stackable"}}}}
				saveTestInventory(t, ctx, g, before)
				before, _, _ = g.Inventory.Load(ctx, "77")
				if reason == "write_failure" {
					g.CharacterItems = growthFailItems{g.CharacterItems}
				}
				o, err := NewOwner(g)
				if err != nil {
					t.Fatal(err)
				}
				out, err := o.UpgradeTicket(ctx, q, staticUpgradeTicketResolver(r))
				if reason == "write_failure" {
					if err == nil {
						t.Fatal("missing injected failure")
					}
				} else if err != nil || out.Success {
					t.Fatalf("invalid target accepted: %+v %v", out, err)
				}
				after, _, _ := g.Inventory.Load(ctx, "77")
				if !reflect.DeepEqual(before, after) {
					t.Fatal("rejection/rollback changed inventory")
				}
			})
		}
	}
}
