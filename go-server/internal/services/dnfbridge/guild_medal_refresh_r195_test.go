package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// A guardian-gem drag used to replay list 0/1/2/7/38 after every op19. The
// guardian-gem page only needs its own list38 snapshot; rebuilding the four
// unrelated containers adds unnecessary client reconstruction work.
func TestGuildR195MedalMoveRefreshesOnlyList38(t *testing.T) {
	s, g, _ := guildR192Fixture(t, "2500")
	result := alignedcmd.Result{Operation: "move_itemspace"}
	action := alignedcmd.PostActionRefreshGuildMedalContainer
	if err := s.sendAlignedPostAction(g, result, action); err != nil {
		t.Fatal(err)
	}
	packets := splitItemGrantNoticeStream(t, s, g.conn.(*bufferConn).write.Bytes())
	if len(packets) != 1 || packets[0].classification != 0 || packets[0].msgID != 13 {
		t.Fatalf("packets=%+v, want one class0/op13 list38 snapshot", packets)
	}
	body := packets[0].body
	if len(body) != 3+currentItemListEntryWireSize || body[0] != 38 || binary.LittleEndian.Uint16(body[1:3]) != 1 {
		t.Fatalf("list38 body header=%x", body[:3])
	}
	row := body[3 : 3+currentItemListEntryWireSize]
	if int16(binary.LittleEndian.Uint16(row[0:2])) != 49 || binary.LittleEndian.Uint32(row[2:6]) != 90000 || binary.LittleEndian.Uint32(row[6:10]) != 4 {
		t.Fatalf("list38 row slot=%d item=%d count=%d", binary.LittleEndian.Uint16(row[0:2]), binary.LittleEndian.Uint32(row[2:6]), binary.LittleEndian.Uint32(row[6:10]))
	}
}

func TestGuildR195RejectedRequestRestoresMaterialsAfterFailureAck(t *testing.T) {
	s, g, repositories := guildR192Fixture(t, "2500")
	before, _, _ := repositories.Inventory.Load(context.Background(), "19")
	if err := s.handleGameCommand(g, 1, 835, guildR192Body(1, 49)); err != nil {
		t.Fatal(err)
	}
	after, _, _ := repositories.Inventory.Load(context.Background(), "19")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejection changed material inventory")
	}
	packets := splitItemGrantNoticeStream(t, s, g.conn.(*bufferConn).write.Bytes())
	if len(packets) != 2 || packets[0].classification != 1 || packets[0].msgID != 835 || !bytes.Equal(packets[0].body, []byte{0, 1}) {
		t.Fatalf("expected failure ACK then snapshot: %+v", packets)
	}
	b := packets[1].body
	if packets[1].msgID != 13 || packets[1].classification != 0 || len(b) != 3+currentItemListEntryWireSize || b[0] != 38 {
		t.Fatalf("invalid material restore snapshot: %+v", packets[1])
	}
	if binary.LittleEndian.Uint16(b[3:5]) != 49 || binary.LittleEndian.Uint32(b[5:9]) != 90000 || binary.LittleEndian.Uint32(b[9:13]) != 4 {
		t.Fatalf("rejected materials missing from snapshot: %x", b)
	}
}

func TestGuildR195LegacyWrongClassCannotConsume(t *testing.T) {
	s, g, repositories := guildR192Fixture(t, "2500")
	before, _, _ := repositories.Inventory.Load(context.Background(), "19")
	if err := s.handleGameCommand(g, 0, 835, guildR192Body(4, 49)); err != nil {
		t.Fatal(err)
	}
	after, _, _ := repositories.Inventory.Load(context.Background(), "19")
	if !reflect.DeepEqual(before, after) || g.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("wrong-class legacy request changed inventory or emitted a success")
	}
}
