package dungeoncmd

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestOfficial249DieMonsterNativeWriterBoundaries(t *testing.T) {
	for _, count := range []int{0, 1, 2, 254} {
		body := make([]byte, 74+14*count)
		binary.LittleEndian.PutUint32(body[0:4], 403)
		binary.LittleEndian.PutUint16(body[4:6], 27)
		binary.LittleEndian.PutUint64(body[6:14], 0x123456789abcdef0)
		binary.LittleEndian.PutUint32(body[14:18], 0x87654321)
		binary.LittleEndian.PutUint64(body[18:26], 0xfedcba9876543210)
		binary.LittleEndian.PutUint32(body[26:30], 0xabcdef01)
		body[30] = byte(count)
		for i := 31; i < len(body); i++ {
			body[i] = byte(i)
		}
		got, err := DecodeOfficial249DieMonsterRequest(body)
		if err != nil {
			t.Fatal(err)
		}
		if got.RuntimeObjectKey != 403 || got.OwnerObjectKey != 27 || int(got.CombatEntryCount) != count || len(got.CombatEntries) != 14*count || len(got.RuntimeTail) != 43 || len(got.OpaqueTail) != 0 {
			t.Fatalf("count %d layout mismatch: %+v", count, got)
		}
		if got.OfficialCombatValue64 != ([2]uint64{0x123456789abcdef0, 0xfedcba9876543210}) || got.OfficialCombatValue32 != ([2]uint32{0x87654321, 0xabcdef01}) {
			t.Fatal("64-bit native values were truncated or shifted")
		}
		if !bytes.Equal(got.RuntimeTail, body[len(body)-43:]) {
			t.Fatal("runtime block/integrity footer boundary changed")
		}
		body[len(body)-1] ^= 0xff
		if got.RuntimeTail[42] == body[len(body)-1] {
			t.Fatal("decoded request aliases transport buffer")
		}
		for _, invalid := range [][]byte{body[:len(body)-1], append(append([]byte(nil), body...), 0)} {
			if _, err := DecodeOfficial249DieMonsterRequest(invalid); err == nil {
				t.Fatalf("accepted unknown boundary for count %d len %d", count, len(invalid))
			}
		}
	}
	for _, n := range []int{0, 61, 62, 66, 73} {
		if _, err := DecodeOfficial249DieMonsterRequest(make([]byte, n)); err == nil {
			t.Fatalf("accepted legacy/short length %d", n)
		}
	}
	invalidCount := make([]byte, 74+14*255)
	invalidCount[30] = 255
	if _, err := DecodeOfficial249DieMonsterRequest(invalidCount); err == nil {
		t.Fatal("accepted count 255 outside native cap")
	}
}
