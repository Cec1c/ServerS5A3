package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestCurrentNPCShopMergedBuyACKUsesPurchaseQuantityAndUpdateUsesTotal(t *testing.T) {
	shop, items := mustCurrentNPCShopTestCatalogs(t)
	repos := mustCurrentNPCShopRepositories(t, 1000, map[string]dnfrepo.ItemStack{"0:65": {ItemID: 600, Count: 5}})
	s := currentNPCShopTestService(repos, shop, items)
	c := &bufferConn{}
	session := &gameSession{conn: c, connID: "npc-shop-ack-quantity", selectedCharacterID: 19}
	if err := s.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketBuyItem), currentNPCShopBuyTestBody(600, 3, 1, 326)); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, c.write.Bytes())
	if got := binary.LittleEndian.Uint32(ack.Body[23:27]); got != 3 {
		t.Fatalf("current EXE adds ACK quantity to quota: got=%d want purchased3, not inventory8", got)
	}
	update, trailing := splitGameServerUpperPacket(t, rest)
	if len(trailing) != 0 {
		t.Fatalf("unexpected extra response bytes=%d", len(trailing))
	}
	assertCurrentNPCShopIncrementalRows(t, update.Body, 700, 65, 600, 8)
	inventory, found, err := repos.Inventory.Load(context.Background(), "19")
	if err != nil || !found || inventory.Slots["0:65"].Count != 8 {
		t.Fatalf("durable inventory=%+v found=%t err=%v", inventory, found, err)
	}
	row := append([]byte(nil), ack.Body[17:17+currentItemListEntryWireSize]...)
	binary.LittleEndian.PutUint32(row[6:10], 8)
	if !bytes.Equal(row, update.Body[3+currentItemListEntryWireSize:]) {
		t.Fatal("ACK changed item identity or expiration beyond purchase quantity")
	}
}

func TestCurrentNPCShopRealPVFMergedBuyACKCountsEachPurchaseOnce(t *testing.T) {
	shop, items := realPVF535(t)
	repos := mustCurrentNPCShopRepositories(t, 1000000, nil)
	s := currentNPCShopTestService(repos, shop, items)
	c := &bufferConn{}
	session := &gameSession{conn: c, connID: "npc-shop-ack-real-pvf", selectedCharacterID: 19}
	var nativeCount uint32
	var slot int16
	for total := uint32(1); total <= 2; total++ {
		c.write.Reset()
		if err := s.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketBuyItem), currentNPCShopBuyTestBody(490010214, 1, 400000044, 0)); err != nil {
			t.Fatal(err)
		}
		ack, rest := splitGameServerUpperPacket(t, c.write.Bytes())
		if ack.Header.MsgID != uint16(dnfenum.CmdPacketBuyItem) || ack.Body[0] != 1 {
			t.Fatalf("purchase failed: %+v", ack.Header)
		}
		nativeCount += binary.LittleEndian.Uint32(ack.Body[23:27])
		if nativeCount != total {
			t.Fatalf("current EXE quota increments: after%d buys=%d want%d", total, nativeCount, total)
		}
		currentSlot := int16(binary.LittleEndian.Uint16(ack.Body[17:19]))
		if total > 1 && currentSlot != slot {
			t.Fatal("second purchase did not merge")
		}
		slot = currentSlot
		update, trailing := splitGameServerUpperPacket(t, rest)
		if len(trailing) != 0 {
			t.Fatal("unexpected extra response")
		}
		assertCurrentNPCShopIncrementalRows(t, update.Body, 1000000-total*200000, slot, 490010214, total)
	}
	c.write.Reset()
	request := make([]byte, 4)
	binary.LittleEndian.PutUint32(request, 3136)
	if err := s.handleCurrentNPCShopPurchaseCount(session, request); err != nil {
		t.Fatal(err)
	}
	count, trailing := splitGameServerUpperPacket(t, c.write.Bytes())
	if len(trailing) != 0 || !bytes.Equal(count.Body, buildCurrentNPCShopPurchaseCountBody(map[uint32]uint32{490010214: 2})) {
		t.Fatal("durable quota disagrees with native increments")
	}
}

func TestCurrentNPCShopMergedMaterialExchangeACKKeepsCostAndInventoryTotals(t *testing.T) {
	shop, items := mustCurrentNPCShopTestCatalogs(t)
	repos := mustCurrentNPCShopRepositories(t, 1000, map[string]dnfrepo.ItemStack{"0:3": {ItemID: 602, Count: 5}, "0:121": {ItemID: 900, Count: 10}})
	s := currentNPCShopTestService(repos, shop, items)
	c := &bufferConn{}
	session := &gameSession{conn: c, connID: "npc-shop-ack-material", selectedCharacterID: 19}
	if err := s.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketBuyItem), currentNPCShopBuyTestBody(602, 2, 1, 326)); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, c.write.Bytes())
	if ack.Body[0] != 1 || binary.LittleEndian.Uint32(ack.Body[23:27]) != 2 {
		t.Fatal("exchange ACK does not carry purchase quantity2")
	}
	if binary.LittleEndian.Uint32(ack.Body[len(ack.Body)-8:]) != 900 || binary.LittleEndian.Uint32(ack.Body[len(ack.Body)-4:]) != 6 {
		t.Fatal("exchange ACK cost balance changed")
	}
	update, trailing := splitGameServerUpperPacket(t, rest)
	if len(trailing) != 0 || update.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
		t.Fatal("missing authoritative inventory update")
	}
	inventory, found, err := repos.Inventory.Load(context.Background(), "19")
	if err != nil || !found || inventory.Slots["0:3"].Count != 7 || inventory.Slots["0:121"].Count != 6 {
		t.Fatalf("durable exchange changed: %+v err=%v", inventory, err)
	}
	seen := false
	for i := 3; i+currentItemListEntryWireSize <= len(update.Body); i += currentItemListEntryWireSize {
		row := update.Body[i : i+currentItemListEntryWireSize]
		if binary.LittleEndian.Uint32(row[2:6]) == 602 {
			seen = true
			if binary.LittleEndian.Uint32(row[6:10]) != 7 {
				t.Fatal("exchange update lost full total")
			}
		}
	}
	if !seen {
		t.Fatal("exchange update omitted bought item")
	}
}
