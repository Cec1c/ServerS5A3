package dnfbridge

import (
	"encoding/binary"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func TestWeddingExtensionNativeWireBoundaries(t *testing.T) {
	for _, op := range []uint16{1048, 1049} {
		body := make([]byte, 19)
		for i := 0; i < 13; i++ {
			body[i] = 255
		}
		binary.LittleEndian.PutUint16(body[13:], 65)
		binary.LittleEndian.PutUint32(body[15:], 10002765)
		cmd, ok := decodeCurrentWeddingTreeCommand(op, body)
		if !ok || cmd.Slot != 65 || cmd.ItemID != 10002765 {
			t.Fatalf("decode %+v %t", cmd, ok)
		}
		if _, ok := decodeCurrentWeddingTreeCommand(op, append(body, 0)); ok {
			t.Fatal("trailing bytes")
		}
	}
	deletion := make([]byte, 23)
	binary.LittleEndian.PutUint16(deletion[13:], 39)
	binary.LittleEndian.PutUint32(deletion[15:], 10002765)
	binary.LittleEndian.PutUint32(deletion[19:], 1)
	if _, ok := decodeCurrentWeddingTreeCommand(1047, deletion); !ok {
		t.Fatal("valid furniture deletion")
	}
	binary.LittleEndian.PutUint32(deletion[19:], 2)
	if _, ok := decodeCurrentWeddingTreeCommand(1047, deletion); ok {
		t.Fatal("manufactured multi-count")
	}
	for _, op := range []uint16{1050, 1051} {
		if _, ok := decodeCurrentWeddingTreeCommand(op, nil); !ok {
			t.Fatal(op)
		}
		if _, ok := decodeCurrentWeddingTreeCommand(op, []byte{0}); ok {
			t.Fatal("unexpected body")
		}
	}
	state := wedding.CapsuleState{Progress: 10, Own: 7, Taken: 8}
	for op, want := range map[uint16]int{1047: 5, 1052: 10, 1053: 6, 1054: 6} {
		ack := currentWeddingExtensionACK(op, 0, state)
		if len(ack) != want || ack[0] != 1 || binary.LittleEndian.Uint32(ack[1:]) != 0 {
			t.Fatalf("opcode%d ack%x", op, ack)
		}
		if failure := currentWeddingExtensionACK(op, -1, state); len(failure) != 5 || binary.LittleEndian.Uint32(failure[1:]) != 0xffffffff {
			t.Fatal("failure body")
		}
	}
	packet := buildCurrentWeddingTree(wedding.TreeState{Index: 3, Expires: 0x12345678, Growth: 30, WaterCount: 2})
	if len(packet) != 13 || binary.LittleEndian.Uint32(packet[4:]) != 0x12345678 || packet[12] != 2 {
		t.Fatalf("tree%x", packet)
	}
	packet = buildCurrentWeddingCapsule(state, true)
	if len(packet) != 7 || binary.LittleEndian.Uint32(packet) != 10 || packet[4] != 7 || packet[5] != 8 || packet[6] != 1 {
		t.Fatalf("capsule%x", packet)
	}
}

func TestWeddingRoomRefreshIncludesEmptyPlacedAndCargoRows(t *testing.T) {
	rules := &wedding.Rules{Furniture: map[int64]wedding.Furniture{100: {Positions: map[uint16]bool{2: true, 8: true}}}}
	room := wedding.Room{Placed: map[uint16]r.ItemStack{2: {ItemID: 100, Count: 1}}, Bag: map[uint16]r.ItemStack{1: {ItemID: 100, Count: 1}}}
	b := buildCurrentWeddingRoomRefresh(room, rules)
	if len(b) != 4+42*11 || binary.LittleEndian.Uint32(b) != 42 {
		t.Fatalf("refresh shape %x", b)
	}
	// The second PVF placed slot is empty and must explicitly erase old native
	// model state. The first cargo row likewise clears its previous item.
	for _, at := range []int{4 + 11, 4 + 2*11} {
		if binary.LittleEndian.Uint32(b[at+1:]) != 0 || binary.LittleEndian.Uint32(b[at+7:]) != 0 {
			t.Fatal("empty slot omitted")
		}
	}
}
