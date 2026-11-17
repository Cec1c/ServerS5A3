package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func npcFavorItemBody(npc uint32) []byte {
	b := testQuestClearCouponRequestBody(70, 159)
	binary.LittleEndian.PutUint32(b[11:15], npc)
	return b
}

func TestNPCFavorItemNativeEnvelope(t *testing.T) {
	b := npcFavorItemBody(2)
	if _, slot, _, err := parseCurrentEventItemRequest(515, b); err != nil || slot != 70 {
		t.Fatalf("slot=%d err=%v", slot, err)
	}
	for _, offset := range []int{1, 2, 3, 7, 15, 18} {
		bad := append([]byte(nil), b...)
		bad[offset] = 255
		if _, _, _, err := parseCurrentEventItemRequest(515, bad); err == nil {
			t.Fatalf("accepted offset %d", offset)
		}
	}
	for _, bad := range [][]byte{b[:18], append(append([]byte(nil), b...), 0), npcFavorItemBody(0)} {
		if _, _, _, err := parseCurrentEventItemRequest(515, bad); err == nil {
			t.Fatalf("accepted %x", bad)
		}
	}
}

func TestRealPVFNPCFavorLetterDispatchAndAtomicEffect(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DNFBRIDGE_REAL_PVF_SMOKE", "")
	for _, scenario := range []string{"success", "npc_not_allowed", "wrong_item", "low_level", "locked", "expired", "dungeon", "malformed", "send_failure"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, g := eventItemBridgeFixture(t, catalog, a, 490700963)
			ctx := context.Background()
			body := npcFavorItemBody(2)
			bag, _, _ := g.Inventory.Load(ctx, "29")
			ch, _, _ := g.Character.Load(ctx, "29")
			item := bag.Slots["0:70"]
			switch scenario {
			case "npc_not_allowed":
				body = npcFavorItemBody(0xfffffffe)
			case "wrong_item":
				item.ItemID = 490008261
			case "low_level":
				ch.Level = 1
			case "locked":
				item.Extra["equipment_lock_state"] = "locked"
			case "expired":
				item.ExpireAt = s.gameplayNow()
			case "dungeon":
				ch.Location.DungeonID = 1
			case "malformed":
				body[15] = 1
			case "send_failure":
				p.conn = &eventItemSendFailure{}
			}
			bag.Slots["0:70"] = item
			if err := g.Inventory.Save(ctx, bag); err != nil {
				t.Fatal(err)
			}
			if err := g.Character.Save(ctx, ch); err != nil {
				t.Fatal(err)
			}
			handled, err := s.handleAlignedGameCommand(p, 1, 515, body)
			if !handled || (err != nil) != (scenario == "send_failure") {
				t.Fatalf("handled=%v err=%v", handled, err)
			}
			afterBag, _, _ := g.Inventory.Load(ctx, "29")
			afterCh, _, _ := g.Character.Load(ctx, "29")
			if scenario != "success" && scenario != "send_failure" {
				if !reflect.DeepEqual(bag, afterBag) || !reflect.DeepEqual(ch, afterCh) {
					t.Fatal("rejected use changed assets")
				}
				frame, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
				if len(rest) != 0 || frame.Header.MsgID != 515 || frame.Header.Classification != 1 || !bytes.Equal(frame.Body, []byte{0, 17, 70, 0, 0, 159}) {
					t.Fatalf("failure %v %x", frame.Header, frame.Body)
				}
				return
			}
			if afterBag.Slots["0:70"].Count != 0 || afterCh.Stats["npc_favor_2_points"] != 1000 {
				t.Fatalf("count=%d stats=%v", afterBag.Slots["0:70"].Count, afterCh.Stats)
			}
			if scenario == "send_failure" {
				return
			}
			raw := p.conn.(*bufferConn).write.Bytes()
			var ops []uint16
			for len(raw) > 0 {
				frame, rest := splitCurrentGameServerUpperPacketAuto(t, raw)
				raw = rest
				ops = append(ops, frame.Header.MsgID)
				if len(ops) == 1 && (frame.Header.Classification != 1 || !bytes.Equal(frame.Body, []byte{1, 70, 0, 0, 159, 0, 0, 0})) {
					t.Fatalf("ACK %x", frame.Body)
				}
			}
			if !reflect.DeepEqual(ops, []uint16{515, 743, 14}) {
				t.Fatalf("ops=%v", ops)
			}
			p.conn.(*bufferConn).write.Reset()
			if _, err := s.handleAlignedGameCommand(p, 1, 515, body); err != nil {
				t.Fatal(err)
			}
			replayed, _, _ := g.Character.Load(ctx, "29")
			if !reflect.DeepEqual(afterCh, replayed) {
				t.Fatal("empty slot replay added favor")
			}
		})
	}
}
