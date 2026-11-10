package equip

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func grantedMedalMoveFixture(t *testing.T, change func(*dnfrepo.ItemStack, *MoveCommand)) (dnfrepo.Group, MoveCommand) {
	t.Helper()
	stack := dnfrepo.ItemStack{ItemID: 100380017, Count: 1, Bind: true, Extra: map[string]string{
		"item_kind": "equipment", "equipment_type": "[flag]",
		"pvf_path": "equipment/flag/test_medal.equ", "quality_seed": "999999998",
	}}
	cmd := MoveCommand{SelectedCharacterID: 77, SourceListType: wireListGuildMedal, SourceSlotIndex: 0,
		SourceInstanceValue: 0x05F8A08C, MoveCount: 1, DestinationListType: wireListEquipment, DestinationSlotIndex: 32}
	if change != nil {
		change(&stack, &cmd)
	}
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, context.Background(), repos, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{
		inventoryKey(cmd.SourceListType, cmd.SourceSlotIndex): stack,
	}})
	saveTestEquipment(t, context.Background(), repos, dnfrepo.EquipmentRecord{CharacterID: "77", Entries: map[string]dnfrepo.EquipmentEntry{}})
	return repos, cmd
}

func TestOwnerMoveGrantedMedalWithoutRawRoundTrips(t *testing.T) {
	for _, slot := range []int16{0, 48} {
		repos, cmd := grantedMedalMoveFixture(t, func(_ *dnfrepo.ItemStack, cmd *MoveCommand) { cmd.SourceSlotIndex = slot })
		owner, err := newValidatedTestOwner(repos)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := owner.Move(context.Background(), cmd); err != nil {
			t.Fatalf("equip medal at %d: %v", slot, err)
		}
		worn := loadTestEquipment(t, context.Background(), repos, "77").Entries["32"]
		if len(worn.RawEntry) != 0x77 || binary.LittleEndian.Uint32(worn.RawEntry[2:6]) != 100380017 ||
			binary.LittleEndian.Uint32(worn.RawEntry[6:10]) != 999999998 ||
			binary.LittleEndian.Uint32(worn.RawEntry[14:18]) != uint32(cmd.SourceInstanceValue) ||
			binary.LittleEndian.Uint16(worn.RawEntry[11:13]) != 0 || !worn.Bind {
			t.Fatalf("rebuilt medal lost identity or no-durability semantics: %+v", worn)
		}
		owner, err = newValidatedTestOwner(repos)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := owner.Move(context.Background(), MoveCommand{SelectedCharacterID: 77, SourceListType: wireListEquipment,
			SourceSlotIndex: 32, MoveCount: 1, DestinationListType: wireListGuildMedal, DestinationSlotIndex: slot}); err != nil {
			t.Fatal(err)
		}
		back := loadTestInventory(t, context.Background(), repos, "77").Slots[inventoryKey(wireListGuildMedal, slot)]
		if back.ItemID != 100380017 || back.Count != 1 || !back.Bind || len(back.RawEntry) != 0x77 {
			t.Fatalf("unequip failed to preserve medal: %+v", back)
		}
	}
}

func TestOwnerMoveGrantedMedalRejectsUnprovenEvidenceWithoutMutation(t *testing.T) {
	for name, change := range map[string]func(*dnfrepo.ItemStack, *MoveCommand){
		"no client identity": func(_ *dnfrepo.ItemStack, c *MoveCommand) { c.SourceInstanceValue = 0 },
		"no quality":         func(s *dnfrepo.ItemStack, _ *MoveCommand) { delete(s.Extra, "quality_seed") },
		"wrong type":         func(s *dnfrepo.ItemStack, _ *MoveCommand) { s.Extra["equipment_type"] = "[flag gem]" },
		"wrong path":         func(s *dnfrepo.ItemStack, _ *MoveCommand) { s.Extra["pvf_path"] = "stackable/flaggem/test.stk" },
		"stacked":            func(s *dnfrepo.ItemStack, _ *MoveCommand) { s.Count = 2 },
		"wrong target":       func(_ *dnfrepo.ItemStack, c *MoveCommand) { c.DestinationSlotIndex = 31 },
		"gem page":           func(_ *dnfrepo.ItemStack, c *MoveCommand) { c.SourceSlotIndex = 49 },
	} {
		t.Run(name, func(t *testing.T) {
			repos, cmd := grantedMedalMoveFixture(t, change)
			before := loadTestInventory(t, context.Background(), repos, "77")
			owner, err := newValidatedTestOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := owner.Move(context.Background(), cmd); err == nil {
				t.Fatal("unproven medal accepted")
			}
			if after := loadTestInventory(t, context.Background(), repos, "77"); !reflect.DeepEqual(before, after) {
				t.Fatal("rejection changed inventory")
			}
			if got := loadTestEquipment(t, context.Background(), repos, "77"); len(got.Entries) != 0 {
				t.Fatal("rejection changed equipment")
			}
		})
	}
}
