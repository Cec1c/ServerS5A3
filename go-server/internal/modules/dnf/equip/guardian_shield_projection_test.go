package equip

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestGuardianShieldProjectionCannotBeWithdrawnAsOrdinaryEquipment(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	raw := make([]byte, 0x77)
	binary.LittleEndian.PutUint16(raw, 24)
	binary.LittleEndian.PutUint32(raw[2:], 113370003)
	before := dnfrepo.EquipmentRecord{CharacterID: "77", Entries: map[string]dnfrepo.EquipmentEntry{"24": {SlotIndex: 24, ItemID: 113370003, RawEntry: raw, Extra: map[string]string{"guardian_shield_deck_projection": "1", "current_exe_runtime_move": "1", "current_exe_equipment_type": "24"}}}}
	if err := repos.Equipment.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77"}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwnerWithPlacementValidator(repos, PlacementValidatorFunc(func(context.Context, Placement) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.Move(ctx, MoveCommand{SelectedCharacterID: 77, SourceListType: 0, SourceSlotIndex: 9, DestinationListType: 3, DestinationSlotIndex: 24, DestinationInstanceValue: 113370003, MoveCount: 1, WornSlotClaimOccurrence: 2, PlayerRequestFlag: true})
	if !errors.Is(err, ErrMoveUnsupported) {
		t.Fatalf("普通穿脱未阻止盾牌投影出包: %v", err)
	}
	got, _, _ := repos.Equipment.Load(ctx, "77")
	bag, _, _ := repos.Inventory.Load(ctx, "77")
	if !reflect.DeepEqual(got, before) || len(bag.Slots) != 0 {
		t.Fatalf("拒绝后资产变化: equipment=%+v inventory=%+v", got, bag)
	}
}
