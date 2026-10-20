package dnfbridge

import (
	"bytes"
	"encoding/binary"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
)

func TestAikenIndependentMaterialEndpointAliases(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		id        int64
		reject    bool
	}{
		{"same_material_alias", "00:0121", 10161536, true},
		{"different_item_alias", "00:0121", 90001, true},
		{"empty_alias", "00:0121", 0, true},
		{"space_alias", " 0 : 121 ", 90001, true},
		{"plus_alias", "+0:+121", 90001, true},
		{"source_space_alias", " 0 : 9 ", 90001, true},
		{"other_list_same_slot", "1:121", 90001, false},
		{"unrelated_slot_alias", "00:0122", 90001, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, se, g, _, _ := aikenR142Fixture(t, "")
			before := aikenR142Load(t, g)
			before.Slots[tc.key] = repo.ItemStack{ItemID: tc.id, Count: 1}
			aikenR142Save(t, g, before)
			if err := aikenR142Request(t, s, se, 9); err != nil {
				t.Fatal(err)
			}
			after := aikenR142Load(t, g)
			if tc.reject && !reflect.DeepEqual(before, after) {
				t.Fatalf("ambiguous material endpoint committed: key=%q alternateID=%d grade=%d material=%d", tc.key, tc.id, after.Slots["0:9"].RawEntry[0x76], after.Slots["0:121"].Count)
			}
			aikenR142Outcome(t, se, !tc.reject)
		})
	}
}

func TestAikenIndependentSourceQualityProjection(t *testing.T) {
	for _, kind := range []string{"equipment", "", "stackable", "avatar", "nil_extra", "zero_raw_id"} {
		t.Run("kind_"+kind, func(t *testing.T) {
			s, se, g, _, _ := aikenR142Fixture(t, "")
			before := aikenR142Load(t, g)
			x := before.Slots["0:9"]
			if kind == "" {
				delete(x.Extra, "item_kind")
			} else if kind == "nil_extra" {
				x.Extra = nil
			} else if kind == "zero_raw_id" {
				binary.LittleEndian.PutUint32(x.RawEntry[2:6], 0)
			} else {
				x.Extra["item_kind"] = kind
			}
			before.Slots["0:9"] = x
			aikenR142Save(t, g, before)
			if err := aikenR142Request(t, s, se, 9); err != nil {
				t.Fatal(err)
			}
			aikenR142Outcome(t, se, true)
			p, _ := splitGameServerUpperPacket(t, se.conn.(*bufferConn).write.Bytes())
			want := binary.LittleEndian.Uint32(x.RawEntry[6:10])
			got := binary.LittleEndian.Uint32(p.Body[3+6 : 3+10])
			if got != want {
				t.Fatalf("upgrade NOTI14 replaced quality seed with count: got=%d want=%d durableRaw=%d", got, want, binary.LittleEndian.Uint32(aikenR142Load(t, g).Slots["0:9"].RawEntry[6:10]))
			}
			after := aikenR142Load(t, g)
			updated := after.Slots["0:9"]
			expected := append([]byte(nil), x.RawEntry...)
			expected[0x76]++
			if updated.Count != 1 || !bytes.Equal(updated.RawEntry, expected) || p.Body[3+0x0a] != x.RawEntry[0x0a] || after.Slots["0:121"].Count != 89 {
				t.Fatal("upgrade changed target count/opaque row/reinforcement or charged wrong amount")
			}
			cold := currentItemListEntryFromStack(0, 9, updated)
			if binary.LittleEndian.Uint32(cold.data[6:10]) != want || cold.data[0x0a] != x.RawEntry[0x0a] {
				t.Fatal("cold projection lost quality/reinforcement")
			}
		})
	}
}
