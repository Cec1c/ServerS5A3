package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"
)

// This grammar is independent of the server writer so a shared struct change
// cannot hide a wire shift. It pins the native 21-byte row, including its final
// per-row flag, with the fog/ridable/party fields kept after the rows.
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
		ExtendedFlag  byte
	}
	if binary.Size(nativeRow{}) != 21 {
		t.Fatal("native fixture width changed")
	}
	nativeFlags := []byte{0x31, 0x42, 0x53, 0x64, 0x75, 0x86}
	for _, count := range []int{0, 1, 4, 6} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			packet := currentDungeonStartMap{MapID: 36765, HellPartyFogFlag: 0x4a, PartyMemberIndex: 0x5f,
				RidableGroups: [][]currentDungeonStartMapRidableEntry{{{PositionX: 11, PositionY: 22, ObjectCode: 33, Faction: 44, State: 55}}}}
			expected := make([]nativeRow, count)
			for i := range expected {
				expected[i] = nativeRow{byte(i), uint32(474 + i), 10158700, 1, 0x42, 3, 0x45, 0x47, nativeFlags[i]}
				r := expected[i]
				packet.ExtraEntries = append(packet.ExtraEntries, currentDungeonStartMapExtraEntry{PassiveObjectIndex: r.ObjectIndex, GlobalSequence: r.Sequence, ItemID: r.Item, StackCount: r.Count, Endurance: r.Endurance, AmplifyType: r.AmplifyType, AmplifyValue: r.AmplifyValue, ExtendedValue: r.ExtendedValue, ExtendedFlag: r.ExtendedFlag})
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
