package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	dungeonPeerActorStateTestNameLengthOffset = 5 + 0x47 + 2
	dungeonPeerActorStateTestNameOffset       = dungeonPeerActorStateTestNameLengthOffset + 4
	dungeonPeerActorStateTestStateOffset      = 5
)

func dungeonPeerActorStateTestBody(name string, objectKey uint16) []byte {
	return buildCurrentSceneObjectListBody(objectKey, dnfrepo.CharacterRecord{Name: name, Level: 90}, true, name)
}

func dungeonPeerActorStateTestTargetOffset(t *testing.T, body []byte) int {
	t.Helper()
	if len(body) < dungeonPeerActorStateTestNameOffset {
		t.Fatalf("mode0 fixture is shorter than its name length: %d", len(body))
	}
	nameLength := int(binary.LittleEndian.Uint32(body[dungeonPeerActorStateTestNameLengthOffset:dungeonPeerActorStateTestNameOffset]))
	target := dungeonPeerActorStateTestNameOffset + nameLength + dungeonPeerActorStateTestStateOffset
	if target >= len(body) {
		t.Fatalf("mode0 fixture state offset %d is outside body length %d", target, len(body))
	}
	return target
}

func TestProjectCurrentDungeonPeerActorStateClonesAndOnlyChangesState(t *testing.T) {
	const objectKey = uint16(17)
	for _, tc := range []struct {
		label string
		name  string
	}{
		{label: "empty", name: ""},
		{label: "one-byte", name: "a"},
		{label: "ascii", name: "peer"},
		{label: "gb18030", name: "城镇队友"},
		{label: "maximum", name: strings.Repeat("n", rosterNameMaxBytes)},
	} {
		t.Run(tc.label, func(t *testing.T) {
			body := dungeonPeerActorStateTestBody(tc.name, objectKey)
			body[3] = 1 // Nonzero advertised server used by the live deployment.
			stateOffset := dungeonPeerActorStateTestTargetOffset(t, body)
			before := append([]byte(nil), body...)

			projected, err := projectCurrentDungeonPeerActorState(body, objectKey)
			if err != nil {
				t.Fatalf("project mode0 state: %v", err)
			}
			if !bytes.Equal(body, before) {
				t.Fatal("projection modified the input body")
			}
			if &projected[0] == &body[0] {
				t.Fatal("projection returned a slice backed by the input body")
			}

			want := append([]byte(nil), before...)
			want[stateOffset] = 1
			if !bytes.Equal(projected, want) {
				t.Fatal("projection changed bytes other than the actor state")
			}

			projected[stateOffset] = 0xff
			if !bytes.Equal(body, before) {
				t.Fatal("mutating the projection changed the input body")
			}
		})
	}
}

func TestProjectCurrentDungeonPeerActorStateTailBoundary(t *testing.T) {
	const objectKey = uint16(17)
	valid := dungeonPeerActorStateTestBody("peer", objectKey)
	nameLength := int(binary.LittleEndian.Uint32(valid[dungeonPeerActorStateTestNameLengthOffset:dungeonPeerActorStateTestNameOffset]))
	exactTail := dungeonPeerActorStateTestNameOffset + nameLength + dungeonPeerActorStateTestStateOffset + 1

	t.Run("exactly six tail bytes", func(t *testing.T) {
		body := append([]byte(nil), valid[:exactTail]...)
		before := append([]byte(nil), body...)
		projected, err := projectCurrentDungeonPeerActorState(body, objectKey)
		if err != nil {
			t.Fatalf("project six-byte tail: %v", err)
		}
		if !bytes.Equal(body, before) {
			t.Fatal("six-byte projection modified its input")
		}
		want := append([]byte(nil), before...)
		want[len(want)-1] = 1
		if !bytes.Equal(projected, want) {
			t.Fatal("six-byte projection changed unexpected bytes")
		}
	})

	t.Run("five tail bytes", func(t *testing.T) {
		body := append([]byte(nil), valid[:exactTail-1]...)
		before := append([]byte(nil), body...)
		if projected, err := projectCurrentDungeonPeerActorState(body, objectKey); err == nil || projected != nil {
			t.Fatalf("five-byte tail succeeded with %d bytes and error %v", len(projected), err)
		}
		if !bytes.Equal(body, before) {
			t.Fatal("failed five-byte projection modified its input")
		}
	})
}

func TestProjectCurrentDungeonPeerActorStateRejectsInvalidBodies(t *testing.T) {
	const objectKey = uint16(17)
	valid := dungeonPeerActorStateTestBody("peer", objectKey)

	for _, tc := range []struct {
		name string
		body func() []byte
		key  uint16
	}{
		{
			name: "empty body",
			body: func() []byte { return nil },
			key:  objectKey,
		},
		{
			name: "truncated name length",
			body: func() []byte { return append([]byte(nil), valid[:dungeonPeerActorStateTestNameOffset-1]...) },
			key:  objectKey,
		},
		{
			name: "wrong mode",
			body: func() []byte {
				body := append([]byte(nil), valid...)
				body[0] = currentCrossSceneMode8Subtype
				return body
			},
			key: objectKey,
		},
		{
			name: "wrong route",
			body: func() []byte {
				body := append([]byte(nil), valid...)
				body[3] = 250
				return body
			},
			key: objectKey,
		},
		{
			name: "zero actors",
			body: func() []byte {
				body := append([]byte(nil), valid...)
				binary.LittleEndian.PutUint16(body[1:3], 0)
				return body
			},
			key: objectKey,
		},
		{
			name: "multiple actors",
			body: func() []byte {
				body := append([]byte(nil), valid...)
				binary.LittleEndian.PutUint16(body[1:3], 2)
				return body
			},
			key: objectKey,
		},
		{
			name: "wrong body key",
			body: func() []byte {
				body := append([]byte(nil), valid...)
				binary.LittleEndian.PutUint16(body[0x4c:0x4e], objectKey+1)
				return body
			},
			key: objectKey,
		},
		{
			name: "wrong requested key",
			body: func() []byte { return append([]byte(nil), valid...) },
			key:  objectKey + 1,
		},
		{
			name: "overlong name",
			body: func() []byte {
				body := append([]byte(nil), valid...)
				binary.LittleEndian.PutUint32(
					body[dungeonPeerActorStateTestNameLengthOffset:dungeonPeerActorStateTestNameOffset],
					rosterNameMaxBytes+1,
				)
				return body
			},
			key: objectKey,
		},
		{
			name: "maximum name length tail truncated",
			body: func() []byte {
				body := append([]byte(nil), valid...)
				binary.LittleEndian.PutUint32(
					body[dungeonPeerActorStateTestNameLengthOffset:dungeonPeerActorStateTestNameOffset],
					rosterNameMaxBytes,
				)
				end := dungeonPeerActorStateTestNameOffset + rosterNameMaxBytes + dungeonPeerActorStateTestStateOffset
				return body[:end]
			},
			key: objectKey,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body()
			before := append([]byte(nil), body...)
			projected, err := projectCurrentDungeonPeerActorState(body, tc.key)
			if err == nil || projected != nil {
				t.Fatalf("invalid mode0 succeeded with %d bytes and error %v", len(projected), err)
			}
			if !bytes.Equal(body, before) {
				t.Fatal("failed projection modified its input")
			}
		})
	}
}
