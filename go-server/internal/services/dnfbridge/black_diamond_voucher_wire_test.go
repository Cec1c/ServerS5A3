package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Exercise the bridge entry and the complete reply stream with the real paired
// PVF, rather than calling inventory.Owner directly.

func blackDiamondShopWirePackets(t *testing.T, wire []byte) []dnfproto.ChannelPacket {
	t.Helper()
	packets := make([]dnfproto.ChannelPacket, 0, 4)
	for len(wire) != 0 {
		before := len(wire)
		packet, rest := splitGameServerUpperPacket(t, wire)
		if len(rest) >= before {
			t.Fatalf("game-upper wire stalled: before=%d after=%d", before, len(rest))
		}
		packets = append(packets, packet)
		wire = rest
	}
	return packets
}

func blackDiamondShopAssertEmptyMainSlot(
	t *testing.T,
	packet dnfproto.ChannelPacket,
	slot int16,
) {
	t.Helper()
	if packet.Header.Classification != 0 ||
		packet.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
		t.Fatalf("source-slot refresh header=%+v, want class0/op14", packet.Header)
	}
	if len(packet.Body) != 3+currentItemListEntryWireSize {
		t.Fatalf("source-slot refresh body len=%d, want=%d body=%x",
			len(packet.Body), 3+currentItemListEntryWireSize, packet.Body)
	}
	if packet.Body[0] != dnfrepo.MainInventoryListType ||
		binary.LittleEndian.Uint16(packet.Body[1:3]) != 1 ||
		int16(binary.LittleEndian.Uint16(packet.Body[3:5])) != slot ||
		binary.LittleEndian.Uint32(packet.Body[5:9]) != math.MaxUint32 ||
		binary.LittleEndian.Uint32(packet.Body[9:13]) != 0 {
		t.Fatalf("source-slot refresh body=%x, want one deleted list0/slot%d row", packet.Body, slot)
	}
}

func blackDiamondShopAssertPremiumNotification(
	t *testing.T,
	packet dnfproto.ChannelPacket,
) int64 {
	t.Helper()
	if packet.Header.Classification != 0 ||
		packet.Header.MsgID != currentPremiumActivatedMsgID {
		t.Fatalf("premium notification header=%+v, want class0/op66", packet.Header)
	}
	if len(packet.Body) != 11 ||
		packet.Body[0] != 2 ||
		packet.Body[1] != 0 ||
		int64(packet.Body[2]) != premium.WireTypeBlackDiamond {
		t.Fatalf("Black Diamond native56 notification body=%x", packet.Body)
	}
	remaining := int64(binary.LittleEndian.Uint64(packet.Body[3:11]))
	const thirtyDays = int64(30 * 86400)
	if remaining < thirtyDays-30 || remaining > thirtyDays {
		t.Fatalf("premium17 remaining=%d, want about 30 days", remaining)
	}
	return remaining
}

func blackDiamondShopAssertFatigue188(t *testing.T, packet dnfproto.ChannelPacket) {
	t.Helper()
	if packet.Header.Classification != 0 || packet.Header.MsgID != currentFatigueMsgID {
		t.Fatalf("fatigue refresh header=%+v, want class0/op36", packet.Header)
	}
	if len(packet.Body) != 10 {
		t.Fatalf("fatigue refresh body len=%d, want 10 body=%x", len(packet.Body), packet.Body)
	}
	// Native op36 carries used/limit, not remaining/limit.
	if binary.LittleEndian.Uint16(packet.Body[0:2]) != 0 ||
		binary.LittleEndian.Uint16(packet.Body[2:4]) != 188 {
		t.Fatalf("fatigue refresh body=%x, want full 188/188", packet.Body)
	}
}

func blackDiamondShopAssertNoPhysicalContractItem(
	t *testing.T,
	f *blackDiamondShopFixture,
	itemID int64,
) {
	t.Helper()
	ctx := context.Background()

	inventory, found, err := f.repositories.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load character inventory found=%t err=%v", found, err)
	}
	for key, stack := range inventory.Slots {
		if stack.ItemID == itemID {
			t.Fatalf("contract item %d leaked into character inventory slot %s: %+v", itemID, key, stack)
		}
	}
	for key, stack := range inventory.Warehouse {
		if stack.ItemID == itemID {
			t.Fatalf("contract item %d leaked into character warehouse slot %s: %+v", itemID, key, stack)
		}
	}

	if f.repositories.AccountInventory != nil {
		accountInventory, found, err := f.repositories.AccountInventory.Load(
			ctx,
			blackDiamondShopAccountID,
		)
		if err != nil {
			t.Fatalf("load account inventory: %v", err)
		}
		if found {
			for key, stack := range accountInventory.Slots {
				if stack.ItemID == itemID {
					t.Fatalf("contract item %d leaked into account inventory slot %s: %+v", itemID, key, stack)
				}
			}
		}
	}

	if f.repositories.Mailbox != nil {
		mailbox, found, err := f.repositories.Mailbox.Load(ctx, "19")
		if err != nil {
			t.Fatalf("load mailbox: %v", err)
		}
		if found {
			for mailID, mail := range mailbox.Mails {
				for _, attachment := range mail.Attachments {
					if attachment.ItemID == itemID {
						t.Fatalf("contract item %d leaked into mailbox %s: %+v", itemID, mailID, attachment)
					}
				}
			}
		}
	}
}

// TestBridgeBlackDiamondVoucherOp518ActivatesInternalContractAndRefreshesFatigue
// is the primary production-wire test. The patched PVF must define:
//
//	490709930 "[usable cera package]" -> package data 490709932 1
//	490709932 premiumlist_new.etc type 17 for 30 days
//
// Voucher 490709930 remains excluded from grant-time auto activation.
func TestBridgeBlackDiamondVoucherOp518ActivatesInternalContractAndRefreshesFatigue(t *testing.T) {
	t.Setenv("DNFBRIDGE_ITEM_GRANT_NOTICE", itemGrantNoticeDelta)
	const internalContractID int64 = 490709932
	if got := uint16(dnfenum.CmdPacketOpenCerapackage); got != 518 {
		t.Fatalf("current op518 identity changed: %d", got)
	}

	f := newBlackDiamondShopFixture(t, 0, 0, 0)
	f.seedPackage(t)
	if f.premiumExpiry(t) != 0 {
		t.Fatal("buying the outer package activated premium")
	}
	f.openPackage(t)
	if f.premiumExpiry(t) != 0 {
		t.Fatal("opening the outer package activated premium")
	}

	voucherSlot, voucherStack := blackDiamondShopMainStack(
		t,
		f.inventory(t),
		blackDiamondShopVoucherID,
	)
	if voucherStack.Count != 1 {
		t.Fatalf("outer package delivered voucher count=%d, want 1", voucherStack.Count)
	}
	definition, err := resolveCurrentCeraPackageDefinition(
		f.shop.items,
		blackDiamondShopVoucherID,
	)
	if err != nil {
		t.Fatalf("resolve real voucher package %d: %v", blackDiamondShopVoucherID, err)
	}
	if normalizeMagicBoxPVFType(definition.Source.StackableType) != "usable cera package" ||
		len(definition.Rewards) != 1 ||
		int64(definition.Rewards[0].Definition.ItemID) != internalContractID ||
		definition.Rewards[0].Count != 1 ||
		len(definition.AvatarItemIDs) != 0 {
		t.Fatalf("voucher package definition/source=%+v rewards=%+v avatars=%v",
			definition.Source, definition.Rewards, definition.AvatarItemIDs)
	}

	contract, found := f.service.premiumCatalog.contractsByItem[internalContractID]
	if !found ||
		contract.PremiumType != premium.TypeBlackDiamond ||
		contract.DurationSeconds != 30*86400 {
		t.Fatalf("internal voucher contract=%+v found=%t", contract, found)
	}
	automatic := currentPremiumInventoryContracts(f.service.premiumCatalog)
	if _, found := automatic[int64(blackDiamondShopVoucherID)]; found {
		t.Fatalf("outer voucher %d is in the grant-time auto-activation set", blackDiamondShopVoucherID)
	}
	if _, found := automatic[internalContractID]; !found {
		t.Fatalf("internal contract %d is absent from the grant-time auto-activation set", internalContractID)
	}

	f.connection.write.Reset()
	before := time.Now().UTC().Unix()
	if err := f.service.handleGameCommand(
		f.session,
		1,
		518,
		blackDiamondShopOpenBody(voucherSlot),
	); err != nil {
		t.Fatalf("use real voucher through op518 at slot %d: %v", voucherSlot, err)
	}
	packets := blackDiamondShopWirePackets(t, f.connection.write.Bytes())
	if len(packets) != 4 {
		t.Fatalf("op518 wire packet count=%d, want ACK/op14/NOTI0x42/fatigue188: %+v",
			len(packets), packets)
	}

	ack := packets[0]
	wantAck := []byte{1, byte(voucherSlot), byte(uint16(voucherSlot) >> 8)}
	if ack.Header.Classification != dnfproto.DefaultChannelClassification ||
		ack.Header.MsgID != 518 ||
		!bytes.Equal(ack.Body, wantAck) {
		t.Fatalf("op518 ACK header=%+v body=%x want=%x", ack.Header, ack.Body, wantAck)
	}
	blackDiamondShopAssertEmptyMainSlot(t, packets[1], voucherSlot)
	blackDiamondShopAssertPremiumNotification(t, packets[2])
	blackDiamondShopAssertFatigue188(t, packets[3])

	if _, found := blackDiamondShopMainStackOptional(
		f.inventory(t),
		blackDiamondShopVoucherID,
	); found {
		t.Fatal("actively used voucher remained in inventory")
	}
	firstExpiry := f.premiumExpiry(t)
	if firstExpiry < before+30*86400-30 || firstExpiry > time.Now().UTC().Unix()+30*86400 {
		t.Fatalf("premium17 expiry=%d before=%d, want about 30 days", firstExpiry, before)
	}
	character, found, err := f.repositories.Character.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	if character.Stats["fatigue"] != 188 || character.Stats["fatigue_limit"] != 188 {
		t.Fatalf("persisted fatigue=%d/%d, want 188/188",
			character.Stats["fatigue"], character.Stats["fatigue_limit"])
	}
	blackDiamondShopAssertNoPhysicalContractItem(t, f, internalContractID)
	blackDiamondShopAssertNoPhysicalContractItem(t, f, int64(blackDiamondShopVoucherID))

	// Repeating the now-empty source slot must be answered as a package failure
	// and must not activate or extend the account contract again.
	f.connection.write.Reset()
	if err := f.service.handleGameCommand(
		f.session,
		1,
		518,
		blackDiamondShopOpenBody(voucherSlot),
	); err != nil {
		t.Fatalf("replay empty op518 slot: %v", err)
	}
	replayed := blackDiamondShopWirePackets(t, f.connection.write.Bytes())
	if len(replayed) != 1 ||
		replayed[0].Header.Classification != dnfproto.DefaultChannelClassification ||
		replayed[0].Header.MsgID != 518 ||
		!bytes.Equal(replayed[0].Body, []byte{0, 4}) {
		t.Fatalf("empty-slot op518 replay=%+v, want one class1/op518 failure body 0004", replayed)
	}
	if got := f.premiumExpiry(t); got != firstExpiry {
		t.Fatalf("empty-slot op518 replay extended expiry from %d to %d", firstExpiry, got)
	}
	blackDiamondShopAssertNoPhysicalContractItem(t, f, internalContractID)
	blackDiamondShopAssertNoPhysicalContractItem(t, f, int64(blackDiamondShopVoucherID))
}

// TestBridgeBlackDiamondVoucherOp44CompatibilityKeepsActiveUsePath pins the
// legacy active-use route while the delivered voucher remains excluded from
// acquisition-time auto activation.
func TestBridgeBlackDiamondVoucherOp44CompatibilityKeepsActiveUsePath(t *testing.T) {
	if got := uint16(dnfenum.CmdPacketUseStackable); got != 44 {
		t.Fatalf("current op44 identity changed: %d", got)
	}
	f := newBlackDiamondShopFixture(t, 0, 0, 0)
	f.seedPackage(t)
	if f.premiumExpiry(t) != 0 {
		t.Fatal("buying the outer package activated premium")
	}
	f.openPackage(t)
	if f.premiumExpiry(t) != 0 {
		t.Fatal("opening the outer package activated premium")
	}

	slot, stack := blackDiamondShopMainStack(t, f.inventory(t), blackDiamondShopVoucherID)
	if stack.Count != 1 {
		t.Fatalf("outer package delivered voucher count=%d, want 1", stack.Count)
	}
	automatic := currentPremiumInventoryContracts(f.service.premiumCatalog)
	if _, found := automatic[int64(blackDiamondShopVoucherID)]; found {
		t.Fatal("outer voucher is in the acquisition-time auto-activation set")
	}

	const (
		nonzeroInstance int32 = 0x49070930
	)
	itemID := int32(blackDiamondShopVoucherID)
	requestBody := buildRejectedUseStackableBody(slot, 0, nonzeroInstance, itemID)

	f.connection.write.Reset()
	before := time.Now().UTC().Unix()
	if err := f.service.handleGameCommand(f.session, 1, 44, requestBody); err != nil {
		t.Fatalf("use real voucher through op44 at slot %d: %v", slot, err)
	}
	packets := blackDiamondShopWirePackets(t, f.connection.write.Bytes())
	if len(packets) != 4 {
		t.Fatalf("op44 wire packet count=%d, want ACK/NOTI0x42/op14/fatigue188: %+v",
			len(packets), packets)
	}

	ack := packets[0]
	wantAck := make([]byte, 12)
	wantAck[0] = 1
	binary.LittleEndian.PutUint16(wantAck[1:3], uint16(slot))
	wantAck[3] = 0
	binary.LittleEndian.PutUint32(wantAck[4:8], uint32(nonzeroInstance))
	binary.LittleEndian.PutUint32(wantAck[8:12], uint32(itemID))
	if ack.Header.Classification != dnfproto.DefaultChannelClassification ||
		ack.Header.MsgID != 44 ||
		!bytes.Equal(ack.Body, wantAck) {
		t.Fatalf("op44 ACK header=%+v body=%x want=%x", ack.Header, ack.Body, wantAck)
	}
	blackDiamondShopAssertPremiumNotification(t, packets[1])
	blackDiamondShopAssertEmptyMainSlot(t, packets[2], slot)
	blackDiamondShopAssertFatigue188(t, packets[3])

	if _, found := blackDiamondShopMainStackOptional(
		f.inventory(t),
		blackDiamondShopVoucherID,
	); found {
		t.Fatal("actively used voucher remained in inventory")
	}
	firstExpiry := f.premiumExpiry(t)
	if firstExpiry < before+30*86400-30 || firstExpiry > time.Now().UTC().Unix()+30*86400 {
		t.Fatalf("premium17 expiry=%d before=%d, want about 30 days", firstExpiry, before)
	}

	f.connection.write.Reset()
	if err := f.service.handleGameCommand(f.session, 1, 44, requestBody); err != nil {
		t.Fatalf("replay empty op44 slot: %v", err)
	}
	replayed := blackDiamondShopWirePackets(t, f.connection.write.Bytes())
	wantFailure := append([]byte(nil), requestBody[:11]...)
	wantFailure[0] = 0
	wantFailure[1] = currentUseStackableRejectionCode
	if len(replayed) != 1 ||
		replayed[0].Header.Classification != dnfproto.DefaultChannelClassification ||
		replayed[0].Header.MsgID != 44 ||
		!bytes.Equal(replayed[0].Body, wantFailure) {
		t.Fatalf("empty-slot op44 replay=%+v body want=%x", replayed, wantFailure)
	}
	if got := f.premiumExpiry(t); got != firstExpiry {
		t.Fatalf("empty-slot op44 replay extended expiry from %d to %d", firstExpiry, got)
	}
}

func TestBridgeBlackDiamondVoucherOp518RenewalDoesNotRefillSpentFatigue(t *testing.T) {
	f := newBlackDiamondShopFixture(t, 0, 0, 0)
	use := func() {
		t.Helper()
		f.seedPackage(t)
		f.openPackage(t)
		slot, _ := blackDiamondShopMainStack(t, f.inventory(t), blackDiamondShopVoucherID)
		f.connection.write.Reset()
		if err := f.service.handleGameCommand(f.session, 1, 518, blackDiamondShopOpenBody(slot)); err != nil {
			t.Fatal(err)
		}
		packets := blackDiamondShopWirePackets(t, f.connection.write.Bytes())
		if len(packets) != 4 || packets[0].Body[0] != 1 {
			t.Fatalf("use reply=%+v", packets)
		}
	}
	use()
	first := f.premiumExpiry(t)
	ctx := context.Background()
	character, _, err := f.repositories.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	character.Stats["fatigue"] = 100
	character.Stats["fatigue_used"] = 88
	character.Stats["fatigue_display_update"] = 88
	if err := f.repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	use()
	if got := f.premiumExpiry(t); got != first+30*86400 {
		t.Fatalf("renewal expiry=%d want=%d", got, first+30*86400)
	}
	character, _, err = f.repositories.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if character.Stats["fatigue_limit"] != 188 || character.Stats["fatigue"] != 100 {
		t.Fatalf("renewal refilled spent fatigue: %+v", character.Stats)
	}
	blackDiamondShopAssertNoPhysicalContractItem(t, f, 490709932)
}
