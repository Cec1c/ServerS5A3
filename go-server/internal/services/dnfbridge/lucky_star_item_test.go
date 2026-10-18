package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/rental"
	r "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func luckyItemSource() initialEquipmentMemSource {
	return initialEquipmentMemSource{
		"monster/monster.lst": "", "equipment/equipment.lst": "", "stackable/stackable.lst": "490001384 `box.stk`",
		"stackable/box.stk":        "[stackable type]\n`[waste]` 0\n[stack limit]\n999\n[action type]\n`[rent point]` 50\n[/action type]\n[action usable place]\n`[village]` `[seria room]`\n[/action usable place]\n[use action packet]\n1\n[minimum level]\n1\n[daily delete item]\n1",
		currentRentalSystemPVFPath: "[limit point]\n400\n[point charge]\n`pdungeon` 1 `gold` 100000\n[section]\n0 1 7 3 1 1 1\n[group]\n`[gun blader]` 0\n[package selection]\n416000000 3\n[/package selection]\n[/group]",
	}
}

func luckyItemBridgeFixture(t *testing.T) (*Service, *gameSession, r.Group) {
	t.Helper()
	s, p, repos := guildMileageFixture(t)
	var err error
	s.pvfItemCatalog, err = newPVFDungeonDropCatalog(luckyItemSource())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveCurrentLuckyStarItem(s.pvfItemCatalog, 490001384); err != nil {
		t.Fatal(err)
	}
	if err := repos.Account.Save(context.Background(), r.AccountRecord{AccountID: s.accountIDForSession(p), Metadata: map[string]string{rental.PointMetadataKey: "10"}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(context.Background(), r.InventoryRecord{CharacterID: "29", Slots: map[string]r.ItemStack{"0:87": {ItemID: 490001384, Count: 2, Extra: map[string]string{"item_kind": "stackable"}}}}); err != nil {
		t.Fatal(err)
	}
	return s, p, repos
}

func sendLuckyItemWire(t *testing.T, s *Service, p *gameSession, upper bool, body []byte, seq uint16) {
	t.Helper()
	wire, _ := protocol.BuildChannelPacket(515, body, seq, 1)
	var err error
	if upper {
		err = s.handleGameUpper(p, wire)
	} else {
		packets, rest, skip, parseErr := protocol.SplitLatestGameStream(wire, 65535)
		if parseErr != nil || len(packets) != 1 || len(rest) != 0 || skip != 0 {
			t.Fatal("stream fixture")
		}
		err = s.handleGameStreamPacket(p, packets[0])
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestLuckyStarItemStreamRespondsAndReplays(t *testing.T) {
	for _, upper := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "upper"}[upper], func(t *testing.T) {
			s, p, repos := luckyItemBridgeFixture(t)
			body := testQuestClearCouponRequestBody(87, 105)
			sendLuckyItemWire(t, s, p, upper, body, 123)
			wire := p.conn.(*bufferConn).write.Bytes()
			if len(wire) == 0 {
				t.Fatal("missing op515 action105 acknowledgement")
			}
			var ops []uint16
			for len(wire) > 0 {
				packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
				wire = rest
				ops = append(ops, packet.Header.MsgID)
				if packet.Header.MsgID == 515 && !bytes.Equal(packet.Body, []byte{1, 87, 0, 0, 105, 0, 0, 0}) {
					t.Fatalf("ack %x", packet.Body)
				}
				if packet.Header.MsgID == 985 && binary.LittleEndian.Uint32(packet.Body) != 60 {
					t.Fatalf("wallet %x", packet.Body)
				}
			}
			if len(ops) < 3 || ops[0] != 515 || ops[len(ops)-1] != 985 {
				t.Fatalf("order %v", ops)
			}
			p.conn.(*bufferConn).write.Reset()
			sendLuckyItemWire(t, s, p, !upper, body, 123)
			a, _, _ := repos.Account.Load(context.Background(), s.accountIDForSession(p))
			bag, _, _ := repos.Inventory.Load(context.Background(), "29")
			if a.Metadata[rental.PointMetadataKey] != "60" || bag.Slots["0:87"].Count != 1 {
				t.Fatal("duplicate consumption")
			}
			packet, _ := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
			if packet.Header.MsgID != 515 || packet.Body[0] != 0 {
				t.Fatal("duplicate ACK would decrement local item again")
			}
		})
	}
}

func TestLuckyStarItemStreamFailureStillResponds(t *testing.T) {
	for _, mode := range []string{"cap", "malformed", "wrong_list", "busy_town", "wrong_account", "unbound"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos := luckyItemBridgeFixture(t)
			body := testQuestClearCouponRequestBody(87, 105)
			a, _, _ := repos.Account.Load(context.Background(), s.accountIDForSession(p))
			switch mode {
			case "cap":
				a.Metadata[rental.PointMetadataKey] = "351"
				repos.Account.Save(context.Background(), a)
			case "malformed":
				body = body[:11]
			case "wrong_list":
				body[2] = 1
			case "busy_town":
				p.townMu.Lock()
				defer p.townMu.Unlock()
			case "wrong_account":
				p.accountID = "other"
			case "unbound":
				p.selectedCharacterID = 0
			}
			before, _, _ := repos.Inventory.Load(context.Background(), "29")
			if mode == "busy_town" {
				// The outer stream intentionally waits for scene rebuilds. Exercise
				// the handler's nonblocking guard without self-locking that preflight.
				sequence := uint64(100)
				if handled, err := s.handleCurrentLuckyStarItemRequest(p, gameplayRequest{Opcode: 515, Classification: 1, Body: body, Sequence: &sequence}); !handled || err != nil {
					t.Fatalf("%v %v", handled, err)
				}
			} else {
				sendLuckyItemWire(t, s, p, false, body, 100)
			}
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
			if len(rest) != 0 || packet.Header.MsgID != 515 || len(packet.Body) != 6 || packet.Body[0] != 0 {
				t.Fatalf("failure ack %x", packet.Body)
			}
			after, _, _ := repos.Inventory.Load(context.Background(), "29")
			aa, _, _ := repos.Account.Load(context.Background(), a.AccountID)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(a, aa) {
				t.Fatal("rejected use mutated assets")
			}
		})
	}
}

func TestLuckyStarItemPVFRejectsUnprovenEffects(t *testing.T) {
	for _, pair := range [][2]string{{"`[rent point]` 50", "`[rent point]` -1"}, {"[use action packet]\n1", "[use action packet]\n0"}, {"`[village]` `[seria room]`", "`[dungeon]`"}, {"[limit point]\n400", "[limit point]\n0"}} {
		source := luckyItemSource()
		for path, text := range source {
			source[path] = strings.ReplaceAll(text, pair[0], pair[1])
		}
		items, err := newPVFDungeonDropCatalog(source)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := resolveCurrentLuckyStarItem(items, 490001384); err == nil {
			t.Fatalf("accepted %v", pair)
		}
	}
}

func TestLuckyStarItemRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF not configured")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	d, err := resolveCurrentLuckyStarItem(items, 490001384)
	if err != nil || d.Amount != 50 || d.Limit != 400 || !d.DailyDelete || d.MinimumLevel != 1 {
		t.Fatalf("%+v %v", d, err)
	}
}
