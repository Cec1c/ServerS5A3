package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"

	"fmt"

	"strconv"
	"strings"
	"testing"
	"time"

	dnfauction "longheng.io/server/internal/modules/dnf/auction"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/premium"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestCurrentAuctionServiceOpenPublishesOnlyImplementedNativeGateOnce(t *testing.T) {
	connection := &bufferConn{}
	service := &Service{options: options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	session := &gameSession{conn: connection, accountID: "account-auction-open"}
	if err := service.sendCurrentAuctionServicesOpen(session); err != nil {
		t.Fatal(err)
	}
	remaining := connection.write.Bytes()
	for serviceType := byte(0); serviceType < 2; serviceType++ {
		packet, rest := splitGameServerUpperPacket(t, remaining)
		remaining = rest
		if packet.Header.Classification != 0 || packet.Header.MsgID != currentAuctionServiceNoticeMsgID || !bytes.Equal(packet.Body, buildAuctionServiceNotice(serviceType, 1)) {
			t.Fatalf("拍卖入口%d包不符", serviceType)
		}
	}
	if len(remaining) != 0 || session.auctionServiceOpenMask != 3 {
		t.Fatal("拍卖和金币寄售入口通知不完整")
	}
	written := connection.write.Len()
	if err := service.sendCurrentAuctionServicesOpen(session); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != written {
		t.Fatal("重复开启通知")
	}
}

func TestCurrentAuctionRegisterBuyAndClaimUsesNativeBodies(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedCurrentAuctionBridgeCharacter(t, ctx, repositories, "10", "account-seller", "Seller", 20_000, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 6001, Count: 2, RawEntry: make([]byte, currentItemListEntryWireSize)},
	})
	seedCurrentAuctionBridgeCharacter(t, ctx, repositories, "11", "account-buyer", "Buyer", 1000, nil)
	service := &Service{
		options:                options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider:     func() (dnfrepo.Group, bool) { return repositories, true },
		auctionItemEligibility: func(itemID uint32) bool { return itemID == 6001 },
	}
	seedAuctionSequenceForBridge(t, service, 100)
	sellerConnection := &bufferConn{}
	seller := &gameSession{conn: sellerConnection, selectedCharacterID: 10, accountID: "account-seller"}
	register := buildCurrentAuctionRegisterRequestForTest(9, 6001, 2, -1, 75, 75, 24)
	if err := service.registerCurrentAuctionItemLocked(seller, register); err != nil {
		t.Fatal(err)
	}
	registerACK, registerRest := splitGameServerUpperPacket(t, sellerConnection.write.Bytes())
	if registerACK.Header.MsgID != uint16(dnfenum.CmdPacketAuctionRegistItem) || !bytes.Equal(registerACK.Body, []byte{1, 0}) {
		t.Fatalf("register ACK header=%+v body=%x", registerACK.Header, registerACK.Body)
	}
	registerRefresh, registerGoldRest := splitGameServerUpperPacket(t, registerRest)
	if registerRefresh.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
		t.Fatalf("register refresh header=%+v", registerRefresh.Header)
	}
	// Registration now takes the deposit, so the wallet row is republished
	// right behind the emptied source slot.
	registerGold, trailing := splitGameServerUpperPacket(t, registerGoldRest)
	if len(trailing) != 0 || registerGold.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
		t.Fatalf("register gold refresh header=%+v trailing=%d", registerGold.Header, len(trailing))
	}
	sellerAfterRegister, _, _ := repositories.Character.Load(ctx, "10")
	if sellerAfterRegister.Stats["gold"] != 20_000-dnfauction.ListingDepositGold {
		t.Fatalf("registration deposit not taken: gold=%d", sellerAfterRegister.Stats["gold"])
	}

	buyerConnection := &bufferConn{}
	buyer := &gameSession{conn: buyerConnection, selectedCharacterID: 11, accountID: "account-buyer"}
	searchRequest := make([]byte, 14)
	searchRequest[0] = currentAuctionPayTypeGold
	searchRequest[7] = 1
	binary.LittleEndian.PutUint32(searchRequest[10:14], 6001)
	if err := service.searchCurrentAuctionItemsLocked(buyer, searchRequest); err != nil {
		t.Fatal(err)
	}
	searchACK, searchTrailing := splitGameServerUpperPacket(t, buyerConnection.write.Bytes())
	if len(searchTrailing) != 0 || searchACK.Header.MsgID != uint16(dnfenum.CmdPacketAuctionSearchByItemkey) ||
		len(searchACK.Body) != 1+7+0xA8 || searchACK.Body[0] != 1 || binary.LittleEndian.Uint64(searchACK.Body[8:16]) != 101 {
		t.Fatalf("buyer search header=%+v body_len=%d body=%x trailing=%d", searchACK.Header, len(searchACK.Body), searchACK.Body, len(searchTrailing))
	}
	buyerConnection.write.Reset()
	buy := make([]byte, 30)
	buy[0] = currentAuctionPayTypeGold
	binary.LittleEndian.PutUint32(buy[1:5], 150)
	binary.LittleEndian.PutUint64(buy[5:13], 101)
	binary.LittleEndian.PutUint32(buy[26:30], 6001)
	if err := service.buyCurrentAuctionItemLocked(buyer, uint16(dnfenum.CmdPacketAuctionBidding), buy, false); err != nil {
		t.Fatal(err)
	}
	buyACK, buyRest := splitGameServerUpperPacket(t, buyerConnection.write.Bytes())
	if buyACK.Header.MsgID != uint16(dnfenum.CmdPacketAuctionBidding) || !bytes.Equal(buyACK.Body, []byte{1, 0}) {
		t.Fatalf("buy ACK header=%+v body=%x", buyACK.Header, buyACK.Body)
	}
	goldRefresh, buyTrailing := splitGameServerUpperPacket(t, buyRest)
	if len(buyTrailing) != 0 || goldRefresh.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
		t.Fatalf("buy gold refresh=%+v trailing=%d", goldRefresh.Header, len(buyTrailing))
	}
	buyerRecord, _, _ := repositories.Character.Load(ctx, "11")
	buyerInventory, _, _ := repositories.Inventory.Load(ctx, "11")
	if buyerRecord.Stats["gold"] != 850 || len(buyerInventory.Slots) != 0 {
		t.Fatalf("buyer character=%+v inventory=%+v", buyerRecord, buyerInventory)
	}
	buyerMailbox, found, err := repositories.Mailbox.Load(ctx, "11")
	if err != nil || !found || len(buyerMailbox.Mails) != 1 {
		t.Fatalf("buyer mailbox found=%t err=%v mails=%+v", found, err, buyerMailbox.Mails)
	}
	for _, auctionMail := range buyerMailbox.Mails {
		if auctionMail.Metadata["source"] != "auction_purchase_delivery" || auctionMail.Metadata["completion_key"] == "" ||
			len(auctionMail.Attachments) != 1 || auctionMail.Attachments[0].ItemID != 6001 || auctionMail.Attachments[0].Count != 2 {
			t.Fatalf("buyer auction mail=%+v", auctionMail)
		}
	}

	mailbox, found, err := repositories.Mailbox.Load(ctx, "10")
	if err != nil || !found || len(mailbox.Mails) != 1 {
		t.Fatalf("seller mailbox found=%t err=%v mails=%+v", found, err, mailbox.Mails)
	}
	// 150 sold, 5% commission without an auction coupon, and the deposit back
	// because the listing sold out: 150 - 7 + 10000.
	wantProceeds := int64(150) - int64(150)*dnfauction.SaleFeePercent/100 + dnfauction.ListingDepositGold
	wantFee := int64(150) * dnfauction.SaleFeePercent / 100
	for _, auctionMail := range mailbox.Mails {
		if auctionMail.Gold != wantProceeds || auctionMail.Metadata["completion_key"] == "" || auctionMail.Metadata["source"] != "auction_sale_proceeds" {
			t.Fatalf("seller auction mail=%+v want gold=%d", auctionMail, wantProceeds)
		}
		// The seller's letter is the itemised statement, not one flat sentence.
		// This service carries no PVF catalogue, so the display name falls back
		// to 物品<id>; a real server prints the item's own name here. The subject
		// line also opens the body, because the client's detail pane draws the
		// body only.
		itemName := "物品6001"
		subject := fmt.Sprintf("[已售出 %s 2个。]", itemName)
		if auctionMail.Title != subject {
			t.Fatalf("seller sale title=%q want %q", auctionMail.Title, subject)
		}
		wantBody := fmt.Sprintf("%s\n出售价 : + 150 金币\n保证金 : + %d 金币\n手续费 : - %d 金币\n已售出 %s 并获得 %d 金币。",
			subject, dnfauction.ListingDepositGold, wantFee, itemName, wantProceeds)
		if auctionMail.Body != wantBody {
			t.Fatalf("seller sale body=%q\nwant %q", auctionMail.Body, wantBody)
		}
	}
	sellerRecord, _, _ := repositories.Character.Load(ctx, "10")
	if sellerRecord.Stats["gold"] != 20_000-dnfauction.ListingDepositGold {
		t.Fatalf("mail proceeds credited wallet before extraction: %d", sellerRecord.Stats["gold"])
	}

	sellerConnection.write.Reset()
	if err := service.sendCurrentAuctionOwnedItemsLocked(seller, []byte{currentAuctionPayTypeGold}); err != nil {
		t.Fatal(err)
	}
	claimACK, claimTrailing := splitGameServerUpperPacket(t, sellerConnection.write.Bytes())
	if len(claimTrailing) != 0 || claimACK.Header.MsgID != uint16(dnfenum.CmdPacketAuctionMyRegistedItemInfo) || !bytes.Equal(claimACK.Body, []byte{1, 0, 0}) {
		t.Fatalf("claim ack=%+v body=%x trailing=%d", claimACK.Header, claimACK.Body, len(claimTrailing))
	}
}

func TestCurrentAuctionEmptyResultBodiesMatchCurrentEXEReaders(t *testing.T) {
	if stock := buildCurrentAuctionRegisterSuccessBody(101, false); !bytes.Equal(stock, []byte{0}) {
		t.Fatalf("stock op183 feature body=%x", stock)
	}
	robot := buildCurrentAuctionRegisterSuccessBody(101, true)
	if len(robot) != 9 || robot[0] != 0 || binary.LittleEndian.Uint64(robot[1:9]) != 101 {
		t.Fatalf("robot op183 feature body=%x", robot)
	}
	search := buildCurrentAuctionSearchBody(nil, nil)
	if !bytes.Equal(search, []byte{0, 0, 0, 0, 0, 0, 0}) {
		t.Fatalf("empty op186 body=%x, want pay+total+u16 count", search)
	}

	if bidding := buildCurrentAuctionBiddingBody(nil, "10", func(uint32) bool { return true }, time.Now().UTC()); !bytes.Equal(bidding, []byte{0, 0, 0}) {
		t.Fatalf("empty op189 body=%x, want pay+u16 count", bidding)
	}

	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedCurrentAuctionBridgeCharacter(t, ctx, repositories, "10", "account-seller", "Seller", 1_000, nil)
	connection := &bufferConn{}
	service := &Service{
		options:                options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider:     func() (dnfrepo.Group, bool) { return repositories, true },
		auctionItemEligibility: func(uint32) bool { return true },
	}
	session := &gameSession{conn: connection, selectedCharacterID: 10, accountID: "account-seller"}
	if err := service.handleCurrentAuctionCommand(session, uint16(dnfenum.CmdPacketAuctionMyBiddingInfo), []byte{currentAuctionPayTypeGold}); err != nil {
		t.Fatal(err)
	}
	ack, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || ack.Header.MsgID != uint16(dnfenum.CmdPacketAuctionMyBiddingInfo) || !bytes.Equal(ack.Body, []byte{1, 0, 0, 0}) {
		t.Fatalf("op189 ACK header=%+v body=%x trailing=%d", ack.Header, ack.Body, len(trailing))
	}
}

func TestCurrentAuctionStackableRowsUseProvenCurrentEXELayout(t *testing.T) {
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint32(raw[2:6], 6001)
	binary.LittleEndian.PutUint32(raw[6:10], 2)
	raw[0x0A] = 7
	binary.LittleEndian.PutUint16(raw[0x0B:0x0D], 99)
	raw[0x13] = 3
	binary.LittleEndian.PutUint16(raw[0x14:0x16], 12)
	registeredAt := time.Now().UTC()
	listing := dnfauction.Listing{
		ID: 101, SellerID: "10", SellerName: "Seller", UnitPrice: 75,
		CreatedAt: registeredAt, ExpiresAt: registeredAt.Add(48 * time.Hour),
		Item: dnfrepo.ItemStack{ItemID: 6001, Count: 2, RawEntry: raw},
	}
	body := buildCurrentAuctionSearchBody([]dnfauction.Listing{listing}, func(uint32) bool { return true })
	if len(body) != 7+0xA8 || body[0] != 0 || binary.LittleEndian.Uint32(body[1:5]) != 1 || binary.LittleEndian.Uint16(body[5:7]) != 1 {
		t.Fatalf("search body header=%x len=%d", body[:min(len(body), 7)], len(body))
	}
	row := body[7:]
	item := row[0x2B:0x7E]
	// This fixture is buyout-only (no StartPrice), so the 当前竞拍价 slot is the
	// -1 sentinel the painter tests and only the 一口价 pair carries numbers.
	if binary.LittleEndian.Uint64(row[0:8]) != 101 || binary.LittleEndian.Uint32(row[0x09:0x0D]) != currentAuctionNoPrice ||
		binary.LittleEndian.Uint32(row[0x0D:0x11]) != 150 || binary.LittleEndian.Uint32(row[0x11:0x15]) != 75 ||
		binary.LittleEndian.Uint32(item[1:5]) != 6001 || binary.LittleEndian.Uint32(item[6:10]) != 2 ||
		item[5] != 7 || binary.LittleEndian.Uint16(item[0x0A:0x0C]) != 99 || item[0x10] != 3 ||
		binary.LittleEndian.Uint16(item[0x11:0x13]) != 12 {
		t.Fatalf("search row=%x", row)
	}
	// 期限 is the one-byte hour count the painter formats with text 60159
	// ("%d小时"); row+0xA0 is the seller-warning tier (100/200/300 render
	// 注意！赖皮小人！/无耻之徒！/全民公敌！ in red) and must stay 0.
	if row[0x2A] != 48 {
		t.Fatalf("op186 期限=%d, want 48", row[0x2A])
	}
	if binary.LittleEndian.Uint32(row[0xA0:0xA4]) != 0 {
		t.Fatalf("op186 seller-warning tier=%d, want 0", binary.LittleEndian.Uint32(row[0xA0:0xA4]))
	}
	owned := buildCurrentAuctionOwnedBody([]dnfauction.Listing{listing}, "10", func(uint32) bool { return true })
	if len(owned) != 2+0x97 || owned[1] != 1 || binary.LittleEndian.Uint64(owned[2:10]) != 101 {
		t.Fatalf("owned body=%x", owned)
	}
	if owned[2+0x1D] != 48 {
		t.Fatalf("op188 期限=%d, want 48", owned[2+0x1D])
	}
	filtered := buildCurrentAuctionSearchBody([]dnfauction.Listing{listing}, func(uint32) bool { return false })
	if !bytes.Equal(filtered, []byte{0, 0, 0, 0, 0, 0, 0}) {
		t.Fatalf("complex equipment must stay hidden, body=%x", filtered)
	}
}

func TestCurrentAuctionRemainingHoursRoundsUp(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	for name, probe := range map[string]struct {
		expires time.Time
		want    byte
	}{
		"just_registered_24h": {now.Add(24 * time.Hour), 24},
		"just_registered_48h": {now.Add(48 * time.Hour), 48},
		"partial_hour_left":   {now.Add(90 * time.Minute), 2},
		"final_minute":        {now.Add(time.Minute), 1},
		"already_expired":     {now.Add(-time.Minute), 0},
		"no_deadline":         {time.Time{}, 0},
		"beyond_one_byte":     {now.Add(300 * time.Hour), 255},
	} {
		got := currentAuctionRemainingHours(dnfauction.Listing{ExpiresAt: probe.expires}, now)
		if got != probe.want {
			t.Fatalf("%s: 期限=%d, want %d", name, got, probe.want)
		}
	}
}

func TestCurrentAuctionRowsRenderBidTotalSeparatelyFromBuyoutTotal(t *testing.T) {
	listing := dnfauction.Listing{
		ID: 101, SellerID: "10", SellerName: "Seller", UnitPrice: 75, StartPrice: 30,
		CreatedAt: time.Unix(100, 0), ExpiresAt: time.Unix(100, 0).Add(48 * time.Hour),
		Item: dnfrepo.ItemStack{ItemID: 6001, Count: 2},
	}
	stackable := func(uint32) bool { return true }
	for name, probe := range map[string]struct {
		startPrice       int64
		bidTotal, buyout uint32
		unitPrice        uint32
	}{
		"opening_bid_below_buyout":  {30, 60, 150, 75},
		"buyout_only_sentinel":      {-1, currentAuctionNoPrice, 150, 75},
		"opening_bid_equals_buyout": {75, 150, 150, 75},
	} {
		listing.StartPrice = probe.startPrice
		row := buildCurrentAuctionSearchBody([]dnfauction.Listing{listing}, stackable)[7:]
		if binary.LittleEndian.Uint32(row[0x09:0x0D]) != probe.bidTotal ||
			binary.LittleEndian.Uint32(row[0x0D:0x11]) != probe.buyout ||
			binary.LittleEndian.Uint32(row[0x11:0x15]) != probe.unitPrice {
			t.Fatalf("%s: op186 slots=%d/%d/%d want %d/%d/%d", name,
				binary.LittleEndian.Uint32(row[0x09:0x0D]), binary.LittleEndian.Uint32(row[0x0D:0x11]),
				binary.LittleEndian.Uint32(row[0x11:0x15]), probe.bidTotal, probe.buyout, probe.unitPrice)
		}
		owned := buildCurrentAuctionOwnedBody([]dnfauction.Listing{listing}, "10", stackable)[2:]
		if binary.LittleEndian.Uint32(owned[0x08:0x0C]) != probe.bidTotal ||
			binary.LittleEndian.Uint32(owned[0x0C:0x10]) != probe.buyout ||
			binary.LittleEndian.Uint32(owned[0x93:0x97]) != probe.unitPrice {
			t.Fatalf("%s: op188 slots=%d/%d/%d want %d/%d/%d", name,
				binary.LittleEndian.Uint32(owned[0x08:0x0C]), binary.LittleEndian.Uint32(owned[0x0C:0x10]),
				binary.LittleEndian.Uint32(owned[0x93:0x97]), probe.bidTotal, probe.buyout, probe.unitPrice)
		}
	}
	// A listing built by code written before StartPrice existed leaves the zero
	// value behind. That must read as buyout-only - so the bid slot is the
	// sentinel, never a price the buyer could take as a free item.
	listing.StartPrice = 0
	zeroValue := buildCurrentAuctionSearchBody([]dnfauction.Listing{listing}, stackable)[7:]
	if binary.LittleEndian.Uint32(zeroValue[0x09:0x0D]) != currentAuctionNoPrice {
		t.Fatalf("zero StartPrice bid total=%d, want the -1 sentinel",
			binary.LittleEndian.Uint32(zeroValue[0x09:0x0D]))
	}
	if binary.LittleEndian.Uint32(zeroValue[0x0D:0x11]) != 150 {
		t.Fatalf("zero StartPrice buyout=%d, want 150", binary.LittleEndian.Uint32(zeroValue[0x0D:0x11]))
	}
}

func TestCurrentAuctionRegisterCarriesOpeningBidIntoEscrow(t *testing.T) {
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
	seedAuctionSequenceForBridge(t, service, 200)
	seller := &gameSession{conn: &bufferConn{}, selectedCharacterID: 10, accountID: "account-seller"}
	if err := service.registerCurrentAuctionItemLocked(seller, buildCurrentAuctionRegisterRequestForTest(9, 6001, 2, 30, 75, 75, 24)); err != nil {
		t.Fatal(err)
	}
	owner, _ := dnfauction.NewOwner(repositories)
	listings, err := owner.Search(ctx, []uint32{6001})
	if err != nil || len(listings) != 1 {
		t.Fatalf("search=%+v err=%v", listings, err)
	}
	if listings[0].StartPrice != 30 {
		t.Fatalf("registration dropped the opening bid: %+v", listings[0])
	}
	row := buildCurrentAuctionSearchBody(listings, func(uint32) bool { return true })[7:]
	if binary.LittleEndian.Uint32(row[0x09:0x0D]) != 60 || binary.LittleEndian.Uint32(row[0x0D:0x11]) != 150 {
		t.Fatalf("op186 row after registration=%x", row[0x09:0x15])
	}
}

func TestCurrentAuctionRegisterRejectsUnsupportedItemAndInconsistentUnitPricesWithoutRemovingStack(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedCurrentAuctionBridgeCharacter(t, ctx, repositories, "10", "account-seller", "Seller", 20_000, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 6001, Count: 2},
	})
	service := &Service{
		options:                options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider:     func() (dnfrepo.Group, bool) { return repositories, true },
		auctionItemEligibility: func(uint32) bool { return false },
	}
	request := buildCurrentAuctionRegisterRequestForTest(9, 6001, 2, 75, 75, 75, 24)
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 10, accountID: "account-seller"}
	if err := service.registerCurrentAuctionItemLocked(session, request); err != nil {
		t.Fatal(err)
	}
	inventory, _, _ := repositories.Inventory.Load(ctx, "10")
	if inventory.Slots["0:9"].Count != 2 {
		t.Fatalf("unsupported item was removed: %+v", inventory.Slots)
	}
	service.auctionItemEligibility = func(uint32) bool { return true }
	binary.LittleEndian.PutUint32(request[16:20], 76)
	if err := service.registerCurrentAuctionItemLocked(session, request); err != nil {
		t.Fatal(err)
	}
	inventory, _, _ = repositories.Inventory.Load(ctx, "10")
	if inventory.Slots["0:9"].Count != 2 {
		t.Fatalf("inconsistent total removed item: %+v", inventory.Slots)
	}
}

func TestCurrentAuctionRegisterDecodesLiveNoBidRequest(t *testing.T) {
	body := buildCurrentAuctionRegisterRequestForTest(14, 10081, 1, -1, 31200, 31200, 24)
	request, err := decodeCurrentAuctionRegisterRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if request.PayType != 0 || request.ListType != currentExeInventoryMainListType ||
		request.Slot != 14 || request.ItemID != 10081 || request.Count != 1 ||
		request.StartPrice != -1 || request.InstantPrice != 31200 || request.UnitPrice != 31200 || request.DurationHours != 24 {
		t.Fatalf("decoded live register request=%+v", request)
	}
	reserved := append([]byte(nil), body...)
	reserved[28] = 1
	badDuration := append([]byte(nil), body...)
	binary.LittleEndian.PutUint32(badDuration[24:28], 72)
	wrongList := append([]byte(nil), body...)
	wrongList[1] = 1
	for name, candidate := range map[string][]byte{
		"legacy_28_byte_shape": body[:28],
		"nonzero_reserved":     reserved,
		"unsupported_duration": badDuration,
		"non_main_list":        wrongList,
	} {
		if _, err := decodeCurrentAuctionRegisterRequest(candidate); err == nil {
			t.Fatalf("%s request unexpectedly accepted: %x", name, candidate)
		}
	}
}

func TestCurrentAuctionCouponFreezesTheDiscountedFee(t *testing.T) {
	ctx := context.Background()
	for name, coupon := range map[string]bool{"with_coupon": true, "without_coupon": false} {
		repositories := dnfrepomemory.NewMemoryGroup()
		seedCurrentAuctionBridgeCharacter(t, ctx, repositories, "10", "account-seller", "Seller", 20_000, map[string]dnfrepo.ItemStack{
			"0:9": {ItemID: 6001, Count: 1, RawEntry: make([]byte, currentItemListEntryWireSize)},
		})
		account := dnfrepo.AccountRecord{AccountID: "account-seller"}
		if coupon {
			premium.Upsert(&account, 29, int64(30*24*time.Hour/time.Second), 1, time.Now().UTC())
		}
		if err := repositories.Account.Save(ctx, account); err != nil {
			t.Fatal(err)
		}
		service := &Service{
			options:                options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
			repositoryProvider:     func() (dnfrepo.Group, bool) { return repositories, true },
			auctionItemEligibility: func(itemID uint32) bool { return itemID == 6001 },
		}
		seedAuctionSequenceForBridge(t, service, 500)
		seller := &gameSession{conn: &bufferConn{}, selectedCharacterID: 10, accountID: "account-seller"}
		if err := service.registerCurrentAuctionItemLocked(seller, buildCurrentAuctionRegisterRequestForTest(9, 6001, 1, -1, 75, 75, 24)); err != nil {
			t.Fatal(err)
		}
		owner, _ := dnfauction.NewOwner(repositories)
		listings, err := owner.Search(ctx, nil)
		if err != nil || len(listings) != 1 {
			t.Fatalf("%s: search=%+v err=%v", name, listings, err)
		}
		want := dnfauction.SaleFeePercentFor(coupon)
		if listings[0].FeePercent != want || listings[0].Deposit != dnfauction.ListingDepositGold {
			t.Fatalf("%s: listing fee=%d deposit=%d, want fee=%d deposit=%d",
				name, listings[0].FeePercent, listings[0].Deposit, want, dnfauction.ListingDepositGold)
		}
	}
}

func TestCurrentAuctionExpiredListingReturnsByMailWithDeposit(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedCurrentAuctionBridgeCharacter(t, ctx, repositories, "10", "account-seller", "Seller", 20_000, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 6001, Count: 1, RawEntry: make([]byte, currentItemListEntryWireSize)},
	})
	service := &Service{
		options:                options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider:     func() (dnfrepo.Group, bool) { return repositories, true },
		auctionItemEligibility: func(itemID uint32) bool { return itemID == 6001 },
	}
	seedAuctionSequenceForBridge(t, service, 600)
	seller := &gameSession{conn: &bufferConn{}, selectedCharacterID: 10, accountID: "account-seller"}
	if err := service.registerCurrentAuctionItemLocked(seller, buildCurrentAuctionRegisterRequestForTest(9, 6001, 1, -1, 75, 75, 24)); err != nil {
		t.Fatal(err)
	}
	// Age the listing past its 24h duration.
	owner, _ := dnfauction.NewOwner(repositories)
	escrow, _, _ := repositories.Inventory.Load(ctx, dnfauction.EscrowCharacterID)
	for key, stack := range escrow.Slots {
		if !strings.HasPrefix(key, "auction:listing:") {
			continue
		}
		stack.Extra = cloneDungeonCardStringMap(stack.Extra)
		stack.Extra["expires_at"] = strconv.FormatInt(time.Now().UTC().Add(-time.Hour).Unix(), 10)
		escrow.Slots[key] = stack
	}
	if err := repositories.Inventory.Save(ctx, escrow); err != nil {
		t.Fatal(err)
	}
	if err := service.sendCurrentAuctionOwnedItemsLocked(seller, []byte{currentAuctionPayTypeGold}); err != nil {
		t.Fatal(err)
	}
	inventory, _, _ := repositories.Inventory.Load(ctx, "10")
	if _, exists := inventory.Slots["0:9"]; exists {
		t.Fatalf("expired listing was written back into the bag: %+v", inventory.Slots)
	}
	mailbox, found, err := repositories.Mailbox.Load(ctx, "10")
	if err != nil || !found || len(mailbox.Mails) != 1 {
		t.Fatalf("seller mailbox found=%t err=%v mails=%+v", found, err, mailbox.Mails)
	}
	for _, expiredMail := range mailbox.Mails {
		if expiredMail.Metadata["source"] != "auction_listing_expired" || expiredMail.Gold != dnfauction.ListingDepositGold ||
			len(expiredMail.Attachments) != 1 || expiredMail.Attachments[0].ItemID != 6001 {
			t.Fatalf("expired return mail=%+v", expiredMail)
		}
	}
	pending, err := owner.PendingDeliveries(ctx, "10")
	if err != nil || len(pending) != 0 {
		t.Fatalf("outbox after the expiry mail=%+v err=%v", pending, err)
	}
	escrowCharacter, _, _ := repositories.Character.Load(ctx, dnfauction.EscrowCharacterID)
	if escrowCharacter.Stats["gold"] != 0 {
		t.Fatalf("escrow kept the deposit after refunding it: %d", escrowCharacter.Stats["gold"])
	}
}

func TestCurrentAuctionCancelReturnsTheItemByMail(t *testing.T) {
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
	connection.write.Reset()

	cancel := make([]byte, 9)
	cancel[0] = currentAuctionPayTypeGold
	binary.LittleEndian.PutUint64(cancel[1:9], 401)
	if err := service.cancelCurrentAuctionItemLocked(seller, cancel); err != nil {
		t.Fatal(err)
	}
	cancelACK, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if cancelACK.Header.MsgID != uint16(dnfenum.CmdPacketAuctionRegistCancel) || !bytes.Equal(cancelACK.Body, []byte{1, 0}) {
		t.Fatalf("cancel ACK header=%+v body=%x", cancelACK.Header, cancelACK.Body)
	}
	// No op14 row follows: nothing entered the bag, so there is nothing to
	// repaint there.
	if len(trailing) != 0 {
		t.Fatalf("cancel emitted %d trailing bytes, want the ACK alone", len(trailing))
	}
	inventory, _, _ := repositories.Inventory.Load(ctx, "10")
	if _, exists := inventory.Slots["0:9"]; exists {
		t.Fatalf("cancel put the item back in the bag: %+v", inventory.Slots)
	}
	mailbox, found, err := repositories.Mailbox.Load(ctx, "10")
	if err != nil || !found || len(mailbox.Mails) != 1 {
		t.Fatalf("seller mailbox found=%t err=%v mails=%+v", found, err, mailbox.Mails)
	}
	for _, returnMail := range mailbox.Mails {
		if returnMail.Metadata["source"] != "auction_listing_return" || returnMail.Metadata["completion_key"] == "" ||
			len(returnMail.Attachments) != 1 || returnMail.Attachments[0].ItemID != 6001 || returnMail.Attachments[0].Count != 2 {
			t.Fatalf("cancel return mail=%+v", returnMail)
		}
	}
	owner, _ := dnfauction.NewOwner(repositories)
	pending, err := owner.PendingDeliveries(ctx, "10")
	if err != nil || len(pending) != 0 {
		t.Fatalf("outbox after the return mail=%+v err=%v", pending, err)
	}
	listings, err := owner.Search(ctx, nil)
	if err != nil || len(listings) != 0 {
		t.Fatalf("listing survived the cancel: %+v err=%v", listings, err)
	}
}

func TestCurrentAuctionRegisterAcceptsBidOnlyListing(t *testing.T) {
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
	seedAuctionSequenceForBridge(t, service, 300)
	seller := &gameSession{conn: &bufferConn{}, selectedCharacterID: 10, accountID: "account-seller"}
	if err := service.registerCurrentAuctionItemLocked(seller, buildCurrentAuctionRegisterRequestForTest(9, 6001, 2, 30, -1, 0, 24)); err != nil {
		t.Fatal(err)
	}
	owner, _ := dnfauction.NewOwner(repositories)
	listings, err := owner.Search(ctx, []uint32{6001})
	if err != nil || len(listings) != 1 {
		t.Fatalf("bid-only registration did not list: %+v err=%v", listings, err)
	}
	listing := listings[0]
	if listing.StartPrice != 30 || listing.UnitPrice != 30 || listing.HasBuyout() {
		t.Fatalf("bid-only listing=%+v want start=unit=30 with no buyout", listing)
	}
	// 当前竞拍价 carries the bid total; both 一口价 slots go out as the -1
	// sentinel, so the client's painter skips the whole 一口价 line instead of
	// drawing "一口价 0 金币".
	row := buildCurrentAuctionSearchBody(listings, func(uint32) bool { return true })[7:]
	if binary.LittleEndian.Uint32(row[0x09:0x0D]) != 60 ||
		binary.LittleEndian.Uint32(row[0x0D:0x11]) != currentAuctionNoPrice ||
		binary.LittleEndian.Uint32(row[0x11:0x15]) != currentAuctionNoPrice {
		t.Fatalf("op186 bid-only slots=%x want 60/-1/-1", row[0x09:0x15])
	}
	owned := buildCurrentAuctionOwnedBody(listings, "10", func(uint32) bool { return true })[2:]
	if binary.LittleEndian.Uint32(owned[0x08:0x0C]) != 60 ||
		binary.LittleEndian.Uint32(owned[0x0C:0x10]) != currentAuctionNoPrice ||
		binary.LittleEndian.Uint32(owned[0x93:0x97]) != currentAuctionNoPrice {
		t.Fatalf("op188 bid-only slots=%d/%d/%d want 60/-1/-1",
			binary.LittleEndian.Uint32(owned[0x08:0x0C]), binary.LittleEndian.Uint32(owned[0x0C:0x10]),
			binary.LittleEndian.Uint32(owned[0x93:0x97]))
	}
}

func TestCurrentAuctionRegisterRejectsPriceShapesTheClientCannotSend(t *testing.T) {
	for name, body := range map[string][]byte{
		// sub_3007BC0 shows text 0xEAE2 and never sends this one.
		"both_controls_empty": buildCurrentAuctionRegisterRequestForTest(9, 6001, 2, -1, -1, 0, 24),
		// A missing buyout leaves the per-item slot at 0, never at a price.
		"absent_buyout_with_unit_price": buildCurrentAuctionRegisterRequestForTest(9, 6001, 2, 30, -1, 75, 24),
		// With a buyout set the two buyout slots always agree.
		"buyout_slots_disagree": buildCurrentAuctionRegisterRequestForTest(9, 6001, 2, 30, 75, 76, 24),
		// sub_3007BC0 shows text 0xEAE4 for a bid above the buyout.
		"bid_above_buyout": buildCurrentAuctionRegisterRequestForTest(9, 6001, 2, 80, 75, 75, 24),
		"negative_bid":     buildCurrentAuctionRegisterRequestForTest(9, 6001, 2, -2, 75, 75, 24),
	} {
		if _, err := decodeCurrentAuctionRegisterRequest(body); err == nil {
			t.Fatalf("%s: decoder accepted a body the client cannot produce", name)
		}
	}
}

func TestCurrentAuctionBiddingOpcodeRecordsSubBuyoutAmount(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedCurrentAuctionBridgeCharacter(t, ctx, repositories, "10", "account-seller", "Seller", 20_000, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 6001, Count: 2, RawEntry: make([]byte, currentItemListEntryWireSize)},
	})
	seedCurrentAuctionBridgeCharacter(t, ctx, repositories, "11", "account-buyer", "Bidder", 1_000, nil)
	service := &Service{
		options:                options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider:     func() (dnfrepo.Group, bool) { return repositories, true },
		auctionItemEligibility: func(itemID uint32) bool { return itemID == 6001 },
	}
	seedAuctionSequenceForBridge(t, service, 200)
	seller := &gameSession{conn: &bufferConn{}, selectedCharacterID: 10, accountID: "account-seller"}
	// Opening bid 30/item (total 60), buyout 75/item (total 150).
	if err := service.registerCurrentAuctionItemLocked(seller, buildCurrentAuctionRegisterRequestForTest(9, 6001, 2, 30, 75, 75, 24)); err != nil {
		t.Fatal(err)
	}

	bid := make([]byte, 30)
	bid[0] = currentAuctionPayTypeGold
	binary.LittleEndian.PutUint32(bid[1:5], 100)
	binary.LittleEndian.PutUint64(bid[5:13], 201)
	binary.LittleEndian.PutUint32(bid[26:30], 6001)
	bidderConnection := &bufferConn{}
	bidder := &gameSession{conn: bidderConnection, selectedCharacterID: 11, accountID: "account-buyer"}
	searchRequest := make([]byte, 14)
	searchRequest[0] = currentAuctionPayTypeGold
	searchRequest[7] = 1
	binary.LittleEndian.PutUint32(searchRequest[10:14], 6001)
	// The dispatcher records the search the player is looking at; the bid then
	// has to hand that list back, because the client erases the row it bid on.
	service.rememberCurrentAuctionSearchLocked(bidder, uint16(dnfenum.CmdPacketAuctionSearchByItemkey), searchRequest)
	if err := service.buyCurrentAuctionItemLocked(bidder, uint16(dnfenum.CmdPacketAuctionBidding), bid, false); err != nil {
		t.Fatal(err)
	}
	bidACK, bidRest := splitGameServerUpperPacket(t, bidderConnection.write.Bytes())
	if bidACK.Header.MsgID != uint16(dnfenum.CmdPacketAuctionBidding) || !bytes.Equal(bidACK.Body, []byte{1, 0}) {
		t.Fatalf("bid ACK header=%+v body=%x", bidACK.Header, bidACK.Body)
	}
	goldRefresh, afterGold := splitGameServerUpperPacket(t, bidRest)
	if goldRefresh.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
		t.Fatalf("bid gold refresh=%+v", goldRefresh.Header)
	}
	replay, trailing := splitGameServerUpperPacket(t, afterGold)
	if len(trailing) != 0 || replay.Header.MsgID != uint16(dnfenum.CmdPacketAuctionSearchByItemkey) ||
		binary.LittleEndian.Uint32(replay.Body[8:][0x09:0x0D]) != 100 {
		t.Fatalf("bid did not replay the search: header=%+v body=%x trailing=%d", replay.Header, replay.Body, len(trailing))
	}
	bidderRecord, _, _ := repositories.Character.Load(ctx, "11")
	if bidderRecord.Stats["gold"] != 900 {
		t.Fatalf("bid did not escrow the gold: %d", bidderRecord.Stats["gold"])
	}
	// The item stays in escrow - a bid is not a sale.
	escrow, _, _ := repositories.Inventory.Load(ctx, dnfauction.EscrowCharacterID)
	if _, exists := escrow.Slots["auction:listing:201"]; !exists {
		t.Fatalf("bid consumed the listing: %+v", escrow.Slots)
	}

	// A second, lower bid is refused with the code the panel renders for it,
	// and the first bid stays standing.
	bidderConnection.write.Reset()
	binary.LittleEndian.PutUint32(bid[1:5], 80)
	if err := service.buyCurrentAuctionItemLocked(bidder, uint16(dnfenum.CmdPacketAuctionBidding), bid, false); err != nil {
		t.Fatal(err)
	}
	lowACK, lowTrailing := splitGameServerUpperPacket(t, bidderConnection.write.Bytes())
	if len(lowTrailing) != 0 || !bytes.Equal(lowACK.Body, []byte{0, currentAuctionErrorPriceMismatch, currentAuctionPayTypeGold}) {
		t.Fatalf("low bid ACK body=%x trailing=%d", lowACK.Body, len(lowTrailing))
	}

	// The search row has to carry the standing bid, not the opening one.
	bidderConnection.write.Reset()
	if err := service.searchCurrentAuctionItemsLocked(bidder, searchRequest); err != nil {
		t.Fatal(err)
	}
	searchACK, _ := splitGameServerUpperPacket(t, bidderConnection.write.Bytes())
	row := searchACK.Body[8:]
	if binary.LittleEndian.Uint32(row[0x09:0x0D]) != 100 || binary.LittleEndian.Uint32(row[0x0D:0x11]) != 150 {
		t.Fatalf("current bid slot=%d buyout slot=%d, want 100/150",
			binary.LittleEndian.Uint32(row[0x09:0x0D]), binary.LittleEndian.Uint32(row[0x0D:0x11]))
	}

	// Exactly the buyout total still buys it out, and the bidder's own bid
	// counts towards it: 150 - 100 leaves the wallet.
	bidderConnection.write.Reset()
	binary.LittleEndian.PutUint32(bid[1:5], 150)
	if err := service.buyCurrentAuctionItemLocked(bidder, uint16(dnfenum.CmdPacketAuctionBidding), bid, false); err != nil {
		t.Fatal(err)
	}
	buyoutACK, _ := splitGameServerUpperPacket(t, bidderConnection.write.Bytes())
	if !bytes.Equal(buyoutACK.Body, []byte{1, 0}) {
		t.Fatalf("buyout over own bid ACK body=%x", buyoutACK.Body)
	}
	bidderRecord, _, _ = repositories.Character.Load(ctx, "11")
	if bidderRecord.Stats["gold"] != 850 {
		t.Fatalf("own-bid buyout charged the full price: %d", bidderRecord.Stats["gold"])
	}
}

func TestCurrentAuctionMyBiddingListsStandingBids(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedCurrentAuctionBridgeCharacter(t, ctx, repositories, "10", "account-seller", "Seller", 20_000, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 6001, Count: 2, RawEntry: make([]byte, currentItemListEntryWireSize)},
	})
	seedCurrentAuctionBridgeCharacter(t, ctx, repositories, "11", "account-bidder", "Bidder", 1_000, nil)
	seedCurrentAuctionBridgeCharacter(t, ctx, repositories, "12", "account-rival", "Rival", 1_000, nil)
	service := &Service{
		options:                options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider:     func() (dnfrepo.Group, bool) { return repositories, true },
		auctionItemEligibility: func(itemID uint32) bool { return itemID == 6001 },
	}
	seedAuctionSequenceForBridge(t, service, 300)
	seller := &gameSession{conn: &bufferConn{}, selectedCharacterID: 10, accountID: "account-seller"}
	if err := service.registerCurrentAuctionItemLocked(seller, buildCurrentAuctionRegisterRequestForTest(9, 6001, 2, 30, 75, 75, 24)); err != nil {
		t.Fatal(err)
	}

	bidderConnection := &bufferConn{}
	bidder := &gameSession{conn: bidderConnection, selectedCharacterID: 11, accountID: "account-bidder"}
	bid := make([]byte, 30)
	bid[0] = currentAuctionPayTypeGold
	binary.LittleEndian.PutUint32(bid[1:5], 100)
	binary.LittleEndian.PutUint64(bid[5:13], 301)
	binary.LittleEndian.PutUint32(bid[26:30], 6001)
	if err := service.buyCurrentAuctionItemLocked(bidder, uint16(dnfenum.CmdPacketAuctionBidding), bid, false); err != nil {
		t.Fatal(err)
	}

	bidderConnection.write.Reset()
	if err := service.sendCurrentAuctionBiddingItemsLocked(bidder, []byte{currentAuctionPayTypeGold}); err != nil {
		t.Fatal(err)
	}
	ack, trailing := splitGameServerUpperPacket(t, bidderConnection.write.Bytes())
	if len(trailing) != 0 || ack.Header.MsgID != uint16(dnfenum.CmdPacketAuctionMyBiddingInfo) ||
		len(ack.Body) != 1+3+0x9F || ack.Body[1] != currentAuctionPayTypeGold ||
		binary.LittleEndian.Uint16(ack.Body[2:4]) != 1 {
		t.Fatalf("op189 ack header=%+v body_len=%d body=%x trailing=%d", ack.Header, len(ack.Body), ack.Body, len(trailing))
	}
	row := ack.Body[4:]
	if binary.LittleEndian.Uint64(row[0x00:0x08]) != 301 ||
		binary.LittleEndian.Uint32(row[0x08:0x0C]) != 100 ||
		binary.LittleEndian.Uint32(row[0x0C:0x10]) != 150 ||
		binary.LittleEndian.Uint32(row[0x9B:0x9F]) != 75 {
		t.Fatalf("my-bidding row=%x", row)
	}
	if row[0x25] != 24 {
		t.Fatalf("my-bidding 期限=%d, want 24", row[0x25])
	}
	if string(bytes.TrimRight(row[0x10:0x1D], "\x00")) != string(rosterNameBytes("Seller")) {
		t.Fatalf("my-bidding seller name=%x", row[0x10:0x1D])
	}
	item := row[0x26 : 0x26+0x53]
	if binary.LittleEndian.Uint32(item[1:5]) != 6001 || binary.LittleEndian.Uint32(item[6:10]) != 2 {
		t.Fatalf("my-bidding item=%x", item)
	}

	// The seller sees nothing here, and a rival who gets outbid drops off the
	// list the moment their bid is released.
	sellerConnection := &bufferConn{}
	seller.conn = sellerConnection
	if err := service.sendCurrentAuctionBiddingItemsLocked(seller, []byte{currentAuctionPayTypeGold}); err != nil {
		t.Fatal(err)
	}
	sellerACK, _ := splitGameServerUpperPacket(t, sellerConnection.write.Bytes())
	if !bytes.Equal(sellerACK.Body, []byte{1, 0, 0, 0}) {
		t.Fatalf("seller my-bidding body=%x, want empty", sellerACK.Body)
	}
	rivalConnection := &bufferConn{}
	rival := &gameSession{conn: rivalConnection, selectedCharacterID: 12, accountID: "account-rival"}
	binary.LittleEndian.PutUint32(bid[1:5], 120)
	if err := service.buyCurrentAuctionItemLocked(rival, uint16(dnfenum.CmdPacketAuctionBidding), bid, false); err != nil {
		t.Fatal(err)
	}
	bidderConnection.write.Reset()
	if err := service.sendCurrentAuctionBiddingItemsLocked(bidder, []byte{currentAuctionPayTypeGold}); err != nil {
		t.Fatal(err)
	}
	outbidACK, _ := splitGameServerUpperPacket(t, bidderConnection.write.Bytes())
	if !bytes.Equal(outbidACK.Body, []byte{1, 0, 0, 0}) {
		t.Fatalf("outbid bidder still listed: body=%x", outbidACK.Body)
	}
	rivalConnection.write.Reset()
	if err := service.sendCurrentAuctionBiddingItemsLocked(rival, []byte{currentAuctionPayTypeGold}); err != nil {
		t.Fatal(err)
	}
	rivalACK, _ := splitGameServerUpperPacket(t, rivalConnection.write.Bytes())
	if len(rivalACK.Body) != 1+3+0x9F || binary.LittleEndian.Uint32(rivalACK.Body[4:][0x08:0x0C]) != 120 {
		t.Fatalf("new leader my-bidding body=%x", rivalACK.Body)
	}
}

func buildCurrentAuctionRegisterRequestForTest(slot uint16, itemID uint32, count, startPrice, instantPrice, unitPrice int32, durationHours uint32) []byte {
	body := make([]byte, currentAuctionRegisterRequestSize)
	body[0] = currentAuctionPayTypeGold
	body[1] = currentExeInventoryMainListType
	binary.LittleEndian.PutUint16(body[2:4], slot)
	binary.LittleEndian.PutUint32(body[4:8], itemID)
	binary.LittleEndian.PutUint32(body[8:12], uint32(count))
	binary.LittleEndian.PutUint32(body[12:16], uint32(startPrice))
	binary.LittleEndian.PutUint32(body[16:20], uint32(instantPrice))
	binary.LittleEndian.PutUint32(body[20:24], uint32(unitPrice))
	binary.LittleEndian.PutUint32(body[24:28], durationHours)
	return body
}

func TestCurrentAuctionRegisterDecodesActiveClientRequest(t *testing.T) {
	body := []byte{
		0x00, 0x00, 0x09, 0x00, 0x48, 0x83, 0x00, 0x00,
		0x01, 0x00, 0x00, 0x00, 0xff, 0xff, 0xff, 0xff,
		0x04, 0x29, 0x00, 0x00, 0x04, 0x29, 0x00, 0x00,
		0x18, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00,
	}
	request, err := decodeCurrentAuctionRegisterRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if request.PayType != 0 || request.ListType != 0 || request.Slot != 9 ||
		request.ItemID != 33608 || request.Count != 1 || request.StartPrice != -1 ||
		request.InstantPrice != 10500 || request.UnitPrice != 10500 || request.DurationHours != 24 {
		t.Fatalf("decoded active request=%+v", request)
	}
}

func TestCurrentAuctionRegisterDecodesCapturedMaterialStackRequest(t *testing.T) {
	// Active 20260827-r2 capture for two item 3051 materials at 1,500 gold
	// each. Both instant and unit fields are per-item; the UI separately shows
	// the computed 3,000-gold stack total.
	body := []byte{
		0x00, 0x00, 0xa4, 0x00, 0xeb, 0x0b, 0x00, 0x00,
		0x02, 0x00, 0x00, 0x00, 0xff, 0xff, 0xff, 0xff,
		0xdc, 0x05, 0x00, 0x00, 0xdc, 0x05, 0x00, 0x00,
		0x18, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00,
	}
	request, err := decodeCurrentAuctionRegisterRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if request.PayType != 0 || request.ListType != 0 || request.Slot != 164 ||
		request.ItemID != 3051 || request.Count != 2 || request.StartPrice != -1 ||
		request.InstantPrice != 1500 || request.UnitPrice != 1500 || request.DurationHours != 24 {
		t.Fatalf("decoded material registration=%+v", request)
	}

	legacyTotal := append([]byte(nil), body...)
	binary.LittleEndian.PutUint32(legacyTotal[16:20], 3000)
	if _, err := decodeCurrentAuctionRegisterRequest(legacyTotal); err == nil {
		t.Fatalf("invented stack-total instant price unexpectedly accepted: %x", legacyTotal)
	}
}

func TestCurrentAuctionRowsEncodeChineseSellerNameAsGB18030(t *testing.T) {
	listing := dnfauction.Listing{
		ID: 1, SellerID: "10", SellerName: "卖家", UnitPrice: 20,
		CreatedAt: time.Unix(100, 0), ExpiresAt: time.Unix(100, 0).Add(time.Hour),
		Item: dnfrepo.ItemStack{ItemID: 6001, Count: 1},
	}
	body := buildCurrentAuctionSearchBody([]dnfauction.Listing{listing}, func(uint32) bool { return true })
	want := rosterNameBytes("卖家")
	if len(body) != 7+0xA8 || !bytes.Equal(body[7+0x15:7+0x15+len(want)], want) {
		t.Fatalf("seller name row=%x want=%x", body, want)
	}
}

func TestCurrentAuctionCategorySearchAndAveragePriceUseCurrentReaders(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedCurrentAuctionBridgeCharacter(t, ctx, repositories, "10", "account-seller", "卖家", 20_000, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 6001, Count: 3},
	})
	owner, _ := dnfauction.NewOwner(repositories)
	if _, err := owner.Register(ctx, dnfauction.RegisterCommand{ListingID: 51, SellerID: "10", AccountID: "account-seller", SellerName: "卖家", ListType: 0, SourceSlot: 9, ExpectedID: 6001, Count: 3, UnitPrice: 40, Duration: time.Hour, ListingLimit: 5}); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := &Service{
		options:                options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider:     func() (dnfrepo.Group, bool) { return repositories, true },
		auctionItemEligibility: func(itemID uint32) bool { return itemID == 6001 },
	}
	session := &gameSession{conn: connection, selectedCharacterID: 11, accountID: "account-buyer"}
	category := make([]byte, 20)
	category[0] = currentAuctionPayTypeGold
	if err := service.searchCurrentAuctionCategoryLocked(session, category); err != nil {
		t.Fatal(err)
	}
	search, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || search.Header.MsgID != uint16(dnfenum.CmdPacketAuctionSearchByItemkey) || len(search.Body) != 1+7+0xA8 || search.Body[0] != 1 {
		t.Fatalf("category response header=%+v body=%x trailing=%d", search.Header, search.Body, len(trailing))
	}
	connection.write.Reset()
	average := make([]byte, 8)
	average[0] = currentAuctionPayTypeGold
	// Current NoPack op182 sender places the item ID directly after pay.
	binary.LittleEndian.PutUint32(average[1:5], 6001)
	if err := service.sendCurrentAuctionAveragePriceLocked(session, average); err != nil {
		t.Fatal(err)
	}
	price, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || price.Header.MsgID != uint16(dnfenum.CmdPacketAuctionAskAveragePrice) || len(price.Body) != 58 ||
		price.Body[0] != 1 || price.Body[1] != currentAuctionPayTypeGold || binary.LittleEndian.Uint32(price.Body[2:6]) != 40 ||
		binary.LittleEndian.Uint32(price.Body[6:10]) != 3 || binary.LittleEndian.Uint32(price.Body[10:14]) != 40 {
		t.Fatalf("average response header=%+v body=%x trailing=%d", price.Header, price.Body, len(trailing))
	}
}

func TestCurrentAuctionRegistrationPolicyMatchesTradeableEquipmentRarities(t *testing.T) {
	definition := dungeonDropItemDefinition{
		ItemID: 7001, Kind: dungeonDropItemEquipment, AttachType: "[free]",
		AuctionRuleFound: true, AuctionAllowed: true,
	}
	plain := dnfrepo.ItemStack{ItemID: 7001, Count: 1, RawEntry: make([]byte, currentItemListEntryWireSize)}
	sealed := plain
	sealed.Bind = true
	sealed.RawEntry = append([]byte(nil), plain.RawEntry...)
	sealed.RawEntry[0x0D] = 1

	for _, test := range []struct {
		name         string
		rarity       int64
		stack        dnfrepo.ItemStack
		wantExpected bool
		wantAllow    bool
		wantEligible bool
	}{
		// `[free]` 不看封装状态，品级也不参与判断。
		{name: "white-unsealed", rarity: 0, stack: plain, wantEligible: true},
		{name: "white-sealed", rarity: 0, stack: sealed, wantExpected: true, wantAllow: true, wantEligible: true},
		{name: "blue-unsealed", rarity: 1, stack: plain, wantEligible: true},
		{name: "blue-sealed", rarity: 1, stack: sealed, wantExpected: true, wantAllow: true, wantEligible: true},
		{name: "purple-unsealed", rarity: 2, stack: plain, wantEligible: true},
		{name: "purple-sealed", rarity: 2, stack: sealed, wantExpected: true, wantAllow: true, wantEligible: true},
		{name: "unique-unsealed", rarity: 3, stack: plain, wantEligible: true},
		{name: "unique-sealed", rarity: 3, stack: sealed, wantExpected: true, wantAllow: true, wantEligible: true},
		{name: "epic-unsealed", rarity: 4, stack: plain, wantEligible: true},
		{name: "legendary-unsealed", rarity: 6, stack: plain, wantEligible: true},
		{name: "legendary-sealed", rarity: 6, stack: sealed, wantExpected: true, wantAllow: true, wantEligible: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := definition
			candidate.Rarity = test.rarity
			expected, allow, eligible := currentAuctionRegistrationPolicyForDefinition(candidate, test.stack)
			if expected != test.wantExpected || allow != test.wantAllow || eligible != test.wantEligible {
				t.Fatalf("policy=(expected=%t allow=%t eligible=%t), want (%t,%t,%t)", expected, allow, eligible, test.wantExpected, test.wantAllow, test.wantEligible)
			}
		})
	}

	// `[sealing]` 必须处于封装态才可上架，解封件拒绝；品级仍然不参与。
	sealing := dungeonDropItemDefinition{
		ItemID: 7001, Kind: dungeonDropItemEquipment, AttachType: "[sealing]", Rarity: 3,
		AuctionRuleFound: true, AuctionAllowed: true,
	}
	if _, _, eligible := currentAuctionRegistrationPolicyForDefinition(sealing, plain); eligible {
		t.Fatal("unsealed [sealing] equipment must be rejected")
	}
	if _, _, eligible := currentAuctionRegistrationPolicyForDefinition(sealing, sealed); !eligible {
		t.Fatal("sealed [sealing] equipment must be eligible")
	}

	// 其余 attach type 一律拒绝，品级高低都不改变结论。
	for _, attach := range []string{"[trade]", "[account]", "[trade delete]", ""} {
		other := dungeonDropItemDefinition{
			ItemID: 7001, Kind: dungeonDropItemEquipment, AttachType: attach, Rarity: 1,
			AuctionRuleFound: true, AuctionAllowed: true,
		}
		if _, _, eligible := currentAuctionRegistrationPolicyForDefinition(other, plain); eligible {
			t.Fatalf("attach=%q must not be listable", attach)
		}
	}

	stackable := dungeonDropItemDefinition{
		ItemID: 7001, Kind: dungeonDropItemStackable, AttachType: "[free]",
		AuctionRuleFound: true, AuctionAllowed: true,
	}
	if _, _, eligible := currentAuctionRegistrationPolicyForDefinition(stackable, plain); !eligible {
		t.Fatal("tradeable unbound consumable/material must be eligible")
	}
	if _, _, eligible := currentAuctionRegistrationPolicyForDefinition(stackable, sealed); eligible {
		t.Fatal("bound consumable/material must be rejected")
	}
	stackable.AuctionAllowed = false
	if _, _, eligible := currentAuctionRegistrationPolicyForDefinition(stackable, plain); eligible {
		t.Fatal("PVF auction-prohibited stackable must be rejected")
	}
	stackable.AuctionRuleFound = false
	stackable.AttachType = "[account]"
	if _, _, eligible := currentAuctionRegistrationPolicyForDefinition(stackable, plain); eligible {
		t.Fatal("non-tradeable stackable must be rejected")
	}
}

func TestCurrentAuctionGenericFailureCarriesRequiredPayFeature(t *testing.T) {
	for _, opcode := range []uint16{
		uint16(dnfenum.CmdPacketAuctionAskAveragePrice),
		uint16(dnfenum.CmdPacketAuctionRegistItem),
		uint16(dnfenum.CmdPacketAuctionRegistCancel),
		uint16(dnfenum.CmdPacketAuctionBidding),
		uint16(dnfenum.CmdPacketAuctionSearchByItemkey),
		uint16(dnfenum.CmdPacketAuctionMyRegistedItemInfo),
		uint16(dnfenum.CmdPacketAuctionMyBiddingInfo),
	} {
		connection := &bufferConn{}
		service := &Service{options: options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}}
		if err := service.sendGameUpperFailure(&gameSession{conn: connection}, opcode, currentAuctionFailureCode); err != nil {
			t.Fatal(err)
		}
		packet, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
		if len(trailing) != 0 || !bytes.Equal(packet.Body, []byte{0, currentAuctionFailureCode, currentAuctionPayTypeGold}) {
			t.Fatalf("opcode=%d failure=%x trailing=%d", opcode, packet.Body, len(trailing))
		}
	}
	if currentAuctionFailureNeedsPayFeature(uint16(dnfenum.CmdPacketAuctionBuyItemApiece), currentAuctionFailureCode) {
		t.Fatal("op334 must not carry a pay feature on failure")
	}
}

func TestCurrentAuctionFailureCodesStayInsideClientTable(t *testing.T) {
	rendered := map[byte]bool{7: true, 8: true, 214: true, 235: true}
	for code := byte(140); code <= 158; code++ {
		rendered[code] = true
	}
	codes := map[string]byte{
		"generic":       currentAuctionFailureCode,
		"gold_short":    currentAuctionErrorGoldShort,
		"item_rejected": currentAuctionErrorItemRejected,
		"count":         currentAuctionErrorCountTooLarge,
		"item_missing":  currentAuctionErrorItemMissing,
		"no_listing":    currentAuctionErrorNoListing,
		"listing_limit": currentAuctionErrorListingLimit,
		"price_invalid": currentAuctionErrorPriceInvalid,
		"unknown":       currentAuctionErrorUnknown,
		"forbidden":     currentAuctionErrorForbidden,
		"own_listing":   currentAuctionErrorOwnListing,
		"bid_mismatch":  currentAuctionErrorPriceMismatch,
		"item_locked":   currentAuctionErrorItemLocked,
	}
	for name, code := range codes {
		if !rendered[code] {
			t.Fatalf("%s code %d is outside the client's rendered table", name, code)
		}
		if !currentAuctionFailureNeedsPayFeature(uint16(dnfenum.CmdPacketAuctionBidding), code) {
			t.Fatalf("%s code %d must still carry the pay-feature byte", name, code)
		}
	}
	for name, probe := range map[string]struct {
		err  error
		want byte
	}{
		"price_mismatch": {dnfauction.ErrPriceMismatch, currentAuctionErrorPriceInvalid},
		"own_listing":    {dnfauction.ErrBuyerIsSeller, currentAuctionErrorOwnListing},
		"cannot_afford":  {dnfauction.ErrGoldUnavailable, currentAuctionErrorGoldShort},
		"gone":           {dnfauction.ErrListingMissing, currentAuctionErrorNoListing},
		"expired":        {dnfauction.ErrListingExpired, currentAuctionErrorNoListing},
		"item_gone":      {dnfauction.ErrItemUnavailable, currentAuctionErrorItemMissing},
		"other":          {dnfauction.ErrInvalidRequest, currentAuctionErrorUnknown},
	} {
		if got := currentAuctionBuyFailureCode(probe.err); got != probe.want {
			t.Fatalf("%s: buy failure code=%d, want %d", name, got, probe.want)
		}
	}
	// A bid that loses to the standing one is exactly what 157
	// 竞拍金额不符， 可能存在更高价格的竞拍者 describes, and with bids recorded
	// that is now true rather than an invented rival. A buyout-only listing has
	// no bid to beat, so that one stays on 152.
	for name, probe := range map[string]struct {
		err  error
		want byte
	}{
		"too_low":         {dnfauction.ErrBidTooLow, currentAuctionErrorPriceMismatch},
		"no_bidding":      {dnfauction.ErrBiddingNotAllowed, currentAuctionErrorPriceInvalid},
		"own_listing":     {dnfauction.ErrBuyerIsSeller, currentAuctionErrorOwnListing},
		"cannot_afford":   {dnfauction.ErrGoldUnavailable, currentAuctionErrorGoldShort},
		"listing_expired": {dnfauction.ErrListingExpired, currentAuctionErrorNoListing},
		"other":           {dnfauction.ErrInvalidRequest, currentAuctionErrorUnknown},
	} {
		if got := currentAuctionBidFailureCode(probe.err); got != probe.want {
			t.Fatalf("%s: bid failure code=%d, want %d", name, got, probe.want)
		}
	}
	for name, probe := range map[string]struct {
		stage string
		cause error
		want  byte
	}{
		"decode":          {"decode", dnfauction.ErrInvalidRequest, currentAuctionErrorPriceInvalid},
		"duration":        {"duration", dnfauction.ErrInvalidRequest, currentAuctionErrorPriceInvalid},
		"source_mismatch": {"source_mismatch", dnfauction.ErrItemUnavailable, currentAuctionErrorItemMissing},
		"item_locked":     {"item_locked", dnfauction.ErrItemUnavailable, currentAuctionErrorItemLocked},
		"item_policy":     {"item_policy", dnfauction.ErrItemUnavailable, currentAuctionErrorItemRejected},
		"count":           {"count", dnfauction.ErrItemUnavailable, currentAuctionErrorCountTooLarge},
		"deposit_gold":    {"deposit_gold", dnfauction.ErrGoldUnavailable, currentAuctionErrorGoldShort},
		"limit":           {"transaction", dnfauction.ErrListingLimit, currentAuctionErrorListingLimit},
		"transaction":     {"transaction", dnfauction.ErrUnavailable, currentAuctionErrorUnknown},
	} {
		if got := currentAuctionRegisterFailureCode(probe.stage, probe.cause); got != probe.want {
			t.Fatalf("%s: register failure code=%d, want %d", name, got, probe.want)
		}
	}
}

func seedCurrentAuctionBridgeCharacter(t *testing.T, ctx context.Context, repositories dnfrepo.Group, characterID, accountID, name string, gold int64, slots map[string]dnfrepo.ItemStack) {
	t.Helper()
	if slots == nil {
		slots = make(map[string]dnfrepo.ItemStack)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: characterID, AccountID: accountID, Name: name, Stats: map[string]int64{"gold": gold}}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: characterID, Slots: slots}); err != nil {
		t.Fatal(err)
	}
}

// 测试固定编号从真实托管账本播种，服务重建后仍沿用同一序列。
func seedAuctionSequenceForBridge(t *testing.T, s *Service, sequence int64) {
	t.Helper()
	ctx := context.Background()
	r, ok := s.repositoryProvider()
	if !ok {
		t.Fatal("拍卖仓储不可用")
	}
	o, err := dnfauction.NewOwner(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.EnsureEscrow(ctx); err != nil {
		t.Fatal(err)
	}
	c, found, err := r.Character.Load(ctx, dnfauction.EscrowCharacterID)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	c.Stats["auction_listing_sequence"] = sequence
	if err := r.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
}
