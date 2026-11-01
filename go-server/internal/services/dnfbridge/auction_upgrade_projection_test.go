package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"testing"

	dnfauction "longheng.io/server/internal/modules/dnf/auction"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestCurrentAuctionUpgradeMetadataAcrossListsAndCancellation(t *testing.T) {
	for _, cached := range []bool{false, true} {
		for _, amplify := range []byte{0, 1, 2, 3, 4} {
			ctx := context.Background()
			r := memory.NewMemoryGroup()
			item := dnfrepo.ItemStack{ItemID: 6001, Count: 1, Extra: map[string]string{
				"item_kind": "equipment", "ext_data0": "74", "packed_flag_byte": "74", "reinforce": "10", "upgrade_level": "10",
				"quality_seed": "998877", "durability": "45", "byte_13": string('0' + amplify), "marker_16": "123", "seal_flag": "1",
			}}
			if cached {
				item.RawEntry = make([]byte, 119)
				binary.LittleEndian.PutUint32(item.RawEntry[2:], 6001)
			}
			original := dnfrepo.CloneInventory(dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{"0:9": item}}).Slots["0:9"]
			seedCurrentAuctionBridgeCharacter(t, ctx, r, "10", "seller-account", "Seller", 50000, map[string]dnfrepo.ItemStack{"0:9": item})
			owner, err := dnfauction.NewOwner(r)
			if err != nil {
				t.Fatal(err)
			}
			registered, err := owner.Register(ctx, dnfauction.RegisterCommand{ListingID: 991, SellerID: "10", AccountID: "seller-account", SellerName: "Seller", ListType: 0, SourceSlot: 9, ExpectedID: 6001, Count: 1, UnitPrice: 1000, StartPrice: -1, Duration: 3600000000000, ListingLimit: 5})
			if err != nil {
				t.Fatal(err)
			}
			listing := registered.Listing
			listing.BidderID = "20"
			listing.BidTotal = 1000
			eligible := func(uint32) bool { return true }
			rows := [][]byte{
				buildCurrentAuctionSearchRow(listing)[0x2b:0x7e],
				buildCurrentAuctionOwnedBody([]dnfauction.Listing{listing}, "10", eligible)[2+0x1e : 2+0x71],
			}
			for _, row := range rows {
				if row[5] != 74 || row[0x10] != amplify || binary.LittleEndian.Uint16(row[0x11:]) != 123 {
					t.Fatalf("cached=%t amplify=%d auction state packed=%d type=%d value=%d", cached, amplify, row[5], row[0x10], binary.LittleEndian.Uint16(row[0x11:]))
				}
			}
			cancelled, err := owner.Cancel(ctx, dnfauction.CancelCommand{SellerID: "10", AccountID: "seller-account", ListingID: 991})
			if err != nil {
				t.Fatal(err)
			}
			service := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return r, true }}
			if err := service.deliverCurrentAuctionItemMailLocked(ctx, owner, cancelled.Delivery, currentAuctionReturnMail); err != nil {
				t.Fatal(err)
			}
			mailbox, _, err := r.Mailbox.Load(ctx, "10")
			if err != nil || len(mailbox.Mails) != 1 {
				t.Fatal(mailbox, err)
			}
			for _, m := range mailbox.Mails {
				a := m.Attachments[0]
				if !reflect.DeepEqual(a.Extra, original.Extra) || !bytes.Equal(a.RawEntry, original.RawEntry) {
					t.Fatal("cancel changed equipment attributes")
				}
			}
			if !reflect.DeepEqual(item, original) {
				t.Fatal("projection mutated source item")
			}
		}
	}
}

func TestRealPVFAuctionUpgradedEquipmentMetadata(t *testing.T) {
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
	def, err := catalog.ResolveItem(101000069)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("equipment=%d name=%s", def.ItemID, def.Name)
	stack := dnfrepo.ItemStack{ItemID: 101000069, Count: 1, Extra: map[string]string{"item_kind": "equipment", "ext_data0": "10", "reinforce": "10", "upgrade_level": "10", "seal_flag": "1", "quality_seed": "999999998", "durability": "45"}}
	if _, _, ok := currentAuctionRegistrationPolicyForDefinition(def, stack); !ok {
		t.Fatal("sealed equipment rejected")
	}
	if row := buildCurrentAuctionItem83(stack); row[5] != 10 {
		t.Fatalf("upgrade=%d", row[5])
	}
}
