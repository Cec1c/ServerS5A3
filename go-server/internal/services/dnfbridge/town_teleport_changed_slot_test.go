package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestTeleportPotionRefreshesOnlyActualConsumedSlot(t *testing.T) {
	for _, remaining := range []int64{1, 2} {
		service, session, repos := newTeleportPotionTest(t)
		inventory, _, err := repos.Inventory.Load(context.Background(), "29")
		if err != nil {
			t.Fatal(err)
		}
		potion := inventory.Slots["0:65"]
		potion.Count = remaining
		inventory.Slots["0:65"] = potion
		inventory.Slots["0:66"] = dnfrepo.ItemStack{ItemID: 37, Count: 17}
		if err := repos.Inventory.Save(context.Background(), inventory); err != nil {
			t.Fatal(err)
		}
		// Mismatched UI slot exercises the existing real item lookup. The wire
		// refresh must name slot65 actually debited, not claimed slot8.
		if err := service.handleTeleportPotion(session, buildTeleportPotionRequest(8, 2600014, 40)); err != nil {
			t.Fatal(err)
		}
		packets := splitAllGameServerUpperPackets(t, session.conn.(*bufferConn).write.Bytes())
		updates := 0
		sawAck := false
		for _, packet := range packets {
			if packet.Header.Classification != 0 {
				if packet.Header.MsgID == uint16(dnfenum.CmdPacketTeleport) {
					sawAck = true
				}
				continue
			}
			if packet.Header.MsgID == uint16(dnfenum.CmdPacketLeaveParty) {
				t.Fatal("live teleport rebuilt full containers")
			}
			if packet.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
				continue
			}
			updates++
			b := packet.Body
			if len(b) != 3+currentItemListEntryWireSize || b[0] != 0 || binary.LittleEndian.Uint16(b[1:3]) != 1 || binary.LittleEndian.Uint16(b[3:5]) != 65 {
				t.Fatalf("potion refresh is not one actual row: %x", b)
			}
			item := binary.LittleEndian.Uint32(b[5:9])
			count := binary.LittleEndian.Uint32(b[9:13])
			if remaining == 2 && (item != 2600014 || count != 1) {
				t.Fatal("remaining potion projection invalid")
			}
			if remaining == 1 && (item != ^uint32(0) || count != 0) {
				t.Fatal("last potion was not cleared")
			}
		}
		if updates != 1 || !sawAck {
			t.Fatalf("updates=%d ack=%t", updates, sawAck)
		}
		stored, _, err := repos.Inventory.Load(context.Background(), "29")
		if err != nil || stored.Slots["0:66"].Count != 17 || stored.Slots["0:65"].Count != remaining-1 {
			t.Fatal("unrelated item changed or debit wrong")
		}
	}
}

func TestTeleportPotionDoesNotConsumeOtherContainers(t *testing.T) {
	service, session, repos := newTeleportPotionTest(t)
	i, _, _ := repos.Inventory.Load(context.Background(), "29")
	p := i.Slots["0:65"]
	delete(i.Slots, "0:65")
	i.Slots["1:65"] = p
	if err := repos.Inventory.Save(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	if err := service.handleTeleportPotion(session, buildTeleportPotionRequest(65, 2600014, 40)); err != nil {
		t.Fatal(err)
	}
	assertTeleportPotionFailure(t, session)
	stored, _, _ := repos.Inventory.Load(context.Background(), "29")
	if stored.Slots["1:65"].Count != 2 {
		t.Fatal("wrong-container potion consumed")
	}
}
