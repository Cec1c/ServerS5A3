package dnfbridge

import (
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestWornEquipmentProjectionClearsHistoricalSortLockByte(t *testing.T) {
	raw := make([]byte, currentItemListEntryWireSize)
	raw[0x60] = 1
	raw[0x61] = 0xa5
	worn := dnfrepo.EquipmentEntry{ItemID: 100390003, SlotIndex: 25, RawEntry: raw, Extra: map[string]string{"current_exe_runtime_move": "1", "current_exe_equipment_type": "25", dnfrepo.SortLockExtraKey: "1"}}
	row, ok := currentItemListEntryFromEquipment(worn)
	if !ok {
		t.Fatal("projection rejected worn row")
	}
	if row.data[0x60] != 0 {
		t.Fatal("worn row still presents a position lock")
	}
	if row.data[0x61] != 0xa5 || raw[0x60] != 1 {
		t.Fatal("projection changed unrelated bytes or input")
	}
}
