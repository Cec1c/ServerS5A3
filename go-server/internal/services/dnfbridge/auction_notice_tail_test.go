package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	dnfauction "longheng.io/server/internal/modules/dnf/auction"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

// The native class0/op169 reader always reads an option-count byte after the
// route, kind, item ID and u16. This is independent of the notice route/kind.
func TestCurrentAuctionNoticeIncludesEmptyOptionCount(t *testing.T) {
	for _, route := range []byte{currentAuctionNoticeRouteItem, currentAuctionNoticeRouteGold} {
		for _, kind := range []byte{currentAuctionNoticeKindOutbid, currentAuctionNoticeKindSellerSold,
			currentAuctionNoticeKindBuyerWon, currentAuctionNoticeKindSellerFailed, currentAuctionNoticeKindCancelBid} {
			t.Run(fmt.Sprintf("route%d-kind%d", route, kind), func(t *testing.T) {
				conn := &bufferConn{}
				session := &gameSession{conn: conn, selectedCharacterID: 10}
				s := &Service{
					options:      options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
					gameSessions: map[uint16]*gameSession{10: session},
				}
				if err := s.sendCurrentAuctionNoticeToOnlineRecipient(10, route, kind, 100170220); err != nil {
					t.Fatal(err)
				}
				p, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				if p.Header.Classification != 0 || p.Header.MsgID != 169 || len(rest) != 0 {
					t.Fatalf("notice header=%+v trailing=%x", p.Header, rest)
				}
				assertAuctionNoticeOptionTail(t, p.Body, route, kind, 100170220)
			})
		}
	}
}

func assertAuctionNoticeOptionTail(t *testing.T, body []byte, route, kind byte, itemID uint32) {
	t.Helper()
	// Explicit wire contract, not the production builder as its own oracle.
	if len(body) != 9 {
		t.Fatalf("op169 body=%x length=%d: native reader requires the ninth option-count byte", body, len(body))
	}
	if body[0] != route || body[1] != kind || binary.LittleEndian.Uint32(body[2:6]) != itemID ||
		binary.LittleEndian.Uint16(body[6:8]) != 0 || body[8] != 0 {
		t.Fatalf("op169 body=%x want route=%d kind=%d item=%d, u16=0, option count=0", body, route, kind, itemID)
	}
	t.Logf("native_notice_body=%x", body)
}

func assertAuctionDeliveredNotice(t *testing.T, conn *bufferConn, kind byte, itemID uint32) {
	t.Helper()
	assertAuctionDeliveredNoticeWithRoute(t, conn, currentAuctionNoticeRouteItem, kind, itemID, 13)
}

func assertAuctionDeliveredNoticeWithRoute(t *testing.T, conn *bufferConn, route, kind byte, itemID uint32, headerSize int) {
	t.Helper()
	alarm, notices := false, 0
	for rest := conn.write.Bytes(); len(rest) > 0; {
		p, tail := splitGameServerUpperPacketWithHeader(t, rest, headerSize)
		rest = tail
		if p.Header.Classification != 0 {
			continue
		}
		switch p.Header.MsgID {
		case 99:
			alarm = true
		case 169:
			if !alarm {
				t.Fatal("auction notice preceded the mailbox alarm")
			}
			assertAuctionNoticeOptionTail(t, p.Body, route, kind, itemID)
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("auction notices=%d want 1", notices)
	}
}

func TestCurrentAuctionGoldMailNoticeLifecycle(t *testing.T) {
	for _, action := range []string{"buy", "cancel"} {
		t.Run(action, func(t *testing.T) {
			r, s, seller, conn := goldBridgeFixture(t)
			buyerConn := &bufferConn{}
			buyer := &gameSession{conn: buyerConn, selectedCharacterID: 11, accountID: "a-11"}
			s.gameSessions = map[uint16]*gameSession{10: seller, 11: buyer}
			if err := s.handleCurrentAuctionCommand(seller, 183, goldRegisterBody()); err != nil {
				t.Fatal(err)
			}
			owner, err := dnfauction.NewGoldConsignOwner(r)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := owner.Search(context.Background(), nil)
			if err != nil || len(rows) != 1 {
				t.Fatalf("gold listings=%+v err=%v", rows, err)
			}
			conn.write.Reset()
			if action == "buy" {
				body := make([]byte, 17)
				body[0] = 1
				binary.LittleEndian.PutUint32(body[1:5], 1110)
				binary.LittleEndian.PutUint64(body[5:13], rows[0].ID)
				binary.LittleEndian.PutUint32(body[13:17], 2681725)
				if err := s.handleCurrentAuctionCommand(buyer, 185, body); err != nil {
					t.Fatal(err)
				}
				assertAuctionDeliveredNoticeWithRoute(t, buyerConn, 1, currentAuctionNoticeKindBuyerWon, 2681725, 16)
				assertAuctionDeliveredNoticeWithRoute(t, conn, 1, currentAuctionNoticeKindSellerSold, 2681725, 16)
			} else {
				body := make([]byte, 9)
				body[0] = 1
				binary.LittleEndian.PutUint64(body[1:9], rows[0].ID)
				if err := s.handleCurrentAuctionCommand(seller, 184, body); err != nil {
					t.Fatal(err)
				}
				assertAuctionDeliveredNoticeWithRoute(t, conn, 1, currentAuctionNoticeKindCancelBid, 2681725, 16)
			}
			if len(mustGoldMailbox(t, r, "10").Mails) != 1 {
				t.Fatal("gold-consignment mail missing")
			}
		})
	}
}

func TestCurrentAuctionMagicEquipmentMailNoticeLifecycle(t *testing.T) {
	for _, action := range []string{"buy", "cancel", "expire"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			r := memory.NewMemoryGroup()
			catalog := auctionRandomOptionCatalog(t)
			def, err := catalog.ResolveItem(100170220)
			if err != nil {
				t.Fatal(err)
			}
			item, err := buildCurrentDungeonPickupStack(22, def, 1, 123456789)
			if err != nil {
				t.Fatal(err)
			}
			original := dnfrepo.CloneInventory(dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{"0:22": item}}).Slots["0:22"]
			seedCurrentAuctionBridgeCharacter(t, ctx, r, "10", "seller", "Seller", 50000, map[string]dnfrepo.ItemStack{"0:22": item})
			seedCurrentAuctionBridgeCharacter(t, ctx, r, "11", "buyer", "Buyer", 100000, nil)
			sellerConn, buyerConn := &bufferConn{}, &bufferConn{}
			seller := &gameSession{conn: sellerConn, selectedCharacterID: 10, accountID: "seller"}
			buyer := &gameSession{conn: buyerConn, selectedCharacterID: 11, accountID: "buyer"}
			s := &Service{
				options:            options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
				pvfItemCatalog:     catalog,
				repositoryProvider: func() (dnfrepo.Group, bool) { return r, true },
				gameSessions:       map[uint16]*gameSession{10: seller, 11: buyer},
			}
			seedAuctionSequenceForBridge(t, s, 991)
			if err := s.registerCurrentAuctionItemLocked(seller, auctionRandomOptionRequest(t, "307500307500307500")); err != nil {
				t.Fatal(err)
			}
			owner, err := dnfauction.NewOwner(r)
			if err != nil {
				t.Fatal(err)
			}
			listings, err := owner.Search(ctx, nil)
			if err != nil || len(listings) != 1 || !reflect.DeepEqual(listings[0].Item, original) {
				t.Fatalf("listing=%+v err=%v", listings, err)
			}
			listing := listings[0]
			sellerConn.write.Reset()
			id, conn, kind, source := "10", sellerConn, currentAuctionNoticeKindCancelBid, "auction_listing_return"
			wantMailGold := int64(0)
			switch action {
			case "buy":
				body := make([]byte, 30)
				binary.LittleEndian.PutUint32(body[1:5], 60900)
				binary.LittleEndian.PutUint64(body[5:13], listing.ID)
				binary.LittleEndian.PutUint32(body[26:30], 100170220)
				if err := s.buyCurrentAuctionItemLocked(buyer, 185, body, false); err != nil {
					t.Fatal(err)
				}
				id, conn, kind, source = "11", buyerConn, currentAuctionNoticeKindBuyerWon, "auction_purchase_delivery"
				assertAuctionDeliveredNotice(t, sellerConn, currentAuctionNoticeKindSellerSold, 100170220)
			case "cancel":
				body := make([]byte, 9)
				binary.LittleEndian.PutUint64(body[1:9], listing.ID)
				if err := s.cancelCurrentAuctionItemLocked(seller, body); err != nil {
					t.Fatal(err)
				}
			case "expire":
				escrow, _, err := r.Inventory.Load(ctx, dnfauction.EscrowCharacterID)
				if err != nil {
					t.Fatal(err)
				}
				for key, stack := range escrow.Slots {
					if strings.HasPrefix(key, "auction:listing:") {
						stack.Extra["expires_at"] = strconv.FormatInt(time.Now().UTC().Add(-time.Hour).Unix(), 10)
						escrow.Slots[key] = stack
					}
				}
				if err := r.Inventory.Save(ctx, escrow); err != nil {
					t.Fatal(err)
				}
				if err := s.sendCurrentAuctionOwnedItemsLocked(seller, []byte{0}); err != nil {
					t.Fatal(err)
				}
				kind, source, wantMailGold = currentAuctionNoticeKindSellerFailed, "auction_listing_expired", dnfauction.ListingDepositGold
			}
			assertAuctionDeliveredNotice(t, conn, kind, 100170220)
			mailbox, found, err := r.Mailbox.Load(ctx, id)
			if err != nil || !found || len(mailbox.Mails) != 1 {
				t.Fatalf("mailbox=%+v found=%t err=%v", mailbox, found, err)
			}
			for _, m := range mailbox.Mails {
				if m.Metadata["source"] != source || m.Gold != wantMailGold || len(m.Attachments) != 1 {
					t.Fatalf("mail=%+v", m)
				}
				a := m.Attachments[0]
				if a.ItemID != original.ItemID || a.Count != original.Count || a.Bind != original.Bind ||
					!a.ExpireAt.Equal(original.ExpireAt) || !bytes.Equal(a.RawEntry, original.RawEntry) || !reflect.DeepEqual(a.Extra, original.Extra) {
					t.Fatal("empty notice summary changed the real equipment attachment")
				}
			}
			// The notice is separate from durable delivery. Subsequent mailbox/outbox
			// refreshes must not produce another letter or another item notice.
			sellerConn.write.Reset()
			buyerConn.write.Reset()
			s.retryCurrentAuctionMailsLocked(seller)
			s.retryCurrentAuctionMailsLocked(buyer)
			after, _, err := r.Mailbox.Load(ctx, id)
			if err != nil || !reflect.DeepEqual(mailbox, after) || sellerConn.write.Len() != 0 || buyerConn.write.Len() != 0 {
				t.Fatal("completed delivery retried or changed the mailbox")
			}
		})
	}
}
