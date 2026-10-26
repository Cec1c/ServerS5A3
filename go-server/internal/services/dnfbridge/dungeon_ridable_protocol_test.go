package dnfbridge

import (
	"bytes"
	"errors"
	"strconv"
	"testing"
)

func TestDecodeCurrentDungeonRidableRequestNativeSamples(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want currentDungeonRidableRequest
	}{
		{
			name: "native mount",
			body: []byte{0x02, 0x33, 0xef, 0x00, 0x00, 0x02, 0x03},
			want: currentDungeonRidableRequest{
				State:            currentDungeonRidableStateMounted,
				DefinitionKeyRaw: 61235,
				RoomX:            2,
				RoomY:            3,
			},
		},
		{
			name: "release",
			body: []byte{0x01, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00},
			want: currentDungeonRidableRequest{
				State:            currentDungeonRidableStateReleased,
				DefinitionKeyRaw: 1,
			},
		},
		{
			name: "destroy with high key and missing room",
			body: []byte{0x03, 0x78, 0x56, 0x34, 0x12, 0xff, 0xff},
			want: currentDungeonRidableRequest{
				State:            currentDungeonRidableStateDestroyed,
				DefinitionKeyRaw: 0x12345678,
				RoomX:            0xff,
				RoomY:            0xff,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeCurrentDungeonRidableRequest(test.body)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("request=%+v want=%+v", got, test.want)
			}
		})
	}
}

func TestDecodeCurrentDungeonRidableRequestRejectsNonSevenByteBodies(t *testing.T) {
	for length := 0; length < currentDungeonRidableRequestLength; length++ {
		t.Run("short_"+strconv.Itoa(length), func(t *testing.T) {
			body := bytes.Repeat([]byte{0x02}, length)
			_, err := decodeCurrentDungeonRidableRequest(body)
			if !errors.Is(err, errDungeonRidableRequestLength) {
				t.Fatalf("length=%d error=%v", length, err)
			}
		})
	}

	oldFiveByteBody := []byte{0x02, 0x33, 0xef, 0x00, 0x00}
	if _, err := decodeCurrentDungeonRidableRequest(oldFiveByteBody); !errors.Is(err, errDungeonRidableRequestLength) {
		t.Fatalf("old 5-byte body error=%v", err)
	}

	eightByteBody := []byte{0x02, 0x33, 0xef, 0x00, 0x00, 0x02, 0x03, 0x00}
	if _, err := decodeCurrentDungeonRidableRequest(eightByteBody); !errors.Is(err, errDungeonRidableRequestLength) {
		t.Fatalf("8-byte body with trailing data error=%v", err)
	}
}

func TestDecodeCurrentDungeonRidableRequestRejectsUnknownStates(t *testing.T) {
	for _, state := range []byte{0, 4, 0xff} {
		body := []byte{state, 0x33, 0xef, 0x00, 0x00, 0x02, 0x03}
		_, err := decodeCurrentDungeonRidableRequest(body)
		if !errors.Is(err, errDungeonRidableRequestState) {
			t.Fatalf("state=%d error=%v", state, err)
		}
	}
}

func TestBuildCurrentDungeonRidableNotificationMatchesStartMapRow(t *testing.T) {
	entry := currentDungeonStartMapRidableEntry{
		PositionX:  123,
		PositionY:  456,
		ObjectCode: 61235,
		Faction:    7,
		State:      0,
	}
	body := buildCurrentDungeonRidableNotification(entry)
	if len(body) != currentDungeonRidableNotificationLength {
		t.Fatalf("notification length=%d want=%d body=%x", len(body), currentDungeonRidableNotificationLength, body)
	}
	want := []byte{
		0x7b, 0x00, 0x00, 0x00,
		0xc8, 0x01, 0x00, 0x00,
		0x33, 0xef, 0x00, 0x00,
		0x07, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
	}
	if !bytes.Equal(body, want) {
		t.Fatalf("notification=%x want=%x", body, want)
	}

	startMap, err := (currentDungeonStartMap{
		MapID:            1,
		RidableGroups:    [][]currentDungeonStartMapRidableEntry{{entry}},
		PartyMemberIndex: 0xff,
	}).Build()
	if err != nil {
		t.Fatal(err)
	}
	row := startMap[len(startMap)-21 : len(startMap)-1]
	if !bytes.Equal(body, row) {
		t.Fatalf("NOTI296 body=%x START_MAP row=%x full=%x", body, row, startMap)
	}
}
