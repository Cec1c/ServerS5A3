package dnfbridge

import (
	"encoding/binary"

	"longheng.io/server/internal/modules/dnf/wedding"
)

const (
	currentWeddingCeremonyNotice      uint16 = 1004
	currentWeddingCeremonyBeginNotice uint16 = 1007
	currentWeddingEnterCeremonyOpcode uint16 = 1039
)

type currentWeddingCeremonyEntry struct {
	Partner uint32
	Area    uint32
	Guest   bool
}

// Current EXE 1AD9890 appends 22B. The first 13B are opaque transport
// storage. The tail carries this+4, this+0C and host/guest mode.
func decodeCurrentWeddingCeremonyEntry(body []byte) (currentWeddingCeremonyEntry, bool) {
	if len(body) != 22 || body[21] > 1 {
		return currentWeddingCeremonyEntry{}, false
	}
	r := currentWeddingCeremonyEntry{Partner: binary.LittleEndian.Uint32(body[13:]), Area: binary.LittleEndian.Uint32(body[17:]), Guest: body[21] != 0}
	return r, r.Partner > 0 && r.Area >= 8 && r.Area <= 38
}

// NOTI1004 is 79B (1AD9E20 -> 1AD96A0). Fields 4 and 12 must not be
// transposed: raw4=Church area, raw12=channel. NOTI1005 copies raw12 into
// LetterRecord+4 at 1ADB9D9; 1AD0899 uses that field for DSTR72922's channel
// label. 1ACC52B passes LetterRecord+8/raw4 to 1AD9890's enter-area argument.
// The leading character must match the recipient's persisted partner (this+60).
func buildCurrentWeddingCeremony(partner uint32, route wedding.CeremonyRoute, at uint32, kind byte, firstName, secondName []byte, letters uint16) []byte {
	b := make([]byte, 79)
	binary.LittleEndian.PutUint32(b, partner)
	binary.LittleEndian.PutUint32(b[4:], uint32(route.Area))
	binary.LittleEndian.PutUint32(b[8:], at)
	binary.LittleEndian.PutUint32(b[12:], uint32(route.Channel))
	b[16] = kind
	copy(b[17:46], firstName)
	copy(b[47:76], secondName)
	binary.LittleEndian.PutUint16(b[77:], letters)
	return b
}

// NOTI1007 (1ADC8A0) reads two u32 and two 30B names. 1ADC630 checks Town39
// and compares the second u32 to the current area before playing the native
// cinema object. The first key is cleared by 1ADB640 when it matches this+4.
func buildCurrentWeddingCeremonyBegin(partner uint32, area byte, firstName, secondName []byte) []byte {
	b := make([]byte, 68)
	binary.LittleEndian.PutUint32(b, partner)
	binary.LittleEndian.PutUint32(b[4:], uint32(area))
	copy(b[8:37], firstName)
	copy(b[38:67], secondName)
	return b
}
