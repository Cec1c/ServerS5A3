package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"
)

func TestLukeCatalogPreservesExistingRowsAndReplacesAvailability(t *testing.T) {
	for _, day := range []uint16{2421, 2422, 2423, 2424} {
		old, err := buildCurrentActivityEventInfoBodyWithDayDungeon(currentSpendTimeJoustCatalogGoldenRewards, 10800, false, true, true, true, true, day)
		if err != nil {
			t.Fatal(err)
		}
		include := day != 2423
		body, err := buildCurrentActivityEventInfoBodyWithLuke(currentSpendTimeJoustCatalogGoldenRewards, 10800, false, true, true, true, true, day, include)
		if err != nil {
			t.Fatal(err)
		}
		if !include {
			if !bytes.Equal(old, body) {
				t.Fatal("unavailable Luke changed original catalog")
			}
		} else {
			shape := parseCurrentSpendTimeEventInfoBody(t, old)
			activeOffset := 2
			for _, row := range shape.baseRows {
				activeOffset += len(row.raw)
			}
			want := append([]byte(nil), old[:activeOffset]...)
			binary.LittleEndian.PutUint16(want[:2], shape.baseCount+1)
			want = append(want, mustHexBytes(t, "1a0101000000000000000000000000")...)
			want = append(want, shape.activeCount+1)
			want = append(want, old[activeOffset+1:]...)
			want = append(want, append([]byte{0x1a, 0x01}, make([]byte, 48)...)...)
			if !bytes.Equal(want, body) {
				t.Fatal("Luke availability changed existing rows")
			}
		}
		shape := parseCurrentSpendTimeEventInfoBody(t, body)
		if shape.hasEvent(282) != include || shape.hasEvent(2449) {
			t.Fatal("unimplemented battlefield advertised")
		}
		writeAntonNativeCardFixture(t, fmt.Sprintf("catalog-luke-day-%d.bin", day), body)
	}
}

func TestRealPVFLukeNormalChannelRequiresItsOwnProgram(t *testing.T) {
	f, session, _ := normalLukeFixture(t)
	if !f.service.currentLukeNormalChannelAvailable(session) {
		t.Fatal("actual normal Luke unavailable")
	}
	f.service.options.currentClientVersion = "legacy"
	if f.service.currentLukeNormalChannelAvailable(session) {
		t.Fatal("unsupported client received Luke event")
	}
}
