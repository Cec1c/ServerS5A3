package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	dnfauction "longheng.io/server/internal/modules/dnf/auction"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// The current auction request carries an absolute record offset in body[1:5].
// It is not a page number. A 125-record material result has thirteen client
// pages; the first batch is 60 records, and offsets 60/120 must return the next
// two batches rather than being multiplied by a server page size.
func TestCurrentAuctionMaterialSearchUsesRecordOffsetAndFilteredTotal(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()

	const (
		materialItemID = int64(6001)
		excludedItemID = int64(6002)
		totalListings  = 150
	)
	slots := make(map[string]dnfrepo.ItemStack, totalListings)
	for index := 0; index < totalListings; index++ {
		itemID := materialItemID
		// Five matching materials followed by one item from another category
		// keeps filtering and paging interleaved instead of merely filtering the
		// tail after a page boundary.
		if index%6 == 5 {
			itemID = excludedItemID
		}
		slots[fmt.Sprintf("0:%d", index)] = dnfrepo.ItemStack{ItemID: itemID, Count: 1}
	}
	seedCurrentAuctionBridgeCharacter(t, ctx, repositories, "10", "account-seller", "卖家", 20_000, slots)
	owner, err := dnfauction.NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < totalListings; index++ {
		itemID := materialItemID
		if index%6 == 5 {
			itemID = excludedItemID
		}
		if _, err := owner.Register(ctx, dnfauction.RegisterCommand{
			ListingID: uint64(index + 1), SellerID: "10", AccountID: "account-seller", SellerName: "卖家",
			ListType: 0, SourceSlot: int16(index), ExpectedID: itemID, Count: 1,
			UnitPrice: 40, Duration: time.Hour, ListingLimit: 200,
		}); err != nil {
			t.Fatalf("register listing %d: %v", index+1, err)
		}
	}

	connection := &bufferConn{}
	service := &Service{
		options:                options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider:     func() (dnfrepo.Group, bool) { return repositories, true },
		auctionItemEligibility: func(uint32) bool { return true },
		auctionCategoryItem: func(code uint16, itemID uint32) bool {
			return code == 13002 && itemID == uint32(materialItemID)
		},
	}
	session := &gameSession{conn: connection, selectedCharacterID: 11, accountID: "account-buyer"}

	for _, test := range []struct {
		name      string
		offset    uint32
		wantCount uint16
	}{
		{name: "first batch", offset: 0, wantCount: 60},
		{name: "second batch", offset: 60, wantCount: 60},
		// This is the transition the defective server produced after returning
		// 100 records. The client asks for record 100 on page eleven; the offset
		// must remain 100, not become page 100 multiplied by the server batch.
		{name: "post hundred transition", offset: 100, wantCount: 25},
		{name: "final partial batch", offset: 120, wantCount: 5},
		{name: "last record", offset: 124, wantCount: 1},
		{name: "first offset past tail", offset: 125, wantCount: 0},
		{name: "farther offset stays empty", offset: 126, wantCount: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := make([]byte, 20)
			request[0] = currentAuctionPayTypeGold
			binary.LittleEndian.PutUint32(request[1:5], test.offset)
			binary.LittleEndian.PutUint16(request[5:7], 13002) // 消耗品 > 材料
			connection.write.Reset()
			if err := service.searchCurrentAuctionCategoryLocked(session, request); err != nil {
				t.Fatal(err)
			}
			packet, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
			if len(trailing) != 0 || packet.Header.MsgID != uint16(dnfenum.CmdPacketAuctionSearchByItemkey) {
				t.Fatalf("offset=%d header=%+v trailing=%d", test.offset, packet.Header, len(trailing))
			}
			body := packet.Body
			if len(body) < 8 || body[0] != 1 {
				t.Fatalf("offset=%d body=%x, want success header", test.offset, body)
			}
			total := binary.LittleEndian.Uint32(body[2:6])
			count := binary.LittleEndian.Uint16(body[6:8])
			if total != 125 {
				t.Fatalf("offset=%d total=%d, want the full filtered count 125", test.offset, total)
			}
			if count != test.wantCount {
				t.Fatalf("offset=%d count=%d, want %d; body_len=%d", test.offset, count, test.wantCount, len(body))
			}
			if len(body) != 8+int(count)*0xA8 {
				t.Fatalf("offset=%d body_len=%d, want %d", test.offset, len(body), 8+int(count)*0xA8)
			}
			for rowIndex := 0; rowIndex < int(count); rowIndex++ {
				row := body[8+rowIndex*0xA8 : 8+(rowIndex+1)*0xA8]
				itemID := binary.LittleEndian.Uint32(row[0x2B+0x01 : 0x2B+0x05])
				if itemID != uint32(materialItemID) {
					t.Fatalf("offset=%d row=%d item=%d, leaked non-material row", test.offset, rowIndex, itemID)
				}
			}
		})
	}

	if pages := (125 + 9) / 10; pages != 13 {
		t.Fatalf("fixture pages=%d, want the reported 13-page boundary", pages)
	}
}
