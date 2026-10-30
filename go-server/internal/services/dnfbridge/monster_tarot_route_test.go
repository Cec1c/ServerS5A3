package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func monsterTarotRouteOp27Body(mode uint16, slot int16) []byte {
	body := make([]byte, 4)
	binary.LittleEndian.PutUint16(body[0:2], mode)
	binary.LittleEndian.PutUint16(body[2:4], uint16(slot))
	return body
}

func monsterTarotRouteOp27Frame(t *testing.T, mode uint16, slot int16, sequence uint16) []byte {
	t.Helper()
	frame, err := dnfproto.BuildChannelPacket(
		uint16(dnfenum.CmdPacketUseLotteryItem),
		monsterTarotRouteOp27Body(mode, slot),
		sequence,
		dnfproto.DefaultChannelClassification,
	)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func TestMonsterTarotRouteOp27FourByteBodyCommitsOnce(t *testing.T) {
	catalog, _ := monsterTarotTestCatalog(t, "[int data]\n600 100 6\n[/int data]\n")
	repositories := monsterTarotTestAssets(t, 234567, 1)
	ctx := context.Background()
	connection := &bufferConn{}
	service := currentBoosterTestService(catalog, repositories)
	// The deployed V179 profile consumes the pending op27 directly after the
	// four-byte request. Keep the test on that profile instead of exercising the
	// historical phase-start branch.
	service.options.currentClientVersion = "official-24.9"
	session := &gameSession{
		conn:                connection,
		connID:              "monster-tarot-route",
		selectedCharacterID: 19,
	}

	if err := service.handleGameUpper(
		session,
		monsterTarotRouteOp27Frame(t, 0, tarotTestSourceSlot, 41),
	); err != nil {
		t.Fatal(err)
	}

	character, found, err := repositories.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("character found=%t err=%v", found, err)
	}
	if got, want := character.Stats["gold"], int64(134567); got != want {
		t.Fatalf("gold=%d, want exactly one 100000 PVF opening cost deducted to %d", got, want)
	}
	if got, want := character.Stats["fatigue"], int64(120); got != want {
		t.Fatalf("fatigue=%d, want unchanged %d", got, want)
	}
	inventory, found, err := repositories.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("inventory found=%t err=%v", found, err)
	}
	if got := monsterTarotInventoryCount(inventory, tarotTestSourceItemID); got != 0 {
		t.Fatalf("monster tarot source count=%d, want consumed", got)
	}
	if got := monsterTarotInventoryCount(inventory, tarotTestRewardItemA); got != 6 {
		t.Fatalf("PVF reward A count=%d, want 6", got)
	}
	if got := monsterTarotInventoryCount(inventory, tarotTestRewardItemB); got != 0 {
		t.Fatalf("unselected PVF reward B count=%d, want 0", got)
	}

	packets := splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
	resultCount := 0
	for _, packet := range packets {
		if packet.Header.Classification != dnfproto.DefaultChannelClassification ||
			packet.Header.MsgID != uint16(dnfenum.CmdPacketUseLotteryItem) {
			continue
		}
		resultCount++
		if len(packet.Body) != 1+2+currentItemListEntryWireSize+4 {
			t.Fatalf("op27 result length=%d body=%x", len(packet.Body), packet.Body)
		}
		if packet.Body[0] != 1 || binary.LittleEndian.Uint16(packet.Body[1:3]) != uint16(tarotTestSourceSlot) {
			t.Fatalf("op27 result prefix=%x", packet.Body[:3])
		}
		row := packet.Body[3 : 3+currentItemListEntryWireSize]
		if binary.LittleEndian.Uint32(row[2:6]) != tarotTestRewardItemA || binary.LittleEndian.Uint32(row[6:10]) != 6 {
			t.Fatalf("op27 reward row item/count=%d/%d", binary.LittleEndian.Uint32(row[2:6]), binary.LittleEndian.Uint32(row[6:10]))
		}
		if currency := binary.LittleEndian.Uint32(packet.Body[3+currentItemListEntryWireSize:]); currency != 0 {
			t.Fatalf("op27 item reward unexpectedly carried currency=%d", currency)
		}
	}
	if resultCount != 1 {
		t.Fatalf("successful op27 responses=%d, want exactly one; packets=%+v", resultCount, packets)
	}

	// Replaying the same four-byte request after the sole card was consumed
	// must not charge again or issue another reward. It must still answer the
	// command rather than hanging the client.
	beforeCharacter, _, err := repositories.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	beforeInventory, _, err := repositories.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	beforeAccount, _, err := repositories.AccountInventory.Load(ctx, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	connection.write.Reset()
	if err := service.handleGameUpper(
		session,
		monsterTarotRouteOp27Frame(t, 0, tarotTestSourceSlot, 42),
	); err != nil {
		t.Fatal(err)
	}
	afterCharacter, _, err := repositories.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	afterInventory, _, err := repositories.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	afterAccount, _, err := repositories.AccountInventory.Load(ctx, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeCharacter, afterCharacter) ||
		!reflect.DeepEqual(beforeInventory, afterInventory) ||
		!reflect.DeepEqual(beforeAccount, afterAccount) {
		t.Fatal("duplicate op27 changed character, inventory, or account assets")
	}

	failurePackets := splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
	if len(failurePackets) != 1 ||
		failurePackets[0].Header.Classification != dnfproto.DefaultChannelClassification ||
		failurePackets[0].Header.MsgID != uint16(dnfenum.CmdPacketUseLotteryItem) ||
		!bytes.Equal(failurePackets[0].Body, []byte{0, 0x13}) {
		t.Fatalf("duplicate request must yield exactly one current op27 rejection: %+v", failurePackets)
	}
}
