package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// The registered item is moved through the real op19 owner with another copy
// of the same template present. Registration must continue to identify the
// registered physical instance in both bag and worn containers.
func TestR195BuffRegisteredInstanceWornInventoryWornWithTemplateSibling(t *testing.T) {
	f := p94Fixture(t)
	f.session.accountFromToken = true // Carry real provenance without enabling a public strict policy.
	instance := p94Register(t, f)
	ctx := context.Background()
	inv, eq, before, legacy := p94Snapshot(t, f)
	sibling := repo.ItemStack{ItemID: p94Item, Count: 1, RawEntry: append([]byte(nil), eq.Entries["16"].RawEntry...), Extra: map[string]string{}}
	for key, value := range eq.Entries["16"].Extra {
		if key != "native_buff_instance" {
			sibling.Extra[key] = value
		}
	}
	sibling.Extra["r195_template_sibling"] = "not-registered-B"
	binary.LittleEndian.PutUint16(sibling.RawEntry, 58)
	if inv.Slots == nil {
		inv.Slots = make(map[string]repo.ItemStack)
	}
	inv.Slots["0:58"] = sibling
	if err := f.group.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	inv, eq, before, legacy = p94Snapshot(t, f)

	// Worn -> the actual owner-selected inventory destination.
	p94Move(t, f, flaggedBody(t, loginWearBridgeBody(0, 15, 0, 3, int16(p94Slot), int32(p94Item)), op19CurrentUIPlayerFlag))
	inv, eq, after, old := p94Snapshot(t, f)
	registered, ok := inv.Slots["0:15"]
	if !ok || registered.ItemID != p94Item || registered.Extra["native_buff_instance"] != instance {
		t.Fatalf("registered A did not move to inventory: %+v", inv.Slots["0:15"])
	}
	if inv.Slots["0:58"].Extra["native_buff_instance"] != "" || inv.Slots["0:58"].Extra["r195_template_sibling"] != "not-registered-B" {
		t.Fatal("same-template sibling identity was overwritten")
	}
	if eq.Entries["16"].ItemID != 0 || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(legacy, old) {
		t.Fatal("unequip crossed the registration or mutated its owner")
	}
	p94AssertResolved(t, f, instance, 0, 15, true)
	p94Wire(t, f.buffer.write.Bytes(), 0, 15, true, true)

	// Inventory -> worn. The same instance must return without re-registration.
	p94Move(t, f, flaggedBody(t, loginWearBridgeBody(0, 15, int32(p94Item), 3, int16(p94Slot), 0), op19CurrentUIPlayerFlag))
	inv, eq, after, old = p94Snapshot(t, f)
	if inv.Slots["0:15"].ItemID != 0 || eq.Entries["16"].Extra["native_buff_instance"] != instance {
		t.Fatal("re-equip did not restore the same registered instance")
	}
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(legacy, old) {
		t.Fatal("re-equip mutated the durable registration")
	}
	p94AssertResolved(t, f, instance, 3, uint16(p94Slot), true)
	p94Wire(t, f.buffer.write.Bytes(), 3, uint16(p94Slot), true, true)
}
