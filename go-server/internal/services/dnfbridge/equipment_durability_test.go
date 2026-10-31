package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func newDurabilityBridgeFixture(t *testing.T, durability uint16) (*Service, *gameSession, repo.Group) {
	t.Helper()
	s, r, group := prepareCurrentDungeonDropTest(t, 1, 1001)
	s.options.currentClientVersion = "official-24.9"
	s.options.gameUpperHeader = gameUpperHeaderServer16
	s.options.gameUpperClientBodyCodec = gameUpperClientBodyCodecPlain
	ctx := context.Background()
	if err := group.Account.Save(ctx, repo.AccountRecord{AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 0x77)
	binary.LittleEndian.PutUint16(raw[:2], 12)
	binary.LittleEndian.PutUint32(raw[2:6], 101040493)
	binary.LittleEndian.PutUint32(raw[6:10], 987654)
	raw[10] = 15
	binary.LittleEndian.PutUint16(raw[11:13], durability)
	if err := group.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "99", Entries: map[string]repo.EquipmentEntry{"12": {SlotIndex: 12, ItemID: 101040493, RawEntry: raw, Extra: map[string]string{"durability": strconv.Itoa(int(durability)), "max_durability": "48", "quality_seed": "987654"}}}}); err != nil {
		t.Fatal(err)
	}
	s.repairCostCatalog = &currentRepairCostCatalog{evidence: map[int64]alignedcmd.RepairCostEvidence{101040493: {EquipmentType: "[weapon]", MaxDurability: 48}}}
	se := &gameSession{conn: &bufferConn{}, accountID: "account-1", selectedCharacterID: 99, characterGeneration: 1, loginPacketSeen: true, accountFromToken: true}
	se.dungeon.runtime = r
	s.gameSessions = map[uint16]*gameSession{99: se}
	return s, se, group
}

func sendDurabilityFrame(t *testing.T, s *Service, se *gameSession, sequence uint16, body []byte, classification byte) {
	t.Helper()
	frame, err := dnfproto.BuildChannelPacket(48, body, sequence, classification)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.handleGameUpper(se, frame); err != nil {
		t.Fatal(err)
	}
}

func TestDurabilityRealRoutePersistsAndDoesNotReplayRelativeAck(t *testing.T) {
	s, se, g := newDurabilityBridgeFixture(t, 48)
	before, _, _ := g.Equipment.Load(context.Background(), "99")
	sendDurabilityFrame(t, s, se, 10, []byte{12}, 1)
	after, _, _ := g.Equipment.Load(context.Background(), "99")
	want := append([]byte(nil), before.Entries["12"].RawEntry...)
	binary.LittleEndian.PutUint16(want[11:13], 47)
	if !bytes.Equal(after.Entries["12"].RawEntry, want) || after.Entries["12"].Extra["durability"] != "47" {
		t.Fatalf("durability changed unrelated bytes: %+v", after)
	}
	packet, rest := splitLongHengGameServerUpperPacket(t, se.conn.(*bufferConn).write.Bytes())
	if len(rest) != 0 || packet.Header.MsgID != 48 || packet.Header.Classification != 1 || !bytes.Equal(packet.Body, []byte{1, 12}) {
		t.Fatalf("relative ack mismatch %+v rest=%x", packet, rest)
	}
	se.conn.(*bufferConn).write.Reset()
	sendDurabilityFrame(t, s, se, 10, []byte{12}, 1)
	repeat, _, _ := g.Equipment.Load(context.Background(), "99")
	if !reflect.DeepEqual(repeat, after) || se.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("duplicate decremented or repeated relative ACK")
	}
	sendDurabilityFrame(t, s, se, 11, []byte{12}, 1)
	stored, _, _ := g.Equipment.Load(context.Background(), "99")
	if binary.LittleEndian.Uint16(stored.Entries["12"].RawEntry[11:13]) != 46 {
		t.Fatal("next report was lost")
	}
	entry, ok := currentItemListEntryFromEquipment(stored.Entries["12"])
	if !ok || binary.LittleEndian.Uint16(entry.data[11:13]) != 46 {
		t.Fatal("relogin projection did not retain wear")
	}
}

func TestDurabilityRejectsWrongSceneOwnerAndMalformedFrame(t *testing.T) {
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
				body = []byte{12, 0}
			case "invalid_slot":
				body = []byte{255}
			case "wrong_class":
				class = 2
			case "unauthenticated":
				se.accountFromToken = false
			}
			before, _, _ := g.Equipment.Load(context.Background(), "99")
			sendDurabilityFrame(t, s, se, 10, body, class)
			after, _, _ := g.Equipment.Load(context.Background(), "99")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected report changed equipment")
			}
		})
	}
}

func TestDurabilityAutoRepairUsesCommittedQuotaAndRepairAck(t *testing.T) {
	s, se, g := newDurabilityBridgeFixture(t, 1)
	ctx := context.Background()
	a, _, _ := g.Account.Load(ctx, "account-1")
	premium.Upsert(&a, premium.DevilSlotType(premium.DevilSlotAutoRepair), 3600, 1, time.Now().UTC())
	if err := g.Account.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	sendDurabilityFrame(t, s, se, 10, []byte{12}, 1)
	data := se.conn.(*bufferConn).write.Bytes()
	var ids []uint16
	for len(data) > 0 {
		p, rest := splitLongHengGameServerUpperPacket(t, data)
		ids = append(ids, p.Header.MsgID)
		data = rest
	}
	if !reflect.DeepEqual(ids, []uint16{48, 23, 14}) {
		t.Fatalf("repair order=%v", ids)
	}
	character, _, _ := g.Character.Load(ctx, "99")
	if premium.DailyUsage(character, premium.DevilSlotAutoRepair, time.Now().UTC()) != 1 {
		t.Fatal("auto repair quota not committed")
	}
	e, _, _ := g.Equipment.Load(ctx, "99")
	if binary.LittleEndian.Uint16(e.Entries["12"].RawEntry[11:13]) != 48 {
		t.Fatal("auto repair not persisted")
	}
}

func TestDurabilityZeroOverridesStaleRawAndMaxOnBothProjections(t *testing.T) {
	raw := make([]byte, 0x77)
	binary.LittleEndian.PutUint32(raw[2:6], 101040493)
	binary.LittleEndian.PutUint16(raw[11:13], 48)
	extra := map[string]string{"durability": "0", "max_durability": "48", "item_kind": "equipment"}
	e, ok := currentItemListEntryFromEquipment(repo.EquipmentEntry{SlotIndex: 12, ItemID: 101040493, RawEntry: raw, Extra: extra})
	if !ok || binary.LittleEndian.Uint16(e.data[11:13]) != 0 {
		t.Fatal("equipped projection resurrected stale durability")
	}
	b := currentItemListEntryFromStack(0, 9, repo.ItemStack{ItemID: 101040493, Count: 1, RawEntry: raw, Extra: extra})
	if binary.LittleEndian.Uint16(b.data[11:13]) != 0 {
		t.Fatal("bag projection resurrected stale durability")
	}
}

func TestRealPVFDurabilityDecreaseCurrentWeapon(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s, se, g := newDurabilityBridgeFixture(t, 48)
	s.repairCostCatalog = nil
	s.initialEquipmentArchive = archive
	sendDurabilityFrame(t, s, se, 10, []byte{12}, 1)
	after, _, _ := g.Equipment.Load(context.Background(), "99")
	if binary.LittleEndian.Uint16(after.Entries["12"].RawEntry[11:13]) != 47 {
		t.Fatal("actual PVF rejected current weapon wear")
	}
}

func TestDurabilityTransportWrapRejectsStaleAndZeroDoesNotUnderflow(t *testing.T) {
	s, se, g := newDurabilityBridgeFixture(t, 2)
	sendDurabilityFrame(t, s, se, 65535, []byte{12}, 1)
	sendDurabilityFrame(t, s, se, 0, []byte{12}, 1)
	stored, _, _ := g.Equipment.Load(context.Background(), "99")
	if stored.Entries["12"].Extra["durability"] != "0" {
		t.Fatal("uint16 rollover lost the second wear event")
	}
	se.conn.(*bufferConn).write.Reset()
	sendDurabilityFrame(t, s, se, 65535, []byte{12}, 1)
	if se.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("stale event replayed its relative ACK")
	}
	sendDurabilityFrame(t, s, se, 1, []byte{12}, 1)
	after, _, _ := g.Equipment.Load(context.Background(), "99")
	if !reflect.DeepEqual(stored, after) {
		t.Fatal("zero wear changed equipment")
	}
	packet, rest := splitLongHengGameServerUpperPacket(t, se.conn.(*bufferConn).write.Bytes())
	if len(rest) != 0 || packet.Header.MsgID != 48 || packet.Header.Classification != 1 || packet.Body[0] != 0 {
		t.Fatal("zero wear must not instruct another client decrement")
	}
}

func TestDurabilityBagZeroSurvivesExpiryAndMaximumOnlyHints(t *testing.T) {
	for _, extra := range []map[string]string{
		{"durability": "0", "max_durability": "48"},
		{"item_kind": "equipment", "max_durability": "48"},
	} {
		raw := make([]byte, 0x77)
		binary.LittleEndian.PutUint32(raw[2:6], 101040493)
		stack := repo.ItemStack{ItemID: 101040493, Count: 1, RawEntry: raw, Extra: extra, ExpireAt: time.Now().Add(time.Hour)}
		entry := currentItemListEntryFromStack(0, 9, stack)
		if binary.LittleEndian.Uint16(entry.data[11:13]) != 0 {
			t.Fatalf("expiry or maximum resurrected broken bag item: %+v", extra)
		}
	}
}
