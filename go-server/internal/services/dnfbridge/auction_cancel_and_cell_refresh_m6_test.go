package dnfbridge

// The m6 closing pass: one file for the operator reports that are all about a
// *committed* change not being reflected - the cancel notice text and the two
// "the item is back but the bag does not show it" cases.
//
// Every rule here is pinned to client evidence, not to taste:
//
//   - 60091 / 60089 / 60088 are three of the six texts FUN_01d80150 can render,
//     switched on by the notice's own kind byte;
//   - item id -1 is the only value sub_1D73120 treats as a cell delete
//     (`0x1D73912`), and 0 leaves the old object alive so the next row for that
//     cell is dropped by the id-changed check at `0x1D73AB2`.
//
// The third m6 report - a manual cancel forfeiting the 10000 deposit - is a
// domain rule and is pinned in internal/modules/dnf/auction/owner_test.go, next
// to the client dialog text (dstr 60149 「...且无法获得缴纳的押金。」) that
// states it.

import (
	"bytes"
	"context"
	"encoding/binary"

	"testing"

	dnfauction "longheng.io/server/internal/modules/dnf/auction"
	"longheng.io/server/internal/modules/dnf/dnfenum"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// ---------------------------------------------------------------------------
// N4: the source-slot row a listing emits must be a real delete.
// ---------------------------------------------------------------------------

// TestCurrentAuctionRegisterClearsTheSourceSlotWithTheDeleteRow pins the fix for
// "the auction-returned item is invisible in the bag until you reselect".
//
// The registration's op14 row for the emptied source cell used to carry item id
// 0. The client's list-0 reader only removes an object when the id is exactly -1,
// so the old object survived; the mail claim's later row for that cell then hit
// the "id changed" branch, which drops rows whose object type is not 2. Net
// effect: durable state correct, client cell never repainted.
func TestCurrentAuctionRegisterClearsTheSourceSlotWithTheDeleteRow(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedCurrentAuctionBridgeCharacter(t, ctx, repositories, "10", "account-seller", "Seller", 20_000, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 6001, Count: 2, RawEntry: make([]byte, currentItemListEntryWireSize)},
	})
	service := &Service{
		options:                options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider:     func() (dnfrepo.Group, bool) { return repositories, true },
		auctionItemEligibility: func(itemID uint32) bool { return itemID == 6001 },
	}
	seedAuctionSequenceForBridge(t, service, 400)
	connection := &bufferConn{}
	seller := &gameSession{conn: connection, selectedCharacterID: 10, accountID: "account-seller"}
	if err := service.registerCurrentAuctionItemLocked(seller, buildCurrentAuctionRegisterRequestForTest(9, 6001, 2, -1, 75, 75, 24)); err != nil {
		t.Fatal(err)
	}
	// ACK, then the source-cell row, then the wallet row.
	ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketAuctionRegistItem) {
		t.Fatalf("register ACK header=%+v", ack.Header)
	}
	slotRow, rest := splitGameServerUpperPacket(t, rest)
	if slotRow.Header.MsgID != 14 {
		t.Fatalf("source cell row msg id=%d want 14", slotRow.Header.MsgID)
	}
	listType, slot, itemID, amount := decodeSingleItemUpdateRow(t, slotRow.Body)
	if listType != 0 || slot != 9 {
		t.Fatalf("source cell row list=%d slot=%d want list 0 slot 9", listType, slot)
	}
	if itemID != 0xFFFFFFFF {
		t.Fatalf("source cell delete row itemID=0x%08X want 0xFFFFFFFF (-1); 0 leaves the old object alive", itemID)
	}
	if amount != 0 {
		t.Fatalf("delete row amount=%d want 0", amount)
	}
	// The wallet row still follows.
	wallet, trailing := splitGameServerUpperPacket(t, rest)
	if len(trailing) != 0 {
		t.Fatalf("registration left trailing bytes: %x", trailing)
	}
	if wallet.Header.MsgID != 14 {
		t.Fatalf("wallet row msg id=%d want 14", wallet.Header.MsgID)
	}
	_, walletSlot, walletItem, gold := decodeSingleItemUpdateRow(t, wallet.Body)
	wantGold := uint32(20_000 - dnfauction.ListingDepositGold)
	if walletSlot != 0 || walletItem != 0 || gold != wantGold {
		t.Fatalf("wallet row slot=%d item=%d gold=%d, want 0/0/%d", walletSlot, walletItem, gold, wantGold)
	}
}

func decodeSingleItemUpdateRow(t *testing.T, body []byte) (listType byte, slot uint16, itemID uint32, amount uint32) {
	t.Helper()
	rows := decodeItemUpdateRows(t, body)
	if len(rows) != 1 {
		t.Fatalf("item update entries=%d want 1", len(rows))
	}
	row := rows[0]
	return body[0], row.slot, row.itemID, row.amount
}

type m6ItemUpdateRow struct {
	slot   uint16
	itemID uint32
	amount uint32
}

func decodeItemUpdateRows(t *testing.T, body []byte) []m6ItemUpdateRow {
	t.Helper()
	if len(body) < 3 {
		t.Fatalf("item update body too short: %d", len(body))
	}
	count := int(binary.LittleEndian.Uint16(body[1:3]))
	if len(body) != 3+count*currentItemListEntryWireSize {
		t.Fatalf("item update body len=%d want %d for %d entries",
			len(body), 3+count*currentItemListEntryWireSize, count)
	}
	rows := make([]m6ItemUpdateRow, 0, count)
	for index := 0; index < count; index++ {
		row := body[3+index*currentItemListEntryWireSize:]
		rows = append(rows, m6ItemUpdateRow{
			slot:   binary.LittleEndian.Uint16(row[0x00:0x02]),
			itemID: binary.LittleEndian.Uint32(row[0x02:0x06]),
			amount: binary.LittleEndian.Uint32(row[0x06:0x0A]),
		})
	}
	return rows
}

// ---------------------------------------------------------------------------
// N2: the cancel/expiry/purchase banners are three different texts.
// ---------------------------------------------------------------------------

// TestCurrentAuctionDeliveryNoticesNameTheRightText pins the mapping onto the
// six strings FUN_01d80150 can render. Every return delivery used to be
// announced as kind 2, so a cancelling seller was told they had *won* their own
// auction.
func TestCurrentAuctionDeliveryNoticesNameTheRightText(t *testing.T) {
	for _, test := range []struct {
		name string
		kind currentAuctionItemMailKind
		want byte
	}{
		{"purchase", currentAuctionPurchaseMail, currentAuctionNoticeKindBuyerWon},
		{"cancel return", currentAuctionReturnMail, currentAuctionNoticeKindCancelBid},
		{"expiry return", currentAuctionExpiredMail, currentAuctionNoticeKindSellerFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.kind.noticeKind != test.want {
				t.Fatalf("notice kind=%d want %d", test.kind.noticeKind, test.want)
			}
		})
	}
	// The three kinds really are three distinct texts in the client switch.
	if currentAuctionNoticeKindBuyerWon == currentAuctionNoticeKindCancelBid ||
		currentAuctionNoticeKindCancelBid == currentAuctionNoticeKindSellerFailed ||
		currentAuctionNoticeKindSellerFailed == currentAuctionNoticeKindBuyerWon {
		t.Fatal("the three delivery notices collapsed onto one kind again")
	}
	// 60091 is kind 5, 60089 is kind 3 and 60088 is kind 2 - the exact values
	// FUN_01d80150 switches on, so the names cannot drift silently.
	if currentAuctionNoticeKindCancelBid != 5 || currentAuctionNoticeKindSellerFailed != 3 ||
		currentAuctionNoticeKindBuyerWon != 2 {
		t.Fatalf("notice kind table moved: cancel=%d failed=%d won=%d",
			currentAuctionNoticeKindCancelBid, currentAuctionNoticeKindSellerFailed, currentAuctionNoticeKindBuyerWon)
	}
}

// TestCurrentAuctionCancelAnnounces60091 walks a real cancel end to end: the ACK
// first, then the class-0 60091 banner carrying the returned listing's item id.
func TestCurrentAuctionCancelAnnounces60091(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedCurrentAuctionBridgeCharacter(t, ctx, repositories, "10", "account-seller", "Seller", 20_000, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 6001, Count: 2, RawEntry: make([]byte, currentItemListEntryWireSize)},
	})
	connection := &bufferConn{}
	seller := &gameSession{conn: connection, selectedCharacterID: 10, accountID: "account-seller"}
	service := &Service{
		options:                options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider:     func() (dnfrepo.Group, bool) { return repositories, true },
		auctionItemEligibility: func(itemID uint32) bool { return itemID == 6001 },
		gameSessions:           map[uint16]*gameSession{10: seller},
	}
	seedAuctionSequenceForBridge(t, service, 400)
	if err := service.registerCurrentAuctionItemLocked(seller, buildCurrentAuctionRegisterRequestForTest(9, 6001, 2, -1, 75, 75, 24)); err != nil {
		t.Fatal(err)
	}
	connection.write.Reset()

	cancel := make([]byte, 9)
	cancel[0] = currentAuctionPayTypeGold
	binary.LittleEndian.PutUint64(cancel[1:9], 401)
	if err := service.cancelCurrentAuctionItemLocked(seller, cancel); err != nil {
		t.Fatal(err)
	}
	// The ACK, then the return letter's mailbox alarm (class0/0x63), then the
	// 60091 banner on its own class0/0xA9 packet.
	ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketAuctionRegistCancel) {
		t.Fatalf("cancel ACK header=%+v", ack.Header)
	}
	alarm, rest := splitGameServerUpperPacket(t, rest)
	if alarm.Header.MsgID != 0x0063 || alarm.Header.Classification != 0 {
		t.Fatalf("mailbox alarm header=%+v want class0/0x0063", alarm.Header)
	}
	notice, trailing := splitGameServerUpperPacket(t, rest)
	if len(trailing) != 0 {
		t.Fatalf("cancel left trailing bytes: %x", trailing)
	}
	if notice.Header.MsgID != currentAuctionNoticeMsgID || notice.Header.Classification != 0 {
		t.Fatalf("notice header=%+v want class0/0x00A9", notice.Header)
	}
	want := []byte{
		currentAuctionNoticeRouteItem,
		currentAuctionNoticeKindCancelBid,
		0x71, 0x17, 0x00, 0x00, // 6001 little endian
		0x00, 0x00,
	}
	if len(notice.Body) != 8 || !bytes.Equal(notice.Body, want) {
		t.Fatalf("cancel notice body=% X want % X", notice.Body, want)
	}
	// The forfeited deposit: the return letter carries the item and no gold, and
	// the 10000 is still sitting in escrow instead of having been paid back.
	mailbox, found, err := repositories.Mailbox.Load(ctx, "10")
	if err != nil || !found || len(mailbox.Mails) != 1 {
		t.Fatalf("seller mailbox found=%t err=%v mails=%+v", found, err, mailbox.Mails)
	}
	for _, returnMail := range mailbox.Mails {
		if returnMail.Metadata["source"] != currentAuctionReturnMail.source {
			t.Fatalf("return mail source=%q want %q", returnMail.Metadata["source"], currentAuctionReturnMail.source)
		}
		if returnMail.Gold != 0 {
			t.Fatalf("return mail carries %d gold, want 0 (a manual cancel forfeits the deposit)", returnMail.Gold)
		}
		if len(returnMail.Attachments) != 1 || returnMail.Attachments[0].ItemID != 6001 || returnMail.Attachments[0].Count != 2 {
			t.Fatalf("return mail attachments=%+v want the 2x 6001 stack", returnMail.Attachments)
		}
	}
	escrowCharacter, _, _ := repositories.Character.Load(ctx, dnfauction.EscrowCharacterID)
	if escrowCharacter.Stats["gold"] != dnfauction.ListingDepositGold {
		t.Fatalf("escrow gold=%d, want the forfeited %d kept",
			escrowCharacter.Stats["gold"], dnfauction.ListingDepositGold)
	}
	// Nothing is left owed in the outbox.
	owner, _ := dnfauction.NewOwner(repositories)
	if pending, err := owner.PendingDeliveries(ctx, "10"); err != nil || len(pending) != 0 {
		t.Fatalf("outbox after the return mail=%+v err=%v", pending, err)
	}
}

// ---------------------------------------------------------------------------
// N3: a gold consignment cancel announces 60091 on the gold route.
// ---------------------------------------------------------------------------

// TestCurrentGoldConsignCancelAnnounces60091 pins the fix for "cancelling a
// consignment says nothing". The gold panel has no notice of its own on the
// cancel path - the client's op184 handler only pops dstr 60084 (已取消拍卖物品的
// 上架), which is the item panel's wording - so the seller has to be told, and
// the text the auction notice table provides for it is kind 5:
// dstr 60091 「[%s]的竞拍已被取消，请确认邮件」.

// ---------------------------------------------------------------------------
// N5: closing a stall republishes the cells it was selling from.
// ---------------------------------------------------------------------------

// newM6PrivateStoreService is the minimum a stall needs: a licensed seller in a
// town area, a repository group, and the two per-store maps.

// m6PrivateStoreCompleteBody builds the op87 body: title, one row count, then
// rows of {display u16, price u32, list u8, slot u16, instance u32} and the
// trailing permission slot.

// TestCurrentPrivateStoreCloseRepublishesUnsoldSourceSlots covers the second
// "invisible until reselect" report. A stall never moves its items - the rows
// name live cells - so nothing was ever pushed back when it closed and the
// client's bag kept the stacks hidden.

// A cell the stall emptied (its whole stack sold) is republished as a delete
// row, so a sold-out cell cannot be left holding a phantom object either.
