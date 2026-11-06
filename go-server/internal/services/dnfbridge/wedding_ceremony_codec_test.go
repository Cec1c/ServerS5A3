package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/wedding"
)

func TestWeddingCeremonyCodecRouteFieldsMatchNativeChannelLabel(t *testing.T) {
	b := buildCurrentWeddingCeremony(1234, wedding.CeremonyRoute{Channel: 210, Area: 8}, 0x12345678, 2, []byte("groom"), []byte("bride"), 30)
	if len(b) != 79 || binary.LittleEndian.Uint32(b) != 1234 || binary.LittleEndian.Uint32(b[4:]) != 8 || binary.LittleEndian.Uint32(b[12:]) != 210 || binary.LittleEndian.Uint32(b[8:]) != 0x12345678 || b[16] != 2 || b[46] != 0 || b[76] != 0 || binary.LittleEndian.Uint16(b[77:]) != 30 {
		t.Fatalf("ceremony %x", b)
	}
	if !bytes.Equal(b[17:22], []byte("groom")) || !bytes.Equal(b[47:52], []byte("bride")) {
		t.Fatalf("names %x", b)
	}
	begin := buildCurrentWeddingCeremonyBegin(1234, 8, []byte("groom"), []byte("bride"))
	if len(begin) != 68 || binary.LittleEndian.Uint32(begin) != 1234 || binary.LittleEndian.Uint32(begin[4:]) != 8 || !bytes.Equal(begin[8:13], []byte("groom")) || !bytes.Equal(begin[38:43], []byte("bride")) {
		t.Fatalf("begin %x", begin)
	}
}

func TestWeddingCeremonyEntryRejectsPartialTrailingAndWrongRoute(t *testing.T) {
	b := bytes.Repeat([]byte{0xa5}, 22)
	binary.LittleEndian.PutUint32(b[13:], 1234)
	binary.LittleEndian.PutUint32(b[17:], 8)
	b[21] = 0
	q, ok := decodeCurrentWeddingCeremonyEntry(b)
	if !ok || q.Partner != 1234 || q.Area != 8 || q.Guest {
		t.Fatalf("entry %+v %v", q, ok)
	}
	for _, size := range []int{0, 13, 21, 23} {
		raw := append(append([]byte(nil), b...), 0)
		if _, ok = decodeCurrentWeddingCeremonyEntry(raw[:size]); ok {
			t.Fatalf("accepted size %d", size)
		}
	}
	binary.LittleEndian.PutUint32(b[17:], 210)
	if _, ok = decodeCurrentWeddingCeremonyEntry(b); ok {
		t.Fatal("accepted channel as area")
	}
	binary.LittleEndian.PutUint32(b[17:], 8)
	// Native carries an opaque u32 relationship/run key, not a u16 character.
	for _, token := range []uint32{65536, 0xffffffff} {
		binary.LittleEndian.PutUint32(b[13:], token)
		for _, guest := range []byte{0, 1} {
			b[21] = guest
			q, ok = decodeCurrentWeddingCeremonyEntry(b)
			if !ok || q.Partner != token || q.Area != 8 || q.Guest != (guest == 1) {
				t.Fatalf("opaque key truncated/rejected: %+v %v", q, ok)
			}
		}
	}
	b[21] = 2
	if _, ok = decodeCurrentWeddingCeremonyEntry(b); ok {
		t.Fatal("accepted invalid host/guest flag")
	}
	b[21] = 0
	binary.LittleEndian.PutUint32(b[13:], 0)
	if _, ok = decodeCurrentWeddingCeremonyEntry(b); ok {
		t.Fatal("accepted zero relationship key")
	}
}
