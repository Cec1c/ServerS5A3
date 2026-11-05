package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func wornAvatarFramingEquipment(t *testing.T, slot int16, itemID int64, socketData, colorData []byte) currentItemListEntry {
	t.Helper()
	extra := make(map[string]string)
	if slot < 12 {
		extra["item_kind"] = "avatar"
	} else {
		extra["current_exe_equipment_type"] = strconv.Itoa(int(slot))
	}
	if len(socketData) != 0 {
		extra["avatar_socket_data"] = hex.EncodeToString(socketData)
	}
	if len(colorData) != 0 {
		extra["avatar_color_data"] = hex.EncodeToString(colorData)
	}
	entry, ok := currentItemListEntryFromEquipment(dnfrepo.EquipmentEntry{
		SlotIndex: slot,
		ItemID:    itemID,
		Extra:     extra,
	})
	if !ok {
		t.Fatalf("actor slot %d equipment was not projected", slot)
	}
	return entry
}

func TestCurrentItemListEntryWornAvatarMarkerUsesActorSlotZeroThroughEleven(t *testing.T) {
	for slot := int16(0); slot <= 32; slot++ {
		entry := wornAvatarFramingEquipment(t, slot, 415580009, nil, nil)
		want := slot <= 11
		if entry.list3WornAvatar != want {
			t.Fatalf("actor slot %d list-3 worn-avatar marker=%t, want %t", slot, entry.list3WornAvatar, want)
		}
	}
}

func TestList3WornAvatarOptionalBlobFramingMatchesNativeGate(t *testing.T) {
	socketA := make([]byte, currentAvatarSocketBytes)
	copy(socketA, []byte{0xEF, 0xFF, 0, 0, 0, 0, 0xEF, 0xFF, 0, 0, 0, 0})
	socketB := make([]byte, currentAvatarSocketBytes)
	copy(socketB, []byte{0x01, 0, 0, 0, 0, 0, 0x04, 0, 0, 0, 0, 0})
	colorA := []byte{0x11, 0x22, 0x33, 0x44}
	colorB := []byte{0xA1, 0xA2, 0xA3, 0xA4, 0xA5}

	avatarA := wornAvatarFramingEquipment(t, 0, 415580101, socketA, colorA)
	avatarB := wornAvatarFramingEquipment(t, 1, 415580102, socketB, colorB)
	// The weapon deliberately carries stored socket/color bytes too. Its actor
	// slot, rather than item ID or payload presence, must keep both blobs off.
	weapon := wornAvatarFramingEquipment(t, 12, 101040493, socketA, colorA)
	if !avatarA.list3WornAvatar || !avatarB.list3WornAvatar || weapon.list3WornAvatar {
		t.Fatalf("markers avatarA=%t avatarB=%t weapon=%t", avatarA.list3WornAvatar, avatarB.list3WornAvatar, weapon.list3WornAvatar)
	}

	type expectedRow struct {
		entry       currentItemListEntry
		avatarBlobs bool
	}
	checkBody := func(label string, body []byte, rows []expectedRow) {
		t.Helper()
		if len(body) < 3 || body[0] != currentSocketListEquipment {
			t.Fatalf("%s header=%x", label, body)
		}
		if got := binary.LittleEndian.Uint16(body[1:3]); got != uint16(len(rows)) {
			t.Fatalf("%s row count=%d want=%d", label, got, len(rows))
		}
		cursor := 3
		for rowIndex, want := range rows {
			rowEnd := cursor + currentItemListEntryWireSize
			if rowEnd > len(body) {
				t.Fatalf("%s row %d truncated at cursor %d", label, rowIndex, cursor)
			}
			if !bytes.Equal(body[cursor:rowEnd], want.entry.data[:]) {
				t.Fatalf("%s row %d 0x77 bytes changed at cursor %d", label, rowIndex, cursor)
			}
			cursor = rowEnd
			if !want.avatarBlobs {
				continue
			}
			for _, blob := range []struct {
				name string
				data []byte
			}{
				{name: "socket", data: want.entry.avatarSocketData},
				{name: "color", data: want.entry.avatarColorData},
			} {
				if len(blob.data) == 0 {
					t.Fatalf("%s row %d expected non-empty %s payload", label, rowIndex, blob.name)
				}
				if cursor+4 > len(body) {
					t.Fatalf("%s row %d %s length cursor %d is truncated", label, rowIndex, blob.name, cursor)
				}
				length := int(binary.LittleEndian.Uint32(body[cursor : cursor+4]))
				cursor += 4
				if length != len(blob.data) || cursor+length > len(body) {
					t.Fatalf("%s row %d %s length=%d want=%d at cursor %d", label, rowIndex, blob.name, length, len(blob.data), cursor)
				}
				if !bytes.Equal(body[cursor:cursor+length], blob.data) {
					t.Fatalf("%s row %d %s payload=%x want=%x", label, rowIndex, blob.name, body[cursor:cursor+length], blob.data)
				}
				cursor += length
			}
		}
		if cursor != len(body) {
			t.Fatalf("%s cursor=%d body=%d; unexpected trailing framing", label, cursor, len(body))
		}
	}

	rows := []expectedRow{
		{entry: avatarA, avatarBlobs: true},
		{entry: avatarB, avatarBlobs: true},
		{entry: weapon, avatarBlobs: false},
	}
	page := buildCurrentItemListBody(3, []currentItemListEntry{avatarA, avatarB, weapon}, dnfrepo.CharacterContainerState{})
	checkBody("mixed list-3 page", page, rows)

	for _, avatar := range []currentItemListEntry{avatarA, avatarB} {
		update := buildCurrentItemUpdateBody(3, []currentItemListEntry{avatar})
		checkBody("single worn-avatar update", update, []expectedRow{{entry: avatar, avatarBlobs: true}})
	}
	weaponUpdate := buildCurrentItemUpdateBody(3, []currentItemListEntry{weapon})
	checkBody("single ordinary-weapon update", weaponUpdate, []expectedRow{{entry: weapon, avatarBlobs: false}})

	checkBody("empty list-3 page", buildCurrentItemListBody(3, nil, dnfrepo.CharacterContainerState{}), nil)
	checkBody("empty list-3 update", buildCurrentItemUpdateBody(3, nil), nil)
}
