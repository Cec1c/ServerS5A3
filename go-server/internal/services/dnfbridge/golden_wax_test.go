package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const currentWaxTestRulesText = `
[grade]
1 10 1 11 20 1 21 30 1 31 40 2 41 50 2 51 60 3 61 70 3 71 80 4 81 85 4 86 90 5
[/grade]
[rarity weight]
1 2 1 2 8 8 6
[/rarity weight]
[extended rarity weight]
0 0 1 1 1 1 1
[/extended rarity weight]
[soul reseal constant]
0 0 1.10 1.10 0 0 1.11
[/soul reseal constant]
[reseal count limit]
1 85 7 86 90 4
[/reseal count limit]
`

func currentWaxTestCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	c, err := newPVFDungeonDropCatalog(equipmentPlacementPVFSource{
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "28309 `character/swordman/weapon/beamsword/beamswd_28309.equ`",
		"stackable/stackable.lst": "14 `cash/candle_goldwax.stk`",
		"equipment/character/swordman/weapon/beamsword/beamswd_28309.equ": "[equipment type] `[weapon]`\n[attach type] `[sealing]`\n[minimum level] 65\n[grade] 69\n[rarity] 3\n[durability] 40",
		"stackable/cash/candle_goldwax.stk":                               "[stackable type] `[etc]` 0\n[stack limit] 10000",
		"etc/reseal.etc":                                                  currentWaxTestRulesText,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func currentWaxBridgeFixture(t *testing.T, catalog *pvfDungeonDropCatalog) (*Service, *gameSession, dnfrepo.Group, []byte, []byte) {
	t.Helper()
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	raw := make([]byte, 0x77)
	binary.LittleEndian.PutUint16(raw, 10)
	binary.LittleEndian.PutUint32(raw[2:], 28309)
	binary.LittleEndian.PutUint32(raw[6:], 462346698)
	raw[10] = 12
	binary.LittleEndian.PutUint16(raw[11:], 37)
	raw[0x13] = 131
	raw[0x57] = 8
	i := dnfrepo.InventoryRecord{CharacterID: "47", Slots: map[string]dnfrepo.ItemStack{"0:10": {ItemID: 28309, Count: 1, RawEntry: raw, Extra: map[string]string{"item_kind": "equipment", "quality_seed": "462346698", "seal_flag": "0", "seal_removed_by_first_equip": "1", "trade_locked_by_first_equip": "1", "instance": "keep"}}, "0:65": {ItemID: 14, Count: 100, Extra: map[string]string{"item_kind": "stackable"}}}}
	if err := g.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "47", AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	if err := g.Inventory.Save(ctx, i); err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{accountID: "account-1", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}, pvfItemCatalog: catalog, repositoryProvider: func() (dnfrepo.Group, bool) { return g, true }}
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 47}
	body := make([]byte, 10)
	binary.LittleEndian.PutUint16(body, 10)
	binary.LittleEndian.PutUint32(body[2:], 28309)
	binary.LittleEndian.PutUint16(body[6:], 65)
	entry := currentItemListEntryFromStack(0, 10, i.Slots["0:10"])
	return s, session, g, body, append([]byte(nil), entry.data[:]...)
}

func requireCurrentWaxSuccess(t *testing.T, s *Service, session *gameSession, g dnfrepo.Group, body, before []byte) {
	t.Helper()
	if err := s.handleCurrentResetItemAttr(session, body); err != nil {
		t.Fatal(err)
	}
	conn := session.conn.(*bufferConn)
	ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.MsgID != 81 || ack.Header.Classification != 1 || len(ack.Body) != 9 || ack.Body[0] != 1 || binary.LittleEndian.Uint16(ack.Body[1:]) != 65 || binary.LittleEndian.Uint32(ack.Body[3:]) != 94 || binary.LittleEndian.Uint16(ack.Body[7:]) != 1 {
		t.Fatalf("native seal ACK header=%+v body=%x", ack.Header, ack.Body)
	}
	update, tail := splitGameServerUpperPacket(t, rest)
	if len(tail) != 0 || update.Header.Classification != 0 || update.Header.MsgID != 14 || len(update.Body) != 3+0x77 || update.Body[0] != 0 || binary.LittleEndian.Uint16(update.Body[1:]) != 1 {
		t.Fatalf("target refresh header=%+v body=%x", update.Header, update.Body)
	}
	want := append([]byte(nil), before...)
	want[10] = 44
	want[13] = 1
	if !reflect.DeepEqual(want, update.Body[3:]) {
		t.Fatalf("refresh changed unrelated instance bytes\nwant=%x\ngot =%x", want, update.Body[3:])
	}
	i, _, err := g.Inventory.Load(context.Background(), "47")
	if err != nil || i.Slots["0:65"].Count != 94 || i.Slots["0:10"].Extra["quality_seed"] != "462346698" {
		t.Fatalf("saved inventory err=%v slots=%+v", err, i.Slots)
	}
	conn.write.Reset()
	if err = s.handleCurrentResetItemAttr(session, body); err != nil {
		t.Fatal(err)
	}
	repeat, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if repeat.Body[0] != 0 {
		t.Fatal("repeat accepted")
	}
	i, _, _ = g.Inventory.Load(context.Background(), "47")
	if i.Slots["0:65"].Count != 94 {
		t.Fatal("repeat consumed more wax")
	}
}

func TestCurrentGoldenWaxSendsSealResultAndPreservesQuality(t *testing.T) {
	s, session, g, body, before := currentWaxBridgeFixture(t, currentWaxTestCatalog(t))
	requireCurrentWaxSuccess(t, s, session, g, body, before)
}

func TestRealPVFGoldenWaxResealsAncientWishWithNativeReply(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("set runtime PVF")
	}
	a, err := platformpvf.LoadArchive(platformpvf.Options{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	c, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := loadCurrentGoldenWaxRules(a)
	if err != nil {
		t.Fatal(err)
	}
	d, err := (currentGoldenWaxCatalog{c}).ResolveWaxItem(28309)
	if err != nil || d.MinimumLevel != 65 || d.Rarity != 3 || !d.Equipment || !d.Resealable {
		t.Fatalf("real target=%+v err=%v", d, err)
	}
	if cost, limit, err := rules.WaxCost(d.MinimumLevel, d.Rarity, 0); err != nil || cost != 6 || limit != 7 {
		t.Fatalf("real cost=%d limit=%d err=%v", cost, limit, err)
	}
	w, err := (currentGoldenWaxCatalog{c}).ResolveWaxItem(14)
	if err != nil || !w.GoldenWax || w.Equipment {
		t.Fatalf("real wax=%+v err=%v", w, err)
	}
	s, session, g, body, before := currentWaxBridgeFixture(t, c)
	requireCurrentWaxSuccess(t, s, session, g, body, before)
}

type currentWaxWriteFailConn struct{ bufferConn }

func (c *currentWaxWriteFailConn) Write([]byte) (int, error) {
	return 0, errors.New("wax ACK write failed")
}
func TestCurrentGoldenWaxWriteFailureCannotChargeTwice(t *testing.T) {
	s, session, g, body, _ := currentWaxBridgeFixture(t, currentWaxTestCatalog(t))
	session.conn = &currentWaxWriteFailConn{}
	if err := s.handleCurrentResetItemAttr(session, body); err == nil {
		t.Fatal("write error ignored")
	}
	i, _, _ := g.Inventory.Load(context.Background(), "47")
	if i.Slots["0:65"].Count != 94 || i.Slots["0:10"].Extra["seal_flag"] != "1" {
		t.Fatal("committed seal missing")
	}
	conn := &bufferConn{}
	session.conn = conn
	if err := s.handleCurrentResetItemAttr(session, body); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Body[0] != 0 {
		t.Fatal("retry charged")
	}
	i, _, _ = g.Inventory.Load(context.Background(), "47")
	if i.Slots["0:65"].Count != 94 {
		t.Fatal("double charge")
	}
}

func TestCurrentGoldenWaxRejectsUnknownModeWithoutGradeFallback(t *testing.T) {
	s, session, g, body, _ := currentWaxBridgeFixture(t, currentWaxTestCatalog(t))
	binary.LittleEndian.PutUint16(body[8:], 1)
	before, _, _ := g.Inventory.Load(context.Background(), "47")
	if err := s.handleCurrentResetItemAttr(session, body); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
	if ack.Body[0] != 0 {
		t.Fatal("unknown mode accepted")
	}
	after, _, _ := g.Inventory.Load(context.Background(), "47")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("unknown mode mutated inventory")
	}
}
