package equip

import (
	"bytes"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestSortLockClearedAcrossEquipmentTransitions(t *testing.T) {
	raw := bytes.Repeat([]byte{0x25}, currentEquipmentRawEntryWireSize)
	raw[0x60] = 1
	stack := dnfrepo.ItemStack{ItemID: 100390003, Count: 1, RawEntry: raw, Extra: map[string]string{dnfrepo.SortLockExtraKey: "1", "marker": "retain"}}
	worn, err := entryFromStack(stack, wireListMain, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if worn.Extra[dnfrepo.SortLockExtraKey] != "" || worn.RawEntry[0x60] != 0 {
		t.Fatalf("equipped retained sort lock: extra=%v raw=%d", worn.Extra, worn.RawEntry[0x60])
	}
	// Historical equipped rows can already contain the key. Clearing only
	// at equip time would let them bring it back into the bag on unequip.
	worn.Extra[dnfrepo.SortLockExtraKey] = "1"
	worn.RawEntry[0x60] = 1
	back := stackFromEntry(worn)
	if back.Extra[dnfrepo.SortLockExtraKey] != "" || back.RawEntry[0x60] != 0 {
		t.Fatalf("unequipped retained sort lock: extra=%v raw=%d", back.Extra, back.RawEntry[0x60])
	}
	if stack.RawEntry[0x60] != 1 || stack.Extra[dnfrepo.SortLockExtraKey] != "1" || worn.RawEntry[0x60] != 1 || worn.Extra[dnfrepo.SortLockExtraKey] != "1" {
		t.Fatal("conversion mutated input")
	}
	for i := range raw {
		if i != 0x60 && back.RawEntry[i] != raw[i] {
			t.Fatalf("unrelated raw byte %x changed", i)
		}
	}
	if back.Extra["marker"] != "retain" {
		t.Fatal("other Extra changed")
	}
}
