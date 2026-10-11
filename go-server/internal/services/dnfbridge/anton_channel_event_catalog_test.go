package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"
)

// Independent wire oracle: the verified native event ID is B5 00. The base
// row has value 1, two empty dstrs and no extension; the active row is 12 u32s.
// Preserve every existing row from the legacy fixture rather than obtaining
// expected bytes from the production Anton builder.
func withAntonChannelCatalogFixture(t *testing.T, legacy []byte) []byte {
	t.Helper()
	shape := parseCurrentSpendTimeEventInfoBody(t, legacy)
	activeOffset := 2
	for _, row := range shape.baseRows {
		activeOffset += len(row.raw)
	}
	base := mustHexBytes(t, "b50001000000000000000000000000")
	active := append([]byte{0xb5, 0x00}, make([]byte, 48)...)
	want := append([]byte(nil), legacy[:activeOffset]...)
	binary.LittleEndian.PutUint16(want[:2], shape.baseCount+1)
	want = append(want, base...)
	want = append(want, shape.activeCount+1)
	want = append(want, legacy[activeOffset+1:]...)
	return append(want, active...)
}

func TestAntonChannelEventCatalogPreservesOtherEventsAndNativeRows(t *testing.T) {
	for _, joust := range []bool{false, true} {
		for _, townNPC := range []bool{false, true} {
			t.Run(fmt.Sprintf("joust=%t/townNPC=%t", joust, townNPC), func(t *testing.T) {
				legacy, err := buildCurrentSpendTimeEventInfoBodyWithTownNpcEvent(
					currentSpendTimeJoustCatalogGoldenRewards, 10800, joust, townNPC)
				if err != nil {
					t.Fatal(err)
				}
				body, err := buildCurrentSpendTimeEventInfoBodyWithAntonChannelEvent(
					currentSpendTimeJoustCatalogGoldenRewards, 10800, joust, townNPC, true)
				if err != nil {
					t.Fatal(err)
				}
				want := withAntonChannelCatalogFixture(t, legacy)
				if !bytes.Equal(body, want) {
					t.Fatalf("Anton op108=%x want independent wire=%x", body, want)
				}
				shape := parseCurrentSpendTimeEventInfoBody(t, body)
				if shape.baseRows[len(shape.baseRows)-1].eventID != 181 ||
					shape.activeRows[len(shape.activeRows)-1].eventID != 181 ||
					shape.hasEvent(282) || shape.hasEvent(2449) {
					t.Fatalf("wrong raid flags: %+v", shape)
				}
				disabled, err := buildCurrentSpendTimeEventInfoBodyWithAntonChannelEvent(
					currentSpendTimeJoustCatalogGoldenRewards, 10800, joust, townNPC, false)
				if err != nil || !bytes.Equal(disabled, legacy) {
					t.Fatalf("disabled Anton altered legacy catalog: err=%v", err)
				}
			})
		}
	}
}
