package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// This real invite/accept -> host transfer -> op16 path must never send the
// legacy NOTI9 layout to official24.9, even before the op37 loading barrier.
// The current native reader consumes an extra actor byte and six additional
// identity bytes per cross-context row. Synthetic character99/100 and dungeon700
// come from the explicit existing fixture; no player data is changed.
func TestOfficial249PartyRoomTableAfterHostTransferFitsCurrentReader(t *testing.T) {
	f := newInviteAcceptDungeonFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	f.formPartyThroughClientAccept(t)
	if err := f.service.handleOnlineChangePartyHost(f.leader, []byte{1}); err != nil {
		t.Fatal(err)
	}
	state := f.service.runtimePartyStateForSession(f.follower)
	if state.UserID != inviteAcceptFollowerCharacterID || len(runtimePartyMembers(state)) != 2 {
		t.Fatal("host transfer lost party", state)
	}
	for _, session := range []*gameSession{f.leader, f.follower} {
		session.conn.(*bufferConn).write.Reset()
	}
	f.selectDungeon(t, f.follower, 700)
	// Fixed slots are not renumbered by the transfer: char99 keeps slot zero and
	// char100 keeps slot one even though char100 is now the authoritative leader.
	expected := []uint16{inviteAcceptLeaderCharacterID, inviteAcceptFollowerCharacterID}
	total := 0
	for _, session := range []*gameSession{f.leader, f.follower} {
		wire := session.conn.(*bufferConn).write.Bytes()
		for len(wire) > 0 {
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
			wire = rest
			if packet.Header.Classification != 0 || packet.Header.MsgID != 9 || len(packet.Body) < 10 || packet.Body[9] != 0 {
				continue
			}
			ids, slots, hostSlot, err := readPartyRoomOfficial249Record(packet.Body, session.channel.ID)
			if err != nil || !reflect.DeepEqual(ids, expected) || !reflect.DeepEqual(slots, []byte{0, 1}) {
				t.Fatalf("char%d current NOTI9 ids=%v want%v len%d err=%v body=%x", session.selectedCharacterID, ids, expected, len(packet.Body), err, packet.Body)
			}
			if hostSlot != 1 {
				t.Fatalf("char%d current NOTI9 host slot=%d want the new leader's fixed slot 1 body=%x", session.selectedCharacterID, hostSlot, packet.Body)
			}
			total++
		}
	}
	if total < 2 {
		t.Fatalf("room tables=%d want both members", total)
	}
}

func readPartyRoomOfficial249Record(body []byte, localChannel int) (ids []uint16, slots []byte, hostSlot byte, err error) {
	position := 0
	take := func(n int) []byte {
		if err != nil || n < 0 || n > len(body)-position {
			err = fmt.Errorf("native read overrun at %d length %d", position, n)
			return make([]byte, n)
		}
		out := body[position : position+n]
		position += n
		return out
	}
	dstr := func() {
		n := binary.LittleEndian.Uint32(take(4))
		if n > 256 {
			err = fmt.Errorf("identity string overflow")
			return
		}
		take(int(n))
	}
	if binary.LittleEndian.Uint16(take(2)) != 1 || binary.LittleEndian.Uint16(take(2)) != 9999 {
		return nil, nil, 0, fmt.Errorf("record header")
	}
	take(2) // actor key
	outer := take(4)
	owner := outer[1]
	if outer[3] != 0 {
		return nil, nil, 0, fmt.Errorf("record kind")
	}
	take(2)
	dstr()
	take(14)
	count := int(take(1)[0])
	if count > 8 {
		return nil, nil, 0, fmt.Errorf("party count")
	}
	for row := 0; row < count; row++ {
		slots = append(slots, take(1)[0])
		ids = append(ids, binary.LittleEndian.Uint16(take(2)))
		if int(owner) != localChannel {
			take(5)
			dstr()
			dstr()
			take(1)
		}
		take(3)
	}
	tail := take(4)
	if err == nil {
		hostSlot = tail[1]
	}
	if err == nil && position != len(body) {
		err = fmt.Errorf("unconsumed bytes: %d", len(body)-position)
	}
	return ids, slots, hostSlot, err
}

func TestOfficial249RoomTableProfilesAndNativeFixture(t *testing.T) {
	for _, count := range []int{0, 1, 2, 4, 8} {
		rows := make([]dungeonPartyRoomSlotTableRow, count)
		ids := make([]uint16, count)
		for i := range rows {
			rows[i] = dungeonPartyRoomSlotTableRow{Slot: byte(i), UserID: uint16(6 + i), Job: byte(i), Level: 90, Grow: 33, Name: "实际玩家"}
			ids[i] = rows[i].UserID
		}
		actor := dungeonPartyRoomSlotTableActor{ObjectKey: 6, Name: "test78", Job: 0, Grow: 33}
		old := buildDungeonPartyRoomSlotTableBody(actor, rows, 0, vendorDungeonPartyRoomSlotTableShape)
		legacy := buildDungeonPartyRoomSlotTableBodyForProfile(actor, rows, 0, vendorDungeonPartyRoomSlotTableShape, false)
		if !bytes.Equal(old, legacy) {
			t.Fatal("legacy profile changed")
		}
		current := buildDungeonPartyRoomSlotTableBodyForProfile(actor, rows, 0, vendorDungeonPartyRoomSlotTableShape, true)
		got, slots, _, err := readPartyRoomOfficial249Record(current, 42)
		if err != nil || !reflect.DeepEqual(got, ids) && !(count == 0 && len(got) == 0) {
			t.Fatalf("rows%d got%v want%v err%v", count, got, ids, err)
		}
		for index := range slots {
			if slots[index] != byte(index) {
				t.Fatalf("rows%d slot[%d]=%d want %d", count, index, slots[index], index)
			}
		}
		if len(current)-len(legacy) != 1+6*count {
			t.Fatal("native actor/identity boundary delta")
		}
		if dir := os.Getenv("DNFBRIDGE_PARTY_ROOM_NATIVE_OUTPUT"); dir != "" && count == 2 {
			if err := os.WriteFile(filepath.Join(dir, "current-room-table.bin"), current, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
