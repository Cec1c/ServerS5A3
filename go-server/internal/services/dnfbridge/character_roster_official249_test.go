package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Walk the actual native mode-2 grammar independently of the writer's
// constants. Two rows are essential: one row used to borrow the packet tail
// for its missing four flags and hid the incorrect boundary.
func TestOfficial249RosterNativeTwoRowBoundaries(t *testing.T) {
	characters := []dnfrepo.CharacterRecord{
		{CharacterID: "26", Name: "c51", Slot: 0, Job: "0", Level: 1},
		{CharacterID: "27", Name: "新角色", Slot: 3, Job: "14", Level: 90, Stats: map[string]int64{"grow_type": 1}},
	}
	characters[0].Roster.Entry.EquipSummary = []dnfrepo.CharacterRosterEquipSummary{{Slot: 12, ItemIDOrIcon: 10001}}
	s := &Service{}
	s.options.currentClientVersion = "official-24.9"
	body := s.buildCurrentRosterBody(characters)
	if binary.LittleEndian.Uint16(body[13:15]) != 2 {
		t.Fatal("native row count differs from repository count")
	}
	cursor := 15
	for _, character := range characters {
		if binary.LittleEndian.Uint16(body[cursor:]) != uint16(character.Slot) {
			t.Fatalf("row slot is misaligned at %d", cursor)
		}
		cursor += 2
		n := int(binary.LittleEndian.Uint32(body[cursor:]))
		cursor += 4
		if n > 30 || !bytes.Equal(body[cursor:cursor+n], rosterRawNameBytes(character)) {
			t.Fatalf("native DSTR length/name is misaligned at %d", cursor)
		}
		cursor += n + 2 // name and 0x022c17e0's two reserved bytes
		if body[cursor] != byte(numericCharacterStat(character.Job)) || body[cursor+1] != byte(character.Stats["grow_type"]) || body[cursor+2] != byte(character.Level) {
			t.Fatalf("native job/grow/level differs from repository at %d", cursor)
		}
		cursor += 2 + 3 + 8
		equipmentCount := int(body[cursor])
		cursor++
		for row := 0; row < equipmentCount; row++ {
			cursor += 1 + 4 // slot and item ID
			blobBytes := int(binary.LittleEndian.Uint32(body[cursor:]))
			cursor += 4 + blobBytes + 1 + 4 + 4 + 1
		}
		// Scalars/appearance token (24), native status (1), and 17 tail flags.
		if body[cursor+24] != 3 {
			t.Fatal("native normal status changed")
		}
		cursor += 24 + 1 + 17
	}
	if len(body)-cursor != 10 || body[cursor] != 1 {
		t.Fatalf("native rows did not end at the 10-byte mode-2 packet tail: cursor=%d len=%d", cursor, len(body))
	}
	s.options.currentClientVersion = "90cn"
	if old := s.buildCurrentRosterBody(characters); !bytes.Equal(old, buildNoPackRosterBody(characters)) || len(body)-len(old) != 8 {
		t.Fatal("legacy profile changed or four flags per native row were not added")
	}
}
