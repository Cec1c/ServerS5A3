package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"
)

// This grammar is independent of the server writer. In the exact v141 module
// 26abf551..., loop 0x1ffbec0..0x1ffbf77 reads u8, three u32, u16,
// u8, two u16. The next u8 is outside that loop, at 0x1ffbf84.
// Constructor 0x2621750 receives its last flag as literal zero.
func TestStartMapExtraRowsMatchNativeLoopAndPreserveFollowingFields(t *testing.T) {
	type nativeRow struct {
		ObjectIndex   byte
		Sequence      uint32
		Item          uint32
		Count         uint32
		Endurance     uint16
		AmplifyType   byte
		AmplifyValue  uint16
		ExtendedValue uint16
	}
	if binary.Size(nativeRow{}) != 20 {
		t.Fatal("native fixture width changed")
	}
	for _, count := range []int{0, 1, 4} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			packet := currentDungeonStartMap{MapID: 36765, HellPartyFogFlag: 0x4a, PartyMemberIndex: 0x5f,
				RidableGroups: [][]currentDungeonStartMapRidableEntry{{{PositionX: 11, PositionY: 22, ObjectCode: 33, Faction: 44, State: 55}}}}
			expected := make([]nativeRow, count)
			for i := range expected {
				expected[i] = nativeRow{byte(i), uint32(474 + i), 10158700, 1, 0x42, 3, 0x45, 0x47}
				r := expected[i]
				packet.ExtraEntries = append(packet.ExtraEntries, currentDungeonStartMapExtraEntry{PassiveObjectIndex: r.ObjectIndex, GlobalSequence: r.Sequence, ItemID: r.Item, StackCount: r.Count, Endurance: r.Endurance, AmplifyType: r.AmplifyType, AmplifyValue: r.AmplifyValue, ExtendedValue: r.ExtendedValue, ExtendedFlag: 0x49})
			}
			body, err := packet.Build()
			if err != nil {
				t.Fatal(err)
			}
			if len(body) < 20 || body[18] != 0 || int(body[19]) != count {
				t.Fatalf("prefix changed: %x", body)
			}
			reader := bytes.NewReader(body[20:])
			got := make([]nativeRow, count)
			if err := binary.Read(reader, binary.LittleEndian, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, expected) {
				t.Fatalf("native rows=%+v expected=%+v", got, expected)
			}
			var fog, groups, entries, party byte
			for _, value := range []*byte{&fog, &groups, &entries} {
				if err := binary.Read(reader, binary.LittleEndian, value); err != nil {
					t.Fatal(err)
				}
			}
			var ridable currentDungeonStartMapRidableEntry
			if err := binary.Read(reader, binary.LittleEndian, &ridable); err != nil {
				t.Fatal(err)
			}
			if err := binary.Read(reader, binary.LittleEndian, &party); err != nil {
				t.Fatal(err)
			}
			if fog != packet.HellPartyFogFlag || groups != 1 || entries != 1 || ridable != packet.RidableGroups[0][0] || party != packet.PartyMemberIndex || reader.Len() != 0 {
				t.Fatalf("native following fields shifted: fog=%x groups=%d entries=%d ridable=%+v party=%x trailing=%d", fog, groups, entries, ridable, party, reader.Len())
			}
		})
	}
}
