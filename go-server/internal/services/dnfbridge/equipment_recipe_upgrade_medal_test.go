package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// The recipe uses the native op25 request and maintain-ability fields; only
// the equipment family changes. No private reward IDs or configuration here.
func TestEquipmentRecipeMedalUsesDedicatedPageAndKeepsSockets(t *testing.T) {
	for _, mode := range []string{"slot-zero", "full-page", "wrong-container", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			repos, s, session := recipeUpgradeFixture(t, recipeUpgradeDrawing)
			source := s.pvfItemCatalog.source.(dungeonDropCatalogTestSource)
			for _, path := range []string{"equipment/armor/input.equ", "equipment/armor/output.equ"} {
				source[path] = "[equipment type]\n`[flag]` 21\n[grade]\n50\n[attach type]\n`[trade impossible]`\n[minimum level]\n50\n"
			}
			catalog, err := newPVFDungeonDropCatalog(source)
			if err != nil {
				t.Fatal(err)
			}
			s.pvfItemCatalog = catalog
			inv, _, _ := repos.Inventory.Load(ctx, "19")
			stack := inv.Slots["0:9"]
			delete(inv.Slots, "0:9")
			raw := currentGuardianGemTestRaw(0, 5000, 1)
			raw[10] = 7
			for i, v := range []uint16{24, 34, 44, 104} {
				binary.LittleEndian.PutUint16(raw[101+2*i:103+2*i], v)
			}
			stack.RawEntry = raw
			stack.Extra = map[string]string{"item_kind": "equipment", "instance_note": "keep"}
			inv.Slots["38:0"] = stack
			if mode == "full-page" {
				for i := int16(1); i <= currentGuildMedalPageSlotEnd; i++ {
					inv.Slots[currentCeraShopInventorySlotKey(38, i)] = repo.ItemStack{ItemID: 4000, Count: 1}
				}
			}
			if mode == "wrong-container" {
				delete(inv.Slots, "38:0")
				inv.Slots["0:9"] = stack
			}
			if mode == "ambiguous" {
				other := stack
				other.RawEntry = append([]byte(nil), raw...)
				binary.LittleEndian.PutUint16(other.RawEntry[:2], 1)
				inv.Slots["38:1"] = other
			}
			if err = repos.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			_, err = s.commitCurrentEquipmentRecipeCompound(ctx, session, currentEquipmentRecipeCompoundRequest{SourceSlot: 69, Count: 1})
			after, _, _ := repos.Inventory.Load(ctx, "19")
			if mode == "wrong-container" || mode == "ambiguous" {
				if err == nil || !reflect.DeepEqual(inv, after) {
					t.Fatal("invalid source mutated inventory", mode, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := append([]byte(nil), raw...)
			binary.LittleEndian.PutUint32(want[2:6], 6000)
			if got := after.Slots["38:0"]; got.ItemID != 6000 || !bytes.Equal(got.RawEntry, want) || got.Extra["instance_note"] != "keep" {
				t.Fatalf("medal instance changed got=%x want=%x", got.RawEntry, want)
			}
			if _, exists := after.Slots["0:9"]; exists {
				t.Fatal("medal leaked into main equipment bag")
			}
			if _, err = s.commitCurrentEquipmentRecipeCompound(ctx, session, currentEquipmentRecipeCompoundRequest{SourceSlot: 69, Count: 1}); err == nil {
				t.Fatal("replay accepted")
			}
			replay, _, _ := repos.Inventory.Load(ctx, "19")
			if !reflect.DeepEqual(after, replay) {
				t.Fatal("replay mutated inventory")
			}
		})
	}
}
