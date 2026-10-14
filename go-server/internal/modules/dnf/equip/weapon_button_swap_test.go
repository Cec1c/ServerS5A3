package equip

import (
	"context"
	"encoding/binary"
	"errors"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"reflect"
	"testing"
)

func TestWeaponButtonSwapRetainsCountBoundariesAndRollsBackSaveFault(t *testing.T) {
	for _, tc := range []struct {
		name    string
		srcList byte
		srcSlot int16
		dstList byte
		dstSlot int16
		count   int32
		fault   bool
		ok      bool
	}{
		{"button", 3, 12, 3, 24, 0, false, true}, {"legacy_swap", 3, 12, 3, 24, 1, false, true}, {"other_worn_slots", 3, 12, 3, 13, 0, false, false}, {"reverse_zero_not_sent_by_button", 3, 24, 3, 12, 0, false, false}, {"cross_container", 3, 12, 0, 9, 0, false, false}, {"negative", 3, 12, 3, 24, -1, false, false}, {"save_fault", 3, 12, 3, 24, 0, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{}})
			raw := func(slot uint16, id uint32, seed uint32) []byte {
				r := make([]byte, 119)
				binary.LittleEndian.PutUint16(r[:2], slot)
				binary.LittleEndian.PutUint32(r[2:6], id)
				binary.LittleEndian.PutUint32(r[6:10], seed)
				r[10] = 12
				return r
			}
			saveTestEquipment(t, ctx, repos, dnfrepo.EquipmentRecord{CharacterID: "77", Entries: map[string]dnfrepo.EquipmentEntry{"12": {SlotIndex: 12, ItemID: 700, RawEntry: raw(12, 700, 203004656), Extra: map[string]string{"item_kind": "equipment", "quality_seed": "203004656"}}, "24": {SlotIndex: 24, ItemID: 701, RawEntry: raw(24, 701, 70238363), Extra: map[string]string{"item_kind": "equipment", "quality_seed": "70238363"}}}})
			before := loadTestEquipment(t, ctx, repos, "77")
			invBefore := loadTestInventory(t, ctx, repos, "77")
			commitErr := errors.New("button swap save fault")
			if tc.fault {
				repos.CharacterItems = rollbackTestCharacterItems{inventory: repos.Inventory, equipment: repos.Equipment, fail: commitErr}
			}
			validator := &recordingPlacementValidator{}
			owner, err := NewOwnerWithPlacementValidator(repos, validator)
			if err != nil {
				t.Fatal(err)
			}
			_, err = owner.Move(ctx, MoveCommand{SelectedCharacterID: 77, SourceListType: tc.srcList, SourceSlotIndex: tc.srcSlot, DestinationListType: tc.dstList, DestinationSlotIndex: tc.dstSlot, MoveCount: tc.count})
			if tc.ok {
				if err != nil || len(validator.placements) != 2 {
					t.Fatalf("swap err=%v placements=%v", err, validator.placements)
				}
				return
			}
			if err == nil {
				t.Fatal("unproved zero-count move accepted")
			}
			if tc.fault && !errors.Is(err, commitErr) {
				t.Fatalf("wrong failure %v", err)
			}
			if !reflect.DeepEqual(before, loadTestEquipment(t, ctx, repos, "77")) || !reflect.DeepEqual(invBefore, loadTestInventory(t, ctx, repos, "77")) {
				t.Fatal("rejected swap changed assets")
			}
		})
	}
}
