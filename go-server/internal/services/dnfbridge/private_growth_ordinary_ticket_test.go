package dnfbridge

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/inventory"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPrivateGrowthOrdinaryTicketTarget(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("paired PVF required")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	for k := int64(0); k < 5; k++ {
		id := int64(990033101) + k
		d, err := c.ResolveItem(uint32(id))
		if err != nil {
			t.Fatal(err)
		}
		if d.MinimumLevel != 90 || d.Grade != 90 {
			t.Fatalf("growth definition level/grade: %+v", d)
		}
		text, err := a.ReadText(d.PVFPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(text, "售价：") || strings.Contains(text, "无金币费用") {
			t.Fatal("removed description remains")
		}
		mode, _, _ := inventory.GrowthTicketType(id)
		for _, level := range []int64{7, 12, 31} {
			material := int64(990034000) + level
			if k > 0 {
				material += 100
			}
			if level == 7 {
				material = 10008065
				if k > 0 {
					material = 10008086
				}
			}
			r, err := resolveCurrentUpgradeTicketMetadata(c, a, material, id)
			if err != nil {
				t.Fatal(err)
			}
			if r.TicketMode != mode || r.TargetMinimumLevel != 90 || r.TargetLevel != level || r.TargetUpgradeForbidden {
				t.Fatalf("resolution: %+v", r)
			}
			for _, success := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/%d/%v", id, level, success), func(t *testing.T) {
					ctx := context.Background()
					g := memory.NewMemoryGroup()
					target := repo.ItemStack{ItemID: id, Count: 1, RawEntry: make([]byte, 119), Extra: map[string]string{"item_kind": "equipment", "keep": "value"}}
					if err := inventory.InitializeGrowthTicket(&target, 7); err != nil {
						t.Fatal(err)
					}
					if _, attr, _ := inventory.GrowthTicketType(id); attr != 0 {
						target.RawEntry[0x13] = attr
						target.RawEntry[0x14] = 7
						for _, key := range []string{"amplify_type", "amplification_type", "byte_13", "value_13", "value_c"} {
							target.Extra[key] = fmt.Sprint(attr)
						}
						for _, key := range []string{"amplify_value", "amplification_value", "marker_16", "marker16", "value_d"} {
							target.Extra[key] = "7"
						}
					}
					materialStack := repo.ItemStack{ItemID: material, Count: 1, RawEntry: make([]byte, 119), Extra: map[string]string{"item_kind": "stackable"}}
					if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "77", AccountID: "acc", Level: 90}); err != nil {
						t.Fatal(err)
					}
					before := repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:9": target, "0:121": materialStack}}
					if err := g.Inventory.Save(ctx, before); err != nil {
						t.Fatal(err)
					}
					owner, err := inventory.NewOwner(g)
					if err != nil {
						t.Fatal(err)
					}
					q := inventory.NewUpgradeCommand(alignedcmd.Request{AccountID: "acc", SelectedCharacterID: 77}, inventory.UpgradeItemRequest{Mode: mode, TargetSlotIndex: 9, TargetItemTemplateID: int32(id), MaterialSlotIndex: 121, OptionalTicketSlotIndex: -1})
					// Force only RNG boundaries; keep all actual PVF eligibility and level metadata.
					resolution := r
					resolution.SuccessWeight = 0
					if success {
						resolution.SuccessWeight = 100000
					}
					out, err := owner.UpgradeTicket(ctx, q, func(m, targetID int64) (alignedcmd.UpgradeTicketResolution, error) {
						if m != material || targetID != id {
							t.Fatal("wrong identity")
						}
						return resolution, nil
					})
					if err != nil || !out.Success || !out.TicketResolved || out.UpgradeSucceeded != success {
						t.Fatalf("upgrade: %+v / %v", out, err)
					}
					after, ok, err := g.Inventory.Load(ctx, "77")
					if err != nil || !ok {
						t.Fatal(err)
					}
					if _, exists := after.Slots["0:121"]; exists {
						t.Fatal("ordinary coupon not consumed")
					}
					grown := after.Slots["0:9"]
					want := byte(0)
					if success {
						want = byte(level)
					}
					if inventory.GrowthTicketLevel(grown) != want || !inventory.GrowthTicketUsable(grown, time.Now()) || grown.ItemID != id || grown.Count != 1 || grown.Extra["keep"] != "value" {
						t.Fatalf("growth target lost state: %+v", grown)
					}
					if !success && !reflect.DeepEqual(grown, target) {
						t.Fatal("ordinary failed roll changed growth coupon")
					}
				})
			}
		}
	}
}
