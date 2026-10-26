package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func currentCrossSceneTestBodies(t *testing.T) ([]byte, []byte, uint16) {
	t.Helper()

	const (
		key     uint16 = 29
		channel byte   = 10
	)
	service := &Service{options: options{channelAdvertiseID: 4}}
	character := dnfrepo.CharacterRecord{
		CharacterID: "29",
		Name:        "cross-scene",
		Level:       60,
	}
	mode0 := buildCurrentSceneObjectListBodyWithCreatureAndAdventureNameInContext(
		key,
		character,
		true,
		character.Name,
		currentEquippedCreatureSnapshot{},
		"",
		channel,
		service.currentSceneActorRoute(),
	)
	reader := csharpLegacyUserInfoReader{service: service}
	mode3 := reader.buildCurrentUserInfoMode3InContext(character, true, key, 12, channel)

	if len(mode0) < currentCrossSceneMode0KeyOffset+2 {
		t.Fatalf("typed mode0 body is unexpectedly short: %d bytes", len(mode0))
	}
	if len(mode3) < currentCrossSceneMode3KeyOffset+2 {
		t.Fatalf("typed mode3 body is unexpectedly short: %d bytes", len(mode3))
	}
	return mode0, mode3, key
}

func TestBuildCurrentCrossSceneUserInfoBodyPreservesTypedRecords(t *testing.T) {
	mode0, mode3, key := currentCrossSceneTestBodies(t)
	mode0Before := append([]byte(nil), mode0...)
	mode3Before := append([]byte(nil), mode3...)

	body, err := buildCurrentCrossSceneUserInfoBody(mode0, mode3)
	if err != nil {
		t.Fatalf("build cross-scene body: %v", err)
	}
	if len(body) != len(mode0)+len(mode3)-currentCrossSceneSourcePrefixBytes {
		t.Fatalf(
			"cross-scene body length=%d, want %d",
			len(body),
			len(mode0)+len(mode3)-currentCrossSceneSourcePrefixBytes,
		)
	}
	if body[0] != currentCrossSceneMode8Subtype {
		t.Fatalf("cross-scene subtype=%d, want %d", body[0], currentCrossSceneMode8Subtype)
	}
	if got := binary.LittleEndian.Uint16(body[1:3]); got != currentCrossSceneMode8Count {
		t.Fatalf("cross-scene record count=%d, want %d", got, currentCrossSceneMode8Count)
	}
	mode0TailStart := 3
	mode0TailEnd := mode0TailStart + len(mode0) - currentCrossSceneSourcePrefixBytes
	if !bytes.Equal(body[mode0TailStart:mode0TailEnd], mode0[currentCrossSceneSourcePrefixBytes:]) {
		t.Fatal("cross-scene body rewrote the typed mode0 record")
	}
	if !bytes.Equal(body[mode0TailEnd:], mode3[currentCrossSceneSourcePrefixBytes:]) {
		t.Fatal("cross-scene body rewrote the typed mode3 record")
	}
	if got := binary.LittleEndian.Uint16(body[mode0TailStart+currentCrossSceneMode0KeyOffset-currentCrossSceneSourcePrefixBytes:]); got != key {
		t.Fatalf("embedded mode0 object key=%d, want %d", got, key)
	}
	if got := binary.LittleEndian.Uint16(body[mode0TailEnd+currentCrossSceneMode3KeyOffset-currentCrossSceneSourcePrefixBytes:]); got != key {
		t.Fatalf("embedded mode3 object key=%d, want %d", got, key)
	}
	if !bytes.Equal(mode0, mode0Before) || !bytes.Equal(mode3, mode3Before) {
		t.Fatal("cross-scene builder modified an input body")
	}

	body[3] ^= 0xff
	body[mode0TailEnd] ^= 0xff
	if !bytes.Equal(mode0, mode0Before) || !bytes.Equal(mode3, mode3Before) {
		t.Fatal("cross-scene output shares storage with an input body")
	}

	independent, err := buildCurrentCrossSceneUserInfoBody(mode0, mode3)
	if err != nil {
		t.Fatalf("rebuild cross-scene body: %v", err)
	}
	mode0[currentCrossSceneSourcePrefixBytes] ^= 0xff
	mode3[currentCrossSceneSourcePrefixBytes] ^= 0xff
	if independent[3] != mode0Before[currentCrossSceneSourcePrefixBytes] {
		t.Fatal("cross-scene output changed after mutating mode0 input")
	}
	if independent[mode0TailEnd] != mode3Before[currentCrossSceneSourcePrefixBytes] {
		t.Fatal("cross-scene output changed after mutating mode3 input")
	}
}

func TestBuildCurrentCrossSceneUserInfoBodyRejectsInvalidRecordPairs(t *testing.T) {
	baseMode0, baseMode3, key := currentCrossSceneTestBodies(t)

	setU16 := func(body []byte, offset int, value uint16) {
		binary.LittleEndian.PutUint16(body[offset:offset+2], value)
	}
	tests := []struct {
		name   string
		mutate func(mode0, mode3 []byte) ([]byte, []byte)
	}{
		{
			name: "mode0 subtype",
			mutate: func(mode0, mode3 []byte) ([]byte, []byte) {
				mode0[0] = 1
				return mode0, mode3
			},
		},
		{
			name: "mode3 subtype",
			mutate: func(mode0, mode3 []byte) ([]byte, []byte) {
				mode3[0] = 2
				return mode0, mode3
			},
		},
		{
			name: "mode0 count zero",
			mutate: func(mode0, mode3 []byte) ([]byte, []byte) {
				setU16(mode0, 1, 0)
				return mode0, mode3
			},
		},
		{
			name: "mode0 count two",
			mutate: func(mode0, mode3 []byte) ([]byte, []byte) {
				setU16(mode0, 1, 2)
				return mode0, mode3
			},
		},
		{
			name: "mode3 count zero",
			mutate: func(mode0, mode3 []byte) ([]byte, []byte) {
				setU16(mode3, 1, 0)
				return mode0, mode3
			},
		},
		{
			name: "mode3 count two",
			mutate: func(mode0, mode3 []byte) ([]byte, []byte) {
				setU16(mode3, 1, 2)
				return mode0, mode3
			},
		},
		{
			name: "different server",
			mutate: func(mode0, mode3 []byte) ([]byte, []byte) {
				mode3[3] = mode0[3] + 1
				return mode0, mode3
			},
		},
		{
			name: "different channel",
			mutate: func(mode0, mode3 []byte) ([]byte, []byte) {
				mode3[4] = mode0[4] + 1
				return mode0, mode3
			},
		},
		{
			name: "mode0 different object key",
			mutate: func(mode0, mode3 []byte) ([]byte, []byte) {
				setU16(mode0, currentCrossSceneMode0KeyOffset, key+1)
				return mode0, mode3
			},
		},
		{
			name: "mode3 different object key",
			mutate: func(mode0, mode3 []byte) ([]byte, []byte) {
				setU16(mode3, currentCrossSceneMode3KeyOffset, key+1)
				return mode0, mode3
			},
		},
		{
			name: "mode0 zero object key",
			mutate: func(mode0, mode3 []byte) ([]byte, []byte) {
				setU16(mode0, currentCrossSceneMode0KeyOffset, 0)
				setU16(mode3, currentCrossSceneMode3KeyOffset, 0)
				return mode0, mode3
			},
		},
		{
			name: "mode3 zero object key",
			mutate: func(mode0, mode3 []byte) ([]byte, []byte) {
				setU16(mode3, currentCrossSceneMode3KeyOffset, 0)
				return mode0, mode3
			},
		},
		{
			name: "nil mode0",
			mutate: func(mode0, mode3 []byte) ([]byte, []byte) {
				return nil, mode3
			},
		},
		{
			name: "nil mode3",
			mutate: func(mode0, mode3 []byte) ([]byte, []byte) {
				return mode0, nil
			},
		},
		{
			name: "truncated mode0 key",
			mutate: func(mode0, mode3 []byte) ([]byte, []byte) {
				return mode0[:currentCrossSceneMode0KeyOffset+1], mode3
			},
		},
		{
			name: "truncated mode3 key",
			mutate: func(mode0, mode3 []byte) ([]byte, []byte) {
				return mode0, mode3[:currentCrossSceneMode3KeyOffset+1]
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mode0 := append([]byte(nil), baseMode0...)
			mode3 := append([]byte(nil), baseMode3...)
			mode0, mode3 = tc.mutate(mode0, mode3)
			body, err := buildCurrentCrossSceneUserInfoBody(mode0, mode3)
			if err == nil {
				t.Fatalf("accepted invalid record pair, body=%x", body)
			}
			if body != nil {
				t.Fatalf("returned a body with the error: %x", body)
			}
		})
	}
}
