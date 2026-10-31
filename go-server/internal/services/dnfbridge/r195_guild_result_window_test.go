package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
)

// The fixed v154 client reads op835/op864 success as u8 status followed by one
// u32 product item ID. The ID populates the native result window (the item has
// already been granted by the guildcompound transaction). Without it the client
// receives a zero/invalid ID and skips the result window even though list38 is
// correct.
func TestR195GuildSuccessAckCarriesExactlyOneResultWindowItem(t *testing.T) {
	// The zero row documents the wire layout only. Owner.Apply never returns a
	// successful transaction with a zero result item.
	for _, tc := range []struct {
		name   string
		itemID uint32
		want   []byte
	}{
		{name: "zero_layout_only", itemID: 0, want: []byte{1, 0, 0, 0, 0}},
		{name: "high_bit_item", itemID: 0x91234567, want: []byte{1, 0x67, 0x45, 0x23, 0x91}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildCurrentGuildCompoundSuccessAck(tc.itemID); !bytes.Equal(got, tc.want) {
				t.Fatalf("success ack=%x want=%x", got, tc.want)
			}
		})
	}
}

func TestR195GuildGemResultWindowAckMatchesSingleGrantedOutput(t *testing.T) {
	s, g, repositories := guildR192Fixture(t, "2500")
	if err := s.handleGameCommand(g, 1, 835, guildR192Body(4, 49)); err != nil {
		t.Fatal(err)
	}
	inv, found, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%v err=%v", found, err)
	}
	if len(inv.Slots) != 1 {
		t.Fatalf("legal synthesis must leave one result only: %+v", inv.Slots)
	}
	result, found := inv.Slots["38:49"]
	if !found || result.ItemID != 90001 || result.Count != 1 {
		t.Fatalf("gem result slot=%+v", inv.Slots)
	}

	packets := splitItemGrantNoticeStream(t, s, g.conn.(*bufferConn).write.Bytes())
	if len(packets) != 2 {
		t.Fatalf("packets=%+v, want result ack then list38", packets)
	}
	ack, refresh := packets[0], packets[1]
	wantAck := []byte{1, 0x91, 0x5f, 0x01, 0x00}
	if ack.classification != 1 || ack.msgID != 835 || !bytes.Equal(ack.body, wantAck) {
		t.Fatalf("gem result-window ack class=%d msg=%d body=%x", ack.classification, ack.msgID, ack.body)
	}
	if refresh.classification != 0 || refresh.msgID != 13 || len(refresh.body) != 3+currentItemListEntryWireSize || refresh.body[0] != 38 {
		t.Fatalf("gem refresh=%+v", refresh)
	}
	row := refresh.body[3 : 3+currentItemListEntryWireSize]
	if int16(binary.LittleEndian.Uint16(row[0:2])) != 49 ||
		binary.LittleEndian.Uint32(row[2:6]) != binary.LittleEndian.Uint32(ack.body[1:]) ||
		binary.LittleEndian.Uint32(row[6:10]) != 1 {
		t.Fatalf("gem refresh row=%x ack=%x", row, ack.body)
	}
}

func TestR195GuildMedalResultWindowAckMatchesSingleGrantedOutput(t *testing.T) {
	s, g, repositories := guildR192Fixture(t, "2500")
	inv, found, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%v err=%v", found, err)
	}
	body := guildR192Body(4, 0)
	for i := 0; i < 4; i++ {
		slot, err := currentGuildCompoundGrant(s.pvfItemCatalog, &inv, 100380009)
		if err != nil {
			t.Fatal(err)
		}
		binary.LittleEndian.PutUint32(body[21+i*4:], uint32(slot))
	}
	if err := repositories.Inventory.Save(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameCommand(g, 1, 864, body); err != nil {
		t.Fatal(err)
	}
	after, found, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("reload inventory found=%v err=%v", found, err)
	}
	if after.Slots["38:49"].Count != 4 || len(after.Slots) != 2 {
		t.Fatalf("medal synthesis must keep gem page and grant one medal: %+v", after.Slots)
	}
	medal, found := after.Slots["38:0"]
	if !found || medal.Count != 1 || medal.ItemID == 0 {
		t.Fatalf("medal result slot=%+v", after.Slots)
	}

	packets := splitItemGrantNoticeStream(t, s, g.conn.(*bufferConn).write.Bytes())
	if len(packets) != 2 {
		t.Fatalf("packets=%+v, want result ack then list38", packets)
	}
	ack, refresh := packets[0], packets[1]
	wantAck := binary.LittleEndian.AppendUint32([]byte{1}, uint32(medal.ItemID))
	if ack.classification != 1 || ack.msgID != 864 || !bytes.Equal(ack.body, wantAck) {
		t.Fatalf("medal result-window ack class=%d msg=%d body=%x want=%x", ack.classification, ack.msgID, ack.body, wantAck)
	}
	if refresh.classification != 0 || refresh.msgID != 13 || len(refresh.body) != 3+2*currentItemListEntryWireSize || refresh.body[0] != 38 {
		t.Fatalf("medal refresh=%+v", refresh)
	}
	first := refresh.body[3 : 3+currentItemListEntryWireSize]
	if int16(binary.LittleEndian.Uint16(first[0:2])) != 0 ||
		binary.LittleEndian.Uint32(first[2:6]) != binary.LittleEndian.Uint32(ack.body[1:]) ||
		binary.LittleEndian.Uint32(first[6:10]) != 1 {
		t.Fatalf("medal refresh row=%x ack=%x", first, ack.body)
	}
}
