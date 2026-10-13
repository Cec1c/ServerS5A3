package itemquality

import (
	"encoding/binary"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestFromStackRejectsUnprovenOrInvalidQuality(t *testing.T) {
	raw := make([]byte, 0x77)
	binary.LittleEndian.PutUint32(raw[2:6], 1001)
	binary.LittleEndian.PutUint32(raw[6:10], 572024372)
	for _, tc := range []struct {
		name  string
		stack dnfrepo.ItemStack
	}{
		{"stackable_raw_is_count", dnfrepo.ItemStack{ItemID: 1001, RawEntry: raw, Extra: map[string]string{"item_kind": "stackable"}}},
		{"unknown_kind_raw", dnfrepo.ItemStack{ItemID: 1001, RawEntry: raw}},
		{"foreign_raw_item", dnfrepo.ItemStack{ItemID: 1002, RawEntry: raw, Extra: map[string]string{"item_kind": "equipment"}}},
		{"legacy_short_row", dnfrepo.ItemStack{ItemID: 1001, RawEntry: raw[:43], Extra: map[string]string{"item_kind": "equipment"}}},
		{"negative", dnfrepo.ItemStack{Extra: map[string]string{"quality_seed": "-1"}}},
		{"out_of_range", dnfrepo.ItemStack{Extra: map[string]string{"quality_seed": "999999999"}}},
		{"overflow", dnfrepo.ItemStack{Extra: map[string]string{"quality_seed": "4294967297"}}},
		{"zero", dnfrepo.ItemStack{Extra: map[string]string{"quality_seed": "0"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := FromStack(tc.stack); got != 0 {
				t.Fatalf("unproven quality=%d", got)
			}
		})
	}
}
