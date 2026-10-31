package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func sendLegacyDurabilityFrame(t *testing.T, s *Service, se *gameSession, sequence uint16, body []byte, class byte) {
	t.Helper()
	raw := make([]byte, dnfproto.LegacyGameHeaderSize+len(body))
	raw[0] = class
	binary.LittleEndian.PutUint16(raw[1:3], 48)
	binary.LittleEndian.PutUint32(raw[3:7], uint32(len(raw)))
	binary.LittleEndian.PutUint16(raw[11:13], sequence)
	copy(raw[dnfproto.LegacyGameHeaderSize:], body)
	if err := s.handleLegacyGamePacket(se, raw); err != nil {
		t.Fatal(err)
	}
}

func TestDurabilityLegacyTransportCommitsAndSharesReplayBoundary(t *testing.T) {
	s, se, g := newDurabilityBridgeFixture(t, 48)
	before, _, _ := g.Equipment.Load(context.Background(), "99")
	sendLegacyDurabilityFrame(t, s, se, 10, []byte{12}, 1)
	after, _, _ := g.Equipment.Load(context.Background(), "99")
	want := append([]byte(nil), before.Entries["12"].RawEntry...)
	binary.LittleEndian.PutUint16(want[11:13], 47)
	if !bytes.Equal(after.Entries["12"].RawEntry, want) || after.Entries["12"].Extra["durability"] != "47" {
		t.Fatal("legacy frame did not commit exactly one durability decrement")
	}
	packet, rest := splitLongHengGameServerUpperPacket(t, se.conn.(*bufferConn).write.Bytes())
	if len(rest) != 0 || packet.Header.MsgID != 48 || packet.Header.Classification != 1 || !bytes.Equal(packet.Body, []byte{1, 12}) {
		t.Fatalf("legacy relative acknowledgement mismatch: %+v", packet)
	}
	se.conn.(*bufferConn).write.Reset()
	sendLegacyDurabilityFrame(t, s, se, 10, []byte{12}, 1)
	sendLegacyDurabilityFrame(t, s, se, 9, []byte{12}, 1)
	sendDurabilityFrame(t, s, se, 10, []byte{12}, 1)
	repeat, _, _ := g.Equipment.Load(context.Background(), "99")
	if !reflect.DeepEqual(repeat, after) || se.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("replayed or stale transport request decremented again or repeated relative ACK")
	}
	sendDurabilityFrame(t, s, se, 11, []byte{12}, 1)
	sendLegacyDurabilityFrame(t, s, se, 12, []byte{12}, 1)
	stored, _, _ := g.Equipment.Load(context.Background(), "99")
	if binary.LittleEndian.Uint16(stored.Entries["12"].RawEntry[11:13]) != 45 || stored.Entries["12"].Extra["durability"] != "45" {
		t.Fatal("next reports across both transports were lost")
	}
}

func TestDurabilityLegacyRejectsWrongOwnershipAndMalformedBody(t *testing.T) {
	for _, mode := range []string{"town", "old_session", "wrong_account", "empty", "trailing", "invalid_slot", "wrong_class", "unauthenticated"} {
		t.Run(mode, func(t *testing.T) {
			s, se, g := newDurabilityBridgeFixture(t, 48)
			body, class := []byte{12}, byte(1)
			switch mode {
			case "town":
				se.dungeon.runtime = nil
			case "old_session":
				s.gameSessions[99] = &gameSession{selectedCharacterID: 99, characterGeneration: 2}
			case "wrong_account":
				se.accountID = "another"
			case "empty":
				body = nil
			case "trailing":
				body = []byte{12, 0, 0, 0, 0}
			case "invalid_slot":
				body = []byte{255}
			case "wrong_class":
				class = 2
			case "unauthenticated":
				se.accountFromToken = false
			}
			before, _, _ := g.Equipment.Load(context.Background(), "99")
			sendLegacyDurabilityFrame(t, s, se, 10, body, class)
			after, _, _ := g.Equipment.Load(context.Background(), "99")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected legacy report changed equipment")
			}
			if mode != "wrong_class" && se.conn.(*bufferConn).write.Len() == 0 {
				t.Fatal("rejected legacy command fell through silently")
			}
		})
	}
}

func TestDurabilityLegacyAutoRepairAcknowledgesCommittedQuota(t *testing.T) {
	s, se, g := newDurabilityBridgeFixture(t, 1)
	ctx := context.Background()
	a, _, _ := g.Account.Load(ctx, "account-1")
	premium.Upsert(&a, premium.DevilSlotType(premium.DevilSlotAutoRepair), 3600, 1, time.Now().UTC())
	if err := g.Account.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	sendLegacyDurabilityFrame(t, s, se, 10, []byte{12}, 1)
	data := se.conn.(*bufferConn).write.Bytes()
	var ids []uint16
	for len(data) > 0 {
		p, rest := splitLongHengGameServerUpperPacket(t, data)
		ids = append(ids, p.Header.MsgID)
		data = rest
	}
	if !reflect.DeepEqual(ids, []uint16{48, 23, 14}) {
		t.Fatalf("legacy repair acknowledgement order: %v", ids)
	}
	character, _, _ := g.Character.Load(ctx, "99")
	equipment, _, _ := g.Equipment.Load(ctx, "99")
	if premium.DailyUsage(character, premium.DevilSlotAutoRepair, time.Now().UTC()) != 1 || equipment.Entries["12"].Extra["durability"] != "48" {
		t.Fatal("legacy repair quota or durability did not commit")
	}
}
