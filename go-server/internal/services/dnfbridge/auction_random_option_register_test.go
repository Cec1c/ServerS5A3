package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"os"
	"reflect"
	"testing"
	"time"

	dnfauction "longheng.io/server/internal/modules/dnf/auction"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Bounded execution of the current native sender with a magic-sealed shoulder,
// slot 22, no opening bid, 60,900 buyout, and 24 hours. The final nine bytes are
// three {u16 option=30000,u8 value=0} records, not zero-filled reserved space.
const auctionMagicSealedRegisterHex = "00001600ec79f80501000000ffffffffe4ed0000e4ed000018000000307500307500307500"

func auctionRandomOptionRequest(t *testing.T, tail string) []byte {
	t.Helper()
	body, err := hex.DecodeString(auctionMagicSealedRegisterHex)
	if err != nil {
		t.Fatal(err)
	}
	if tail != "" {
		options, err := hex.DecodeString(tail)
		if err != nil || len(options) != 9 {
			t.Fatalf("invalid option fixture %q: %v", tail, err)
		}
		copy(body[28:], options)
	}
	return body
}

func TestCurrentAuctionRegisterNativeRandomOptionSuffix(t *testing.T) {
	for name, tail := range map[string]string{
		"unidentified": "307500307500307500",
		"identified":   "650007ca0009000000",
		"ordinary":     "000000000000000000",
	} {
		t.Run(name, func(t *testing.T) {
			r, err := decodeCurrentAuctionRegisterRequest(auctionRandomOptionRequest(t, tail))
			if err != nil {
				t.Fatal(err)
			}
			if r.ItemID != 100170220 || r.Slot != 22 || r.Count != 1 || r.StartPrice != -1 ||
				r.InstantPrice != 60900 || r.UnitPrice != 60900 || r.DurationHours != 24 || r.AccountOwned {
				t.Fatalf("native register decoded incorrectly: %+v", r)
			}
		})
	}
}

func auctionRandomOptionCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	catalog, err := newPVFDungeonDropCatalog(bridgePVFSource{
		"monster/monster.lst": "", "stackable/stackable.lst": "",
		"equipment/equipment.lst": "100170220 `shoulder.equ`\n",
		"equipment/shoulder.equ":  "[name]\n`密制花岗岩条纹肩铠`\n[rarity]\n2\n[random option]\n1\n[attach type]\n`[free]`\n[equipment type]\n`[shoulder]`\n[durability]\n30\n[price]\n60900\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestCurrentAuctionRegisterRandomOptionEscrowUsesInventory(t *testing.T) {
	testAuctionRandomOptionEscrow(t, auctionRandomOptionCatalog(t))
}

func testAuctionRandomOptionEscrow(t *testing.T, catalog *pvfDungeonDropCatalog) {
	t.Helper()
	definition, err := catalog.ResolveItem(100170220)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Kind != dungeonDropItemEquipment || normalizeMagicBoxPVFType(definition.AttachType) != "free" || definition.Price != 60900 {
		t.Fatalf("unexpected equipment definition: %+v", definition)
	}
	for name, tail := range map[string]string{
		"unidentified": "307500307500307500",
		"identified":   "650007ca0009000000",
		"forged_hint":  "ffffffffffffffffff",
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			r := memory.NewMemoryGroup()
			item, err := buildCurrentDungeonPickupStack(22, definition, 1, 123456789)
			if err != nil {
				t.Fatal(err)
			}
			original := dnfrepo.CloneInventory(dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{"0:22": item}}).Slots["0:22"]
			seedCurrentAuctionBridgeCharacter(t, ctx, r, "10", "seller-account", "Seller", 50000, map[string]dnfrepo.ItemStack{"0:22": item})
			conn := &bufferConn{}
			s := &Service{
				options:            options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
				pvfItemCatalog:     catalog,
				repositoryProvider: func() (dnfrepo.Group, bool) { return r, true },
			}
			seedAuctionSequenceForBridge(t, s, 991)
			session := &gameSession{conn: conn, selectedCharacterID: 10, accountID: "seller-account"}
			body := auctionRandomOptionRequest(t, tail)
			if err := s.registerCurrentAuctionItemLocked(session, body); err != nil {
				t.Fatal(err)
			}
			ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.MsgID != 183 || !bytes.Equal(ack.Body, []byte{1, 0}) {
				t.Fatalf("native magic-option registration refused: %x", ack.Body)
			}
			owner, err := dnfauction.NewOwner(r)
			if err != nil {
				t.Fatal(err)
			}
			listings, err := owner.Search(ctx, []uint32{100170220})
			if err != nil || len(listings) != 1 {
				t.Fatalf("listings=%+v err=%v", listings, err)
			}
			listing := listings[0]
			if !reflect.DeepEqual(listing.Item, original) || listing.UnitPrice != 60900 || listing.StartPrice != -1 {
				t.Fatalf("client suffix changed inventory instance or price: %+v", listing)
			}
			inventory, _, _ := r.Inventory.Load(ctx, "10")
			character, _, _ := r.Character.Load(ctx, "10")
			if len(inventory.Slots) != 0 || character.Stats["gold"] != 50000-dnfauction.ListingDepositGold {
				t.Fatal("escrow/deposit not committed together")
			}
			// Replaying the request cannot duplicate the item or debit a second deposit.
			conn.write.Reset()
			if err := s.registerCurrentAuctionItemLocked(session, body); err != nil {
				t.Fatal(err)
			}
			ack, _ = splitGameServerUpperPacket(t, conn.write.Bytes())
			character, _, _ = r.Character.Load(ctx, "10")
			if !bytes.Equal(ack.Body, []byte{0, currentAuctionErrorItemMissing, 0}) || character.Stats["gold"] != 40000 {
				t.Fatalf("replay ACK=%x gold=%d", ack.Body, character.Stats["gold"])
			}
			cancelled, err := owner.Cancel(ctx, dnfauction.CancelCommand{SellerID: "10", AccountID: "seller-account", ListingID: listing.ID})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.deliverCurrentAuctionItemMailLocked(ctx, owner, cancelled.Delivery, currentAuctionReturnMail); err != nil {
				t.Fatal(err)
			}
			mailbox, _, err := r.Mailbox.Load(ctx, "10")
			if err != nil || len(mailbox.Mails) != 1 {
				t.Fatalf("mailbox=%+v err=%v", mailbox, err)
			}
			for _, m := range mailbox.Mails {
				if len(m.Attachments) != 1 || !reflect.DeepEqual(m.Attachments[0].Extra, original.Extra) ||
					!bytes.Equal(m.Attachments[0].RawEntry, original.RawEntry) || m.Attachments[0].Count != 1 {
					t.Fatal("cancellation did not preserve server-owned equipment attributes")
				}
			}
		})
	}
}

func TestCurrentAuctionRegisterRandomOptionKeepsValidation(t *testing.T) {
	for name, change := range map[string]func([]byte) []byte{
		"short":              func(b []byte) []byte { return b[:36] },
		"long":               func(b []byte) []byte { return append(b, 0) },
		"wrong_container":    func(b []byte) []byte { b[1] = 1; return b },
		"wrong_account_slot": func(b []byte) []byte { b[1] = 36; return b },
		"wrong_price":        func(b []byte) []byte { binary.LittleEndian.PutUint32(b[20:], 60901); return b },
		"wrong_duration":     func(b []byte) []byte { binary.LittleEndian.PutUint32(b[24:], 72); return b },
		"zero_count":         func(b []byte) []byte { binary.LittleEndian.PutUint32(b[8:], 0); return b },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeCurrentAuctionRegisterRequest(change(auctionRandomOptionRequest(t, ""))); err == nil {
				t.Fatal("invalid registration accepted with random-option suffix")
			}
		})
	}
}

func TestRealPVFAuctionRandomOptionRegistration(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("runtime PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	testAuctionRandomOptionEscrow(t, catalog)
}

func TestCurrentAuctionRegisterRandomOptionKeepsAssetGuards(t *testing.T) {
	for _, tc := range []struct {
		name string
		code byte
	}{
		{"locked", currentAuctionErrorItemLocked},
		{"expired", currentAuctionErrorItemRejected},
		{"wrong_item", currentAuctionErrorItemMissing},
		{"excess_count", currentAuctionErrorCountTooLarge},
		{"deposit_short", currentAuctionErrorGoldShort},
		{"blocked_template", currentAuctionErrorItemRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r := memory.NewMemoryGroup()
			catalog := auctionRandomOptionCatalog(t)
			definition, err := catalog.ResolveItem(100170220)
			if err != nil {
				t.Fatal(err)
			}
			item, err := buildCurrentDungeonPickupStack(22, definition, 1, 123456789)
			if err != nil {
				t.Fatal(err)
			}
			body := auctionRandomOptionRequest(t, "")
			gold := int64(50000)
			switch tc.name {
			case "locked":
				item.Extra["equipment_lock_state"] = "1"
			case "expired":
				item.ExpireAt = time.Unix(1, 0)
			case "wrong_item":
				binary.LittleEndian.PutUint32(body[4:], 100170219)
			case "excess_count":
				binary.LittleEndian.PutUint32(body[8:], 2)
			case "deposit_short":
				gold = dnfauction.ListingDepositGold - 1
			case "blocked_template":
				catalog, err = newPVFDungeonDropCatalog(bridgePVFSource{
					"monster/monster.lst": "", "stackable/stackable.lst": "",
					"equipment/equipment.lst": "100170220 `blocked.equ`\n",
					"equipment/blocked.equ":   "[attach type]\n`[trade]`\n[equipment type]\n`[shoulder]`\n",
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			before := dnfrepo.CloneInventory(dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{"0:22": item}})
			seedCurrentAuctionBridgeCharacter(t, ctx, r, "10", "seller-account", "Seller", gold, before.Slots)
			conn := &bufferConn{}
			s := &Service{
				options:            options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
				pvfItemCatalog:     catalog,
				repositoryProvider: func() (dnfrepo.Group, bool) { return r, true },
			}
			seedAuctionSequenceForBridge(t, s, 991)
			session := &gameSession{conn: conn, selectedCharacterID: 10, accountID: "seller-account"}
			if err := s.registerCurrentAuctionItemLocked(session, body); err != nil {
				t.Fatal(err)
			}
			ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
			if len(ack.Body) < 2 || ack.Body[0] != 0 || ack.Body[1] != tc.code {
				t.Fatalf("guard ACK=%x want code %d", ack.Body, tc.code)
			}
			inventory, _, _ := r.Inventory.Load(ctx, "10")
			character, _, _ := r.Character.Load(ctx, "10")
			if !reflect.DeepEqual(inventory.Slots, before.Slots) || character.Stats["gold"] != gold {
				t.Fatal("rejected request changed inventory or gold")
			}
			owner, err := dnfauction.NewOwner(r)
			if err != nil {
				t.Fatal(err)
			}
			listings, err := owner.Search(ctx, nil)
			if err != nil || len(listings) != 0 {
				t.Fatalf("rejected request left listing: %+v %v", listings, err)
			}
		})
	}
}
