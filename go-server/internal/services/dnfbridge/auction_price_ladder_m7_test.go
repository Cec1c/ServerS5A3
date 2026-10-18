package dnfbridge

// The m7 pass: the two item-auction reports that came out of the 无色小晶块 test.
//
//  1. 「拍卖行最低价(以一口价为准)」 showed SIX identical rows for a market with
//     one listing. The op182 response's last twelve dwords are not trade history:
//     FUN_01d1d720 reads exactly two groups of six and hands them to the panel
//     filler (FUN_0300e2f0 -> FUN_0300a520 for owner 7), and FUN_0300b190 paints
//     each pair as dstr 46422 「%d个」 + dstr 46252/60160 「每个 %s 金币」, with
//     dstr 46450 「暂无拍卖品」 for a slot whose price or quantity is below 1.
//     Repeating the average in all six claimed six offers that did not exist.
//
//  2. The buyer could only take the whole stack. A buyout-only listing published
//     its unit price in the 当前竞拍价 slot, so the client believed a start price
//     was set and appended dstr 46263 「已设定拍卖起始价的物品无法单个购买。」
//     to the confirm dialog. The slot has to carry the -1 sentinel the painters
//     test (see currentAuctionNoPrice).

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	dnfauction "longheng.io/server/internal/modules/dnf/auction"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// ---------------------------------------------------------------------------
// 2. The buyout-only row must publish -1 in the 当前竞拍价 slot.
// ---------------------------------------------------------------------------

// TestCurrentAuctionBuyoutOnlyRowCarriesTheNoBidSentinel replays the live case:
// item 3037 (无色小晶块) x2000 registered at 一口价 100 with 竞拍价 left empty.
// The op183 body the client sent was
//
//	00 24 6601 DD0B0000 D0070000 FFFFFFFF 64000000 64000000 18000000
//	          item     count    bid=-1   instant  unit     duration
//
// so the row must come back with -1 where the seller has no bid, and the whole
// stack total 200000 in the 一口价 pair. Before this fix the row carried
// 200000/200000/100 and the client offered no single-unit purchase.
func TestCurrentAuctionBuyoutOnlyRowCarriesTheNoBidSentinel(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedCurrentAuctionBridgeCharacter(t, ctx, repositories, "10", "account-seller", "卖家", 20_000, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 3037, Count: 2000},
	})
	owner, _ := dnfauction.NewOwner(repositories)
	if _, err := owner.Register(ctx, dnfauction.RegisterCommand{
		ListingID: 51, SellerID: "10", AccountID: "account-seller", SellerName: "卖家",
		ListType: 0, SourceSlot: 9, ExpectedID: 3037, Count: 2000, UnitPrice: 100,
		StartPrice: -1, BuyoutPrice: 100, Duration: time.Hour, ListingLimit: 5,
	}); err != nil {
		t.Fatal(err)
	}
	listings, err := owner.Search(ctx, []uint32{3037})
	if err != nil || len(listings) != 1 {
		t.Fatalf("search=%+v err=%v", listings, err)
	}
	if listings[0].HasBidding() {
		t.Fatalf("registration invented a start price: %+v", listings[0])
	}
	eligibility := func(uint32) bool { return true }
	row := buildCurrentAuctionSearchBody(listings, eligibility)[7:]
	bid := binary.LittleEndian.Uint32(row[0x09:0x0D])
	buyout := binary.LittleEndian.Uint32(row[0x0D:0x11])
	unit := binary.LittleEndian.Uint32(row[0x11:0x15])
	if bid != currentAuctionNoPrice {
		t.Fatalf("op186 当前竞拍价=%d (0x%08X) want the -1 sentinel; a number here makes the client refuse a single-unit buy",
			bid, bid)
	}
	if buyout != 200_000 || unit != 100 {
		t.Fatalf("op186 一口价=%d 单价=%d want 200000/100", buyout, unit)
	}
	owned := buildCurrentAuctionOwnedBody(listings, "10", eligibility)[2:]
	if got := binary.LittleEndian.Uint32(owned[0x08:0x0C]); got != currentAuctionNoPrice {
		t.Fatalf("op188 当前竞拍价=%d (0x%08X) want the -1 sentinel", got, got)
	}
	if got := binary.LittleEndian.Uint32(owned[0x0C:0x10]); got != 200_000 {
		t.Fatalf("op188 一口价=%d want 200000", got)
	}
	if got := binary.LittleEndian.Uint32(owned[0x93:0x97]); got != 100 {
		t.Fatalf("op188 单价=%d want 100", got)
	}
}

// The sentinel is shared with the gold consignment rows; pin that they cannot
// drift apart, since both painters compare against the same literal.
func TestCurrentAuctionNoPriceSentinelIsThePaintersLiteral(t *testing.T) {
	if currentAuctionNoPrice != 0xFFFFFFFF {
		t.Fatalf("sentinel=0x%08X want 0xFFFFFFFF", currentAuctionNoPrice)
	}
	if currentAuctionNoPrice == 0 {
		t.Fatal("the sentinel must not be zero - zero is a real price")
	}
}

// ---------------------------------------------------------------------------
// 1. The op182 ladder.
// ---------------------------------------------------------------------------

func TestCurrentAuctionPriceLadderMergesAndSortsByUnitPrice(t *testing.T) {
	listing := func(price int64, count int64) dnfauction.Listing {
		return dnfauction.Listing{UnitPrice: price, Item: dnfrepo.ItemStack{ItemID: 3037, Count: count}}
	}
	ladder := currentAuctionPriceLadder([]dnfauction.Listing{
		listing(120, 300),
		listing(100, 2000),
		listing(100, 500), // same price as the previous one: merged, not repeated
	}, currentAuctionPriceLadderSlots)
	if len(ladder) != currentAuctionPriceLadderSlots {
		t.Fatalf("ladder length=%d want %d", len(ladder), currentAuctionPriceLadderSlots)
	}
	if ladder[0] != (currentAuctionPriceRung{unitPrice: 100, quantity: 2500}) {
		t.Fatalf("rung0=%+v want 100/2500", ladder[0])
	}
	if ladder[1] != (currentAuctionPriceRung{unitPrice: 120, quantity: 300}) {
		t.Fatalf("rung1=%+v want 120/300", ladder[1])
	}
	for index := 2; index < currentAuctionPriceLadderSlots; index++ {
		if ladder[index] != (currentAuctionPriceRung{}) {
			t.Fatalf("rung%d=%+v want the empty rung", index, ladder[index])
		}
	}

	// Junk rows never invent a rung.
	empty := currentAuctionPriceLadder([]dnfauction.Listing{
		listing(0, 5), listing(50, 0), listing(-3, 9),
	}, currentAuctionPriceLadderSlots)
	for index, rung := range empty {
		if rung != (currentAuctionPriceRung{}) {
			t.Fatalf("rung%d of the junk-only ladder=%+v want empty", index, rung)
		}
	}

	// More distinct prices than slots: the cheapest win, ascending.
	many := currentAuctionPriceLadder([]dnfauction.Listing{
		listing(700, 7), listing(100, 1), listing(300, 3), listing(200, 2),
		listing(600, 6), listing(500, 5), listing(400, 4),
	}, currentAuctionPriceLadderSlots)
	for index, want := range []uint32{100, 200, 300, 400, 500, 600} {
		if many[index].unitPrice != want {
			t.Fatalf("rung%d price=%d want %d", index, many[index].unitPrice, want)
		}
	}
}

// TestCurrentAuctionAverageResponseCarriesTheLadder walks the whole op182
// answer: pay byte, the average, the total count, then the six prices and the
// six quantities the 上架 dialog draws.
func TestCurrentAuctionAverageResponseCarriesTheLadder(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedCurrentAuctionBridgeCharacter(t, ctx, repositories, "10", "account-seller", "卖家", 20_000, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 3037, Count: 2000},
		"0:8": {ItemID: 3037, Count: 300},
	})
	owner, _ := dnfauction.NewOwner(repositories)
	for index, probe := range []struct {
		listingID uint64
		slot      int16
		count     int64
		price     int64
	}{
		{61, 9, 2000, 100},
		{62, 8, 300, 120},
	} {
		if _, err := owner.Register(ctx, dnfauction.RegisterCommand{
			ListingID: probe.listingID, SellerID: "10", AccountID: "account-seller", SellerName: "卖家",
			ListType: 0, SourceSlot: probe.slot, ExpectedID: 3037, Count: probe.count,
			UnitPrice: probe.price, StartPrice: -1, BuyoutPrice: probe.price,
			Duration: time.Hour, ListingLimit: 5,
		}); err != nil {
			t.Fatalf("register %d: %v", index, err)
		}
	}
	connection := &bufferConn{}
	service := &Service{
		options:                options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider:     func() (dnfrepo.Group, bool) { return repositories, true },
		auctionItemEligibility: func(itemID uint32) bool { return itemID == 3037 },
	}
	session := &gameSession{conn: connection, selectedCharacterID: 11, accountID: "account-buyer"}
	request := make([]byte, 8)
	request[0] = currentAuctionPayTypeGold
	binary.LittleEndian.PutUint32(request[1:5], 3037)
	if err := service.sendCurrentAuctionAveragePriceLocked(session, request); err != nil {
		t.Fatal(err)
	}
	packet, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 {
		t.Fatalf("trailing=%x", trailing)
	}
	if packet.Header.MsgID != uint16(dnfenum.CmdPacketAuctionAskAveragePrice) {
		t.Fatalf("header=%+v", packet.Header)
	}
	body := packet.Body
	// 1 success + 1 pay + 4 average + 4 count + 6*4 prices + 6*4 quantities.
	if len(body) != 2+4+4+6*4+6*4 {
		t.Fatalf("body len=%d want 58 (%x)", len(body), body)
	}
	if body[0] != 1 || body[1] != currentAuctionPayTypeGold {
		t.Fatalf("envelope=%x", body[:2])
	}
	// The average is (2000*100 + 300*120) / 2300 = 102.6 -> 102.
	if got := binary.LittleEndian.Uint32(body[2:6]); got != 102 {
		t.Fatalf("average=%d want 102", got)
	}
	if got := binary.LittleEndian.Uint32(body[6:10]); got != 2300 {
		t.Fatalf("count=%d want 2300", got)
	}
	wantPrices := []uint32{100, 120, 0, 0, 0, 0}
	wantCounts := []uint32{2000, 300, 0, 0, 0, 0}
	for index := 0; index < currentAuctionPriceLadderSlots; index++ {
		price := binary.LittleEndian.Uint32(body[10+index*4 : 14+index*4])
		quantity := binary.LittleEndian.Uint32(body[10+24+index*4 : 14+24+index*4])
		if price != wantPrices[index] || quantity != wantCounts[index] {
			t.Fatalf("rung%d=%d/%d want %d/%d (body=%x)", index, price, quantity,
				wantPrices[index], wantCounts[index], body)
		}
	}

	// With nothing on sale the ladder is empty and only the PVF reference price
	// fills the average, so the dialog can still open on a brand-new item.
	connection.write.Reset()
	empty := make([]byte, 8)
	empty[0] = currentAuctionPayTypeGold
	binary.LittleEndian.PutUint32(empty[1:5], 3001)
	if err := service.sendCurrentAuctionAveragePriceLocked(session, empty); err != nil {
		t.Fatal(err)
	}
	idle, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(idle.Body) != 58 {
		t.Fatalf("empty-market body len=%d want 58", len(idle.Body))
	}
	if got := binary.LittleEndian.Uint32(idle.Body[6:10]); got != 0 {
		t.Fatalf("empty-market count=%d want 0", got)
	}
	for index := 0; index < currentAuctionPriceLadderSlots; index++ {
		if got := binary.LittleEndian.Uint32(idle.Body[10+index*4 : 14+index*4]); got != 0 {
			t.Fatalf("empty-market rung%d price=%d want 0", index, got)
		}
	}
}
