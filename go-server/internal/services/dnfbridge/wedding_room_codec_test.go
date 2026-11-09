package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func TestWeddingRoomCodecPreservesNativeFieldWidthsAndSortsSnapshots(t *testing.T) {
	room := wedding.Room{Version: 1, Placed: map[uint16]r.ItemStack{27: {ItemID: 10002797, Count: 1}, 0: {ItemID: 10002685, Count: 1}}, Bag: map[uint16]r.ItemStack{39: {ItemID: 10002797, Count: 1}, 2: {ItemID: 10002685, Count: 1}}}
	placed := buildCurrentWeddingRoom(room)
	if len(placed) != 29 || binary.LittleEndian.Uint32(placed) != 2 || binary.LittleEndian.Uint16(placed[4:]) != 0 || binary.LittleEndian.Uint32(placed[6:]) != 10002685 || binary.LittleEndian.Uint16(placed[10:]) != 27 || binary.LittleEndian.Uint32(placed[12:]) != 10002797 || !bytes.Equal(placed[16:], make([]byte, 13)) {
		t.Fatalf("native 1008: %x", placed)
	}
	bag := buildCurrentWeddingRoomInventory(room)
	if len(bag) != 28 || binary.LittleEndian.Uint32(bag) != 2 || binary.LittleEndian.Uint16(bag[4:]) != 2 || binary.LittleEndian.Uint32(bag[6:]) != 10002685 || binary.LittleEndian.Uint32(bag[10:]) != 1 || binary.LittleEndian.Uint16(bag[14:]) != 39 || binary.LittleEndian.Uint32(bag[16:]) != 10002797 || binary.LittleEndian.Uint32(bag[20:]) != 1 || binary.LittleEndian.Uint32(bag[24:]) != 0 {
		t.Fatalf("native 1009: %x", bag)
	}
	if !bytes.Equal(buildCurrentWeddingRoom(wedding.Room{}), make([]byte, 17)) || !bytes.Equal(buildCurrentWeddingRoomInventory(wedding.Room{}), make([]byte, 8)) {
		t.Fatal("empty room requires explicit zero counts")
	}
	if got := append(weddingResultACK(-2), 1); !bytes.Equal(got, []byte{1, 0xfe, 0xff, 0xff, 0xff, 1}) {
		t.Fatalf("open ACK missing operation flag: %x", got)
	}
}

func TestWeddingRoomMoveCodecOpaquePrefixAndStrictBoundary(t *testing.T) {
	body := weddingRoomNativeCases(t)[1].body(t)
	q, ok := decodeCurrentWeddingRoomMove(body)
	if !ok || !q.FromBag || q.ToBag || q.FromSlot != 39 || q.ToSlot != 27 || q.Count != 1 || q.SourceItemID != 10002797 || q.ExpectedDestination != 10002797 {
		t.Fatalf("native 1046: %+v %v", q, ok)
	}
	for i := 0; i < 13; i++ {
		body[i] = 0xa5
	}
	if same, ok := decodeCurrentWeddingRoomMove(body); !ok || same != q {
		t.Fatal("opaque native prefix changed move ownership")
	}
	for _, size := range []int{0, 13, 20, 26, 28} {
		invalid := append(append([]byte(nil), body...), 0)
		if _, ok := decodeCurrentWeddingRoomMove(invalid[:size]); ok {
			t.Fatalf("accepted payload size %d", size)
		}
	}
	binary.LittleEndian.PutUint32(body[16:], 0)
	if _, ok := decodeCurrentWeddingRoomMove(body); ok {
		t.Fatal("accepted empty source item")
	}
	for _, item := range []uint32{2, 10002685, 0xffffffff} {
		binary.LittleEndian.PutUint32(body[16:], item)
		if q, ok := decodeCurrentWeddingRoomMove(body); !ok || q.Count != 1 || q.SourceItemID != int64(item) {
			t.Fatalf("item %d was interpreted as quantity: %+v %v", item, q, ok)
		}
	}
	binary.LittleEndian.PutUint32(body[16:], 10002797)
	for _, slot := range []uint16{40, 65535} {
		binary.LittleEndian.PutUint16(body[14:], slot)
		if _, ok := decodeCurrentWeddingRoomMove(body); ok {
			t.Fatalf("accepted bag slot %d", slot)
		}
	}
	binary.LittleEndian.PutUint16(body[14:], 0)
	for _, slot := range []uint16{28, 65535} {
		binary.LittleEndian.PutUint16(body[21:], slot)
		if _, ok := decodeCurrentWeddingRoomMove(body); ok {
			t.Fatalf("accepted installed slot %d", slot)
		}
	}
	binary.LittleEndian.PutUint16(body[21:], 0)
	body[13] = 2
	if _, ok := decodeCurrentWeddingRoomMove(body); ok {
		t.Fatal("accepted unknown source container")
	}
	if weddingPairInstance(2, 10) != weddingPairInstance(10, 2) || weddingPairInstance(1, 2) == weddingPairInstance(1, 3) || weddingPairInstance(2, 2) != 0 || weddingPairInstance(0, 2) != 0 {
		t.Fatal("couple instance leaked across owners")
	}
}
