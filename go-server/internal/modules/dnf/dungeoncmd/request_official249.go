package dungeoncmd

import (
	"encoding/binary"
	"fmt"
)

// DecodeOfficial249DieMonsterRequest follows the 24.9.12.0 native writers
// 0x02758b4b, 0x0283dbef and 0x0268ac99. They write a 31-byte prefix,
// count entries of u16/u16/u64/u16, then a 39-byte actor/runtime block.
// The native footer 0x03750bdb -> 0x0231ea90 appends a four-byte integrity
// field, making the observed body 74+14*count bytes (88 for one attacker).
// Combat values and the native integrity field are retained without inventing
// domain semantics. The caller must still validate room and actor ownership.
func DecodeOfficial249DieMonsterRequest(body []byte) (DieMonsterRequest, error) {
	if len(body) < Official249DieMonsterBaseSize {
		return DieMonsterRequest{}, fmt.Errorf("official 24.9 die-monster body too short: got %d want >= %d", len(body), Official249DieMonsterBaseSize)
	}
	count := int(body[30])
	if count > DieMonsterVariableMaxCombatCount {
		return DieMonsterRequest{}, fmt.Errorf("official 24.9 die-monster count exceeds native limit: %d", count)
	}
	expected := Official249DieMonsterBaseSize + count*Official249DieMonsterEntrySize
	if len(body) != expected {
		return DieMonsterRequest{}, fmt.Errorf("official 24.9 die-monster boundary mismatch: got %d want %d for count %d", len(body), expected, count)
	}
	entriesEnd := 31 + count*Official249DieMonsterEntrySize
	return DieMonsterRequest{
		Layout:                DieMonsterRequestLayoutVariableCombat,
		RuntimeObjectKey:      binary.LittleEndian.Uint32(body[0:4]),
		OwnerObjectKey:        binary.LittleEndian.Uint16(body[4:6]),
		OfficialCombatValue64: [2]uint64{binary.LittleEndian.Uint64(body[6:14]), binary.LittleEndian.Uint64(body[18:26])},
		OfficialCombatValue32: [2]uint32{binary.LittleEndian.Uint32(body[14:18]), binary.LittleEndian.Uint32(body[26:30])},
		CombatEntryCount:      byte(count),
		CombatEntries:         append([]byte(nil), body[31:entriesEnd]...),
		RuntimeTail:           append([]byte(nil), body[entriesEnd:]...),
	}, nil
}
