package dnfbridge

import (
	"encoding/binary"
	"testing"
)

func TestParticularHellInitMatchesNative264ByteReader(t *testing.T) {
	body := buildCurrentParticularHellInfoBody(2)
	if len(body) != 264 || binary.LittleEndian.Uint32(body) != 3 || binary.LittleEndian.Uint32(body[4:]) != 0 || binary.LittleEndian.Uint32(body[8:]) != 316 || binary.LittleEndian.Uint32(body[12:]) != 2 {
		t.Fatalf("native init layout: %x", body)
	}
	for _, v := range body[16:] {
		if v != 0 {
			t.Fatal("unconfigured dungeon counter invented")
		}
	}
}
