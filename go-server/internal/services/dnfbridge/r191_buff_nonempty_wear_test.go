package dnfbridge

import (
	"context"
	"reflect"
	"strconv"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// r191AddWornEquipment inserts one already-worn, unrelated real item after the
// Buff registration exists. It uses the same current-EXE slot metadata as the
// p94 fixture so the op19 owner resolves a real placement.
func r191AddWornEquipment(t *testing.T, f *loginWearAnchorFixture, slot int16, itemID int64) {
	t.Helper()
	definition, err := f.service.pvfItemCatalog.ResolveItem(uint32(itemID))
	if err != nil {
		t.Fatal(err)
	}
	equipment, _, err := f.group.Equipment.Load(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	if equipment.Entries == nil {
		equipment.Entries = map[string]repo.EquipmentEntry{}
	}
	slotText := strconv.Itoa(int(slot))
	equipment.Entries[slotText] = repo.EquipmentEntry{
		SlotIndex: slot,
		ItemID:    itemID,
		RawEntry:  loginWearAnchorRawEntry(slot, itemID, 203004656, 48),
		Extra: map[string]string{
			"item_kind":                  "equipment",
			"pvf_path":                   definition.PVFPath,
			"equipment_type":             definition.EquipmentType,
			"current_exe_runtime_move":   "1",
			"current_exe_equipment_type": slotText,
			"equipped_slot":              slotText,
			"equipment_slot":             slotText,
		},
	}
	if err := f.group.Equipment.Save(context.Background(), equipment); err != nil {
		t.Fatal(err)
	}
}

func r191AssertBuffReplays(t *testing.T, wire []byte, wantSnapshots int) {
	t.Helper()
	acks, snapshots := 0, 0
	for len(wire) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		if packet.Header.Classification == 1 && packet.Header.MsgID == 19 {
			acks++
		}
		if packet.Header.Classification == 0 &&
			(packet.Header.MsgID == currentBuffSwapSnapshotID || packet.Header.MsgID == nativeBuffSwapSnapshotID) {
			snapshots++
		}
	}
	if acks != 1 || snapshots != wantSnapshots {
		t.Fatalf("op19 ACK=%d Buff snapshots=%d want 1/%d", acks, snapshots, wantSnapshots)
	}
}

// R191: a non-empty current1426 registration must survive equipment movement
// without a spurious Buff reset. Non-appearance armor movement has no actor
// rebuild, so an identical resolved projection must not replay 1368. Appearance
// bearing weapon movement still rebuilds the actor and keeps its required
// replay. Moving the registered instance itself must still update list/index.
func TestR191NonEmptyBuffRegistrationUnrelatedEquipmentWear(t *testing.T) {
	f := p94Fixture(t)
	f.service.options.avatarAppearanceCoalesceOff = true
	instance := p94Register(t, f)
	r191AddWornEquipment(t, f, loginWearAnchorArmorSlot17, loginWearAnchorArmorItemID17)
	r191AddWornEquipment(t, f, loginWearAnchorWeaponSlot, loginWearAnchorWeaponItemID)

	_, _, settingsBefore, legacyBefore := p94Snapshot(t, f)

	// Unrelated non-appearance armor: ACK only, no Buff manager reset.
	p94Move(t, f, flaggedBody(t, loginWearBridgeBody(0, 10, 0, 3, loginWearAnchorArmorSlot17, int32(loginWearAnchorArmorItemID17)), op19CurrentUIPlayerFlag))
	r191AssertBuffReplays(t, f.buffer.write.Bytes(), 0)
	p94Move(t, f, flaggedBody(t, loginWearBridgeBody(0, 10, int32(loginWearAnchorArmorItemID17), 3, loginWearAnchorArmorSlot17, 0), op19CurrentUIPlayerFlag))
	r191AssertBuffReplays(t, f.buffer.write.Bytes(), 0)

	// Unrelated visible weapon: the actor is rebuilt, so the existing replay
	// remains. Its bytes still describe the unchanged registered armor row.
	p94Move(t, f, flaggedBody(t, loginWearBridgeBody(0, 20, 0, 3, loginWearAnchorWeaponSlot, int32(loginWearAnchorWeaponItemID)), op19CurrentUIPlayerFlag))
	p94Wire(t, f.buffer.write.Bytes(), 3, uint16(p94Slot), true, true)
	p94Move(t, f, flaggedBody(t, loginWearBridgeBody(0, 20, int32(loginWearAnchorWeaponItemID), 3, loginWearAnchorWeaponSlot, 0), op19CurrentUIPlayerFlag))
	p94Wire(t, f.buffer.write.Bytes(), 3, uint16(p94Slot), true, true)

	invAfter, eqAfter, settingsAfter, legacyAfter := p94Snapshot(t, f)
	if !reflect.DeepEqual(settingsBefore, settingsAfter) || !reflect.DeepEqual(legacyBefore, legacyAfter) {
		t.Fatal("unrelated wear changed registered owner or legacy loadout")
	}
	registered, ok := eqAfter.Entries[strconv.Itoa(int(p94Slot))]
	if !ok || registered.ItemID != p94Item || registered.Extra["native_buff_instance"] != instance {
		t.Fatal("unrelated wear lost registered item identity")
	}
	if invAfter.Slots["0:10"].ItemID != 0 || invAfter.Slots["0:20"].ItemID != 0 {
		t.Fatal("unrelated equipment did not return to its worn slots")
	}
	p94AssertResolved(t, f, instance, 3, uint16(p94Slot), true)

	// Control: moving the registered instance must still project its new
	// coordinate, and moving it back must project the original coordinate.
	p94Move(t, f, flaggedBody(t, loginWearBridgeBody(0, 11, 0, 3, int16(p94Slot), int32(p94Item)), op19CurrentUIPlayerFlag))
	p94Wire(t, f.buffer.write.Bytes(), 0, 11, true, true)
	p94AssertResolved(t, f, instance, 0, 11, true)
	p94Move(t, f, flaggedBody(t, loginWearBridgeBody(0, 11, int32(p94Item), 3, int16(p94Slot), 0), op19CurrentUIPlayerFlag))
	p94Wire(t, f.buffer.write.Bytes(), 3, uint16(p94Slot), true, true)
	p94AssertResolved(t, f, instance, 3, uint16(p94Slot), true)
}
