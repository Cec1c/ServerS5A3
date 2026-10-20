package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// currentDungeonAuthoritativeDropEntrySize is the DNF90 client's op38
// (NOTI DIE_MONSTER) drop-entry width, written here as an independent literal so
// that a future width change cannot silently satisfy the offset assertions below.
//
// Body = u16 monster sequence | u8 drop count | count x entry | fixed 4-byte tail.
// Entry (127 B = 4 + 0x77 + 2 + 2), body offsets of the first entry:
//
//	+3   u32 scene object key (the client echoes this verbatim in its op43 GET_ITEM)
//	+7   u16 scene slot                   (0x77 item-list row +0x00)
//	+9   u32 item id                      (row +0x02; 0 = gold)
//	+13  u32 value                        (row +0x06; equipment = grade/instance seed,
//	                                       stackable = count; row +0x0A = upgrade byte)
//	+18  u16 durability                   (row +0x0B, written only when non-zero)
//	+126 u16 entry constructor sentinel   (0xFFFF)
//	+128 u16 owner actor object key
//	+130 00 00 FF 00 fixed body tail (zero-drop body is exactly 7 B)
//
// Two independent sources agree on this shape:
//
//  1. Live wire (2026-09-28, 109.244.63.71 /opt/dnf90/logs/packet_log.txt). Every
//     op38 body ever logged is 7 + n*127: 473x7, 85x134(n=1), 6x261(n=2), 2x388(n=3),
//     1x896(n=7), 1x1023(n=8), 1x1277(n=10), 1x1404(n=11). 77 op43 pickups were
//     accepted against the exact u32 keys those rows carried (gold and equipment
//     alike, item_update_body_len = 3 + 119), so the client parses this layout.
//
//  2. The A21 vendor reference (DungeonNotificationBuilder.cs:256-295) uses a
//     *48-byte* entry for a different client build (S4A21_CN DNF.exe 28,398,592 B
//     vs our 82,620,416 B). Its item-id offset (body +5) and value offset (body
//     +10) are NOT ours. Do not "fix" this encoder to the A21 widths: the row
//     below is the one our own client has been picking items up from.
const currentDungeonAuthoritativeDropEntrySize = 127

// TestCurrentDungeonDeathDropEntryGoldenMatchesLiveClientWire pins one complete
// one-drop op38 body against bytes assembled by hand from the layout above - not
// from the encoder's own output - so any width or offset drift fails loudly.
func TestCurrentDungeonDeathDropEntryGoldenMatchesLiveClientWire(t *testing.T) {
	const (
		sequenceID = uint32(402)
		objectKey  = uint32(406)
		sceneSlot  = uint16(1)
		itemID     = uint32(3326)
		amount     = uint32(1)
		sentinel   = uint16(0xFFFF)
		owner      = uint16(3)
	)
	itemState, err := currentDungeonDeathDropItemState(
		sceneSlot,
		dungeonDropItemDefinition{ItemID: itemID, Kind: dungeonDropItemStackable},
		amount,
	)
	if err != nil {
		t.Fatal(err)
	}
	body, err := buildCurrentDungeonDeathNotificationBodyWithDrops(
		sequenceID,
		currentDungeonDeathResponseMonster,
		[]currentDungeonDeathDropWire{{
			SceneObjectKey:      objectKey,
			Item:                itemState,
			UnknownTailSentinel: sentinel,
			OwnerActorObjectKey: owner,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}

	// Header: u16 sequence + u8 count.
	head := []byte{
		0x92, 0x01, // +0 sequence 402
		0x01, // +2 drop count
	}
	if got := body[:len(head)]; !bytes.Equal(got, head) {
		t.Fatalf("header=% x want=% x", got, head)
	}
	// Entry prefix: u32 key, u16 slot, u32 item id, u32 value.
	entry := []byte{
		0x96, 0x01, 0x00, 0x00, // +3..+6  scene object key 406
		0x01, 0x00, // +7..+8  scene slot 1
		0xfe, 0x0c, 0x00, 0x00, // +9..+12 item id 3326
		0x01, 0x00, 0x00, 0x00, // +13..+16 value = count 1
	}
	if got := body[3 : 3+len(entry)]; !bytes.Equal(got, entry) {
		t.Fatalf("entry prefix=% x want=% x", got, entry)
	}
	// The rest of the 0x77 row stays zero for a plain stackable drop: durability,
	// the +0x0A upgrade byte, amplify fields and the expiration dword are only
	// written when non-zero.
	if got := body[3+len(entry) : 3+currentDungeonAuthoritativeDropEntrySize-4]; !bytes.Equal(got, make([]byte, len(got))) {
		t.Fatalf("entry body=% x want all zero", got)
	}
	// Entry tail: u16 sentinel + u16 owner.
	tail := []byte{
		0xff, 0xff, // +126..+127 sentinel
		0x03, 0x00, // +128..+129 owner actor object key
		0x00, 0x00, 0xff, 0x00, // +130..+133 fixed body tail
	}
	if got := body[3+currentDungeonAuthoritativeDropEntrySize-4:]; !bytes.Equal(got, tail) {
		t.Fatalf("entry tail=% x want=% x", got, tail)
	}

	if want := 7 + currentDungeonAuthoritativeDropEntrySize; len(body) != want {
		t.Fatalf("body len=%d want=%d", len(body), want)
	}
	// Negative golden: the A21 reference offsets must NOT hold here.
	if got := binary.LittleEndian.Uint32(body[5:9]); got == itemID {
		t.Fatalf("item id leaked into the A21 offset body[5:9]=%d", got)
	}
}

// TestCurrentDungeonDeathDropBodyLengthsFollowTheLiveDropEntryStride pins the
// length law measured on the live wire (7 + n*127) and records explicitly that the
// A21 law (7 + n*48) is a different client's shape.
func TestCurrentDungeonDeathDropBodyLengthsFollowTheLiveDropEntryStride(t *testing.T) {
	for count := 0; count <= 4; count++ {
		drops := make([]currentDungeonDeathDropWire, 0, count)
		for index := 0; index < count; index++ {
			state, err := currentDungeonDeathDropItemState(
				uint16(index+1),
				dungeonDropItemDefinition{ItemID: uint32(9001 + index), Kind: dungeonDropItemStackable},
				1,
			)
			if err != nil {
				t.Fatal(err)
			}
			drops = append(drops, currentDungeonDeathDropWire{
				SceneObjectKey:      uint32(400 + index),
				Item:                state,
				UnknownTailSentinel: 0xFFFF,
				OwnerActorObjectKey: 3,
			})
		}
		body, err := buildCurrentDungeonDeathNotificationBodyWithDrops(
			402,
			currentDungeonDeathResponseMonster,
			drops,
		)
		if err != nil {
			t.Fatal(err)
		}
		if want := 7 + count*currentDungeonAuthoritativeDropEntrySize; len(body) != want {
			t.Fatalf("count=%d body len=%d want=%d", count, len(body), want)
		}
		if got := int(body[2]); got != count {
			t.Fatalf("count=%d header drop count=%d", count, got)
		}
		if len(body) != 7 && len(body) == 7+count*48 {
			t.Fatalf("count=%d body len=%d matches the A21 48-byte law", count, len(body))
		}
	}
}
