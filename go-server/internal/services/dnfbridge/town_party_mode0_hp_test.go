package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The pinned native mode0 reader consumes v114 at 93+nameBytes+19*rows
// for these zero-expansion equipment fixtures. This intentionally checks the
// wire offset independently of the production variable-row boundary helper.
func townMode0FixtureHPOffset(t *testing.T, body []byte) int {
	t.Helper()
	const nameOffset = 0x4e
	if len(body) < nameOffset+4 {
		t.Fatal("mode0 name length missing")
	}
	nameBytes := int(binary.LittleEndian.Uint32(body[nameOffset : nameOffset+4]))
	start := nameOffset + 4 + nameBytes
	if start+6 >= len(body) {
		t.Fatal("mode0 equipment count missing")
	}
	rows := int(body[start+6])
	for i := 0; i < rows; i++ {
		row := start + 7 + i*19
		if row+19 > len(body) || binary.LittleEndian.Uint32(body[row+5:row+9]) != 0 {
			t.Fatal("fixture requires zero-expansion native equipment rows")
		}
	}
	offset := 93 + nameBytes + 19*rows
	if offset >= len(body) {
		t.Fatal("mode0 v114 missing")
	}
	return offset
}

func requireTownMode0InitialHP(t *testing.T, mode0 []byte, want byte) {
	t.Helper()
	if got := mode0[townMode0FixtureHPOffset(t, mode0)]; got != want {
		t.Fatalf("initial mode0 HP=%d, subsequent op153 HP=%d", got, want)
	}
}

func TestTownMode0HPUsesExistingProjectionWithoutOtherChanges(t *testing.T) {
	for _, name := range []string{"peer", "城镇队友"} {
		for _, hp := range []byte{0, 1, 37, 99, 100} {
			t.Run(fmt.Sprintf("%s/%d", name, hp), func(t *testing.T) {
				body := buildCurrentSceneObjectListBody(17, dnfrepo.CharacterRecord{Name: name, Level: 90}, true, name)
				original := append([]byte(nil), body...)
				wire := []byte{2, 29, 0, 63, 0, 0, 17, 0, hp, 0, 1}
				if err := projectCurrentTownPartyHPIntoMode0(body, wire, 17); err != nil {
					t.Fatal(err)
				}
				requireTownMode0InitialHP(t, body, hp)
				expected := append([]byte(nil), original...)
				expected[townMode0FixtureHPOffset(t, original)] = hp
				if !bytes.Equal(body, expected) {
					t.Fatal("HP projection changed other mode0 bytes")
				}
				// A clone appearance replay reuses the same body; applying the
				// same snapshot again must retain the exact HP, including zero.
				if err := projectCurrentTownPartyHPIntoMode0(body, wire, 17); err != nil || !bytes.Equal(body, expected) {
					t.Fatalf("repeat projection changed bytes: %v", err)
				}
			})
		}
	}
}

func TestTownMode0HPRejectsInvalidIdentityAndTruncation(t *testing.T) {
	valid := buildCurrentSceneObjectListBody(17, dnfrepo.CharacterRecord{Name: "peer", Level: 90}, true, "peer")
	wire := []byte{2, 29, 0, 63, 0, 0, 17, 0, 37, 0, 1}
	for _, tc := range []struct {
		name string
		edit func([]byte, []byte) ([]byte, []byte)
	}{
		{"wrong key", func(b, w []byte) ([]byte, []byte) { b[0x4c] = 18; return b, w }},
		{"wrong mode", func(b, w []byte) ([]byte, []byte) { b[0] = 1; return b, w }},
		{"multiple actors", func(b, w []byte) ([]byte, []byte) { b[1] = 2; return b, w }},
		{"short mode0", func(b, w []byte) ([]byte, []byte) { return b[:0x4d], w }},
		{"short HP", func(b, w []byte) ([]byte, []byte) { return b[:townMode0FixtureHPOffset(t, b)], w }},
		{"short roster", func(b, w []byte) ([]byte, []byte) { return b, w[:len(w)-1] }},
		{"missing member", func(b, w []byte) ([]byte, []byte) { w[6] = 18; return b, w }},
		{"duplicate member", func(b, w []byte) ([]byte, []byte) { w[1] = 17; return b, w }},
		{"invalid percent", func(b, w []byte) ([]byte, []byte) { w[8] = 101; return b, w }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, w := tc.edit(append([]byte(nil), valid...), append([]byte(nil), wire...))
			before := append([]byte(nil), b...)
			if err := projectCurrentTownPartyHPIntoMode0(b, w, 17); err == nil || !bytes.Equal(b, before) {
				t.Fatalf("invalid projection changed body or succeeded: %v", err)
			}
		})
	}
	before := append([]byte(nil), valid...)
	if err := projectCurrentTownPartyHPIntoMode0(valid, nil, 17); err != nil || !bytes.Equal(valid, before) {
		t.Fatalf("absent current roster changed mode0: %v", err)
	}
}
