package inventory

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/itemquality"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// Sorting changes slot addresses, not equipment quality or the persisted item
// instance. The subsequent full-list refresh must keep that same quality.
func TestSortRefreshPreservesEquipmentQuality(t *testing.T) {
	for _, tc := range []struct {
		name    string
		extra   map[string]string
		rawSeed uint32
		want    uint32
	}{
		{"quality_metadata", map[string]string{"item_kind": "equipment", "quality_seed": "572024372", "amount_or_count": "1"}, 123, 572024372},
		{"legacy_quality_metadata", map[string]string{"quality_seed": "572024372"}, 0, 572024372},
		{"top_quality", map[string]string{"item_kind": "equipment", "quality_seed": "999999998"}, 123, itemquality.TopSeed},
		{"legacy_amount", map[string]string{"item_kind": "equipment", "amount_or_count": "572024372"}, 0, 572024372},
		{"legacy_count_or_instance", map[string]string{"item_kind": "equipment", "count_or_instance": "572024372"}, 0, 572024372},
		{"raw_quality", map[string]string{"item_kind": "equipment"}, 572024372, 572024372},
		{"unknown_quality_keeps_count", map[string]string{"item_kind": "equipment"}, 0, 1},
	} {
		for _, list := range []byte{listTypeMain, listTypePersonalCargo} {
			t.Run(tc.name+string(rune('A'+list)), func(t *testing.T) {
				ctx := context.Background()
				repos := dnfrepomemory.NewMemoryGroup()
				raw := make([]byte, currentItemListEntrySize)
				binary.LittleEndian.PutUint16(raw[:2], 20)
				binary.LittleEndian.PutUint32(raw[2:6], 100390003)
				binary.LittleEndian.PutUint32(raw[6:10], tc.rawSeed)
				stack := dnfrepo.ItemStack{ItemID: 100390003, Count: 1, Extra: tc.extra, RawEntry: raw}
				items := map[string]dnfrepo.ItemStack{slotKey(list, 20): stack}
				record := dnfrepo.InventoryRecord{CharacterID: "77"}
				category, target := byte(1), int16(9)
				if list == listTypeMain {
					record.Slots = items
				} else {
					record.Warehouse = items
					category, target = 11, 0
				}
				saveTestInventory(t, ctx, repos, record)
				for attempt := 0; attempt < 2; attempt++ {
					got, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: 20, Body: []byte{list, category}, AccountID: "acc", SelectedCharacterID: 77, Repositories: repos})
					if err != nil || !got.ResponseAllowed || len(got.UpperResponses) != 2 {
						t.Fatalf("sort failed: err=%v result=%+v", err, got)
					}
					body := got.UpperResponses[1].Body
					wantLength := 5 + currentItemListEntrySize
					if list == listTypePersonalCargo {
						wantLength++
					}
					if got.UpperResponses[0].MsgID != 20 || got.UpperResponses[1].MsgID != 13 || len(body) != wantLength {
						t.Fatalf("sort reply order or row size changed: %+v", got.UpperResponses)
					}
					row := body[5:]
					if binary.LittleEndian.Uint16(row[:2]) != uint16(target) || binary.LittleEndian.Uint32(row[2:6]) != uint32(stack.ItemID) {
						t.Fatalf("wrong sorted item address: %x", row[:6])
					}
					if seed := binary.LittleEndian.Uint32(row[6:10]); seed != tc.want {
						t.Errorf("sort attempt %d quality=%d, want %d", attempt, seed, tc.want)
					}
					if value := binary.LittleEndian.Uint32(row[0x0E:0x12]); value != 0 {
						t.Errorf("quality leaked into independent item state: %d", value)
					}
					stored := loadTestInventory(t, ctx, repos, "77")
					storedItems, _ := itemMapForList(&stored, list)
					if !reflect.DeepEqual(storedItems[slotKey(list, target)], stack) {
						t.Fatal("sorting mutated persisted equipment fields")
					}
				}
			})
		}
	}
}

func TestCommonItemListQualityDoesNotReplaceStackableQuantity(t *testing.T) {
	stack := dnfrepo.ItemStack{ItemID: 3277, Count: 23, Extra: map[string]string{"item_kind": "stackable", "count_or_instance": "572024372"}}
	w := &packetWriter{}
	writeCommonItemListEntry(w, 121, stack)
	if count := binary.LittleEndian.Uint32(w.data[6:10]); count != 23 {
		t.Fatalf("stackable quantity=%d, want 23", count)
	}
	if identity := binary.LittleEndian.Uint32(w.data[0x0E:0x12]); identity != 572024372 {
		t.Fatalf("stackable identity=%d", identity)
	}
}
