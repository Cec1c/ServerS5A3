package dnfbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	inventory "longheng.io/server/internal/modules/dnf/inventory"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Exact 28B C2S body from character47/test99, 2026-10-17T07:13:44.302103427Z.
// Credentials/transport header are excluded. The account/PVF stores below are
// explicit existing fixtures; this is not a copy or replay against player DB.
func currentCrystalWireSceneBody(t *testing.T) []byte {
	t.Helper()
	b, err := hex.DecodeString("246201d90b0000d50700000400000000000000000000ffffffff0000")
	if err != nil {
		t.Fatal(err)
	}
	r, err := inventory.DecodeMoveItemspaceRequest(b)
	if err != nil || r.SourceListType != 36 || r.SourceSlotIndex != 354 || r.SourceInstanceValue != 3033 || r.MoveCount != 2005 || r.DestinationListType != 4 || r.DestinationSlotIndex != 0 {
		t.Fatalf("scene decoded=%+v err=%v", r, err)
	}
	return b
}

func crystalWireOffer(t *testing.T, f *onlineCrystalTradeFixture, body []byte) {
	t.Helper()
	handled, err := f.service.handleAlignedGameCommand(f.offerer, byte(dnfenum.GameCmdCommand), currentItemTradeMoveMsg, body)
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	ack, rest := splitGameServerUpperPacket(t, f.offerConn.write.Bytes())
	if len(rest) != 0 || ack.Header.Classification != 1 || ack.Header.MsgID != 19 || len(ack.Body) != 11 || ack.Body[0] != 1 || ack.Body[1] != 36 || ack.Body[8] != 4 || ack.Body[9] != 3 || ack.Body[10] != 0 {
		t.Fatalf("virtual crystal success ACK=%x trailing=%d", ack.Body, len(rest))
	}
	trade := f.service.currentOnlineItemTrade(f.offerer.selectedCharacterID)
	offer, ok := trade.offers[f.offerer.selectedCharacterID][3]
	if !ok || offer.sourceList != 0 || offer.sourceSlot != onlineCrystalTradeSlot {
		t.Fatalf("stored offer=%+v present=%v", offer, ok)
	}
}

func TestOnlineCrystalNative36OffersCancelsAndCommits(t *testing.T) {
	// The original literal proves the native source list and quantity. Use the
	// existing 3037/358 fixture for the asset flow, with only those literal fields
	// changed; retain list36 and the server-assigned visible destination row.
	scene := currentCrystalWireSceneBody(t)
	for _, action := range []string{"cancel", "withdraw", "commit"} {
		t.Run(action, func(t *testing.T) {
			f := newOnlineCrystalTradeFixture(t, nil)
			before, _, _ := f.repositories.AccountInventory.Load(context.Background(), onlineCrystalTradeAccount)
			body := buildOnlineItemTradeMoveRequest(scene[0], onlineCrystalTradeSlot, onlineCrystalTradeItemID, 1000, 4, 0)
			crystalWireOffer(t, f, body)
			pending, _, _ := f.repositories.AccountInventory.Load(context.Background(), onlineCrystalTradeAccount)
			if !reflect.DeepEqual(before, pending) {
				t.Fatal("pending offer debited assets")
			}
			f.offerConn.write.Reset()
			f.peerConn.write.Reset()
			switch action {
			case "cancel":
				f.service.cancelOnlineItemTrade(f.service.currentOnlineItemTrade(1), "native36_cancel")
				ack, rest := splitGameServerUpperPacket(t, f.offerConn.write.Bytes())
				if len(rest) != 0 || ack.Header.MsgID != 16 || !bytes.Equal(ack.Body, []byte{1, 0, 3, 0, 36, 0x66, 1}) {
					t.Fatalf("cancel lost original client container: %x", ack.Body)
				}
			case "withdraw":
				withdrawal := buildOnlineItemTradeMoveRequest(4, 3, 0, 1000, 36, onlineCrystalTradeSlot)
				_, err := f.service.handleAlignedGameCommand(f.offerer, byte(dnfenum.GameCmdCommand), 19, withdrawal)
				if err != nil {
					t.Fatal(err)
				}
				ack, rest := splitGameServerUpperPacket(t, f.offerConn.write.Bytes())
				if len(rest) != 0 || len(ack.Body) != 11 || ack.Body[0] != 1 || ack.Body[8] != 36 {
					t.Fatalf("withdraw ACK=%x", ack.Body)
				}
				if len(f.service.currentOnlineItemTrade(1).offers[1]) != 0 {
					t.Fatal("withdraw retained offer")
				}
			case "commit":
				for _, state := range []byte{currentItemTradeAddRequest, currentItemTradeReady} {
					for _, session := range []*gameSession{f.offerer, f.peer} {
						handled, err := f.service.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), 24, []byte{state})
						if !handled || err != nil {
							t.Fatalf("ready=%v/%v", handled, err)
						}
					}
				}
				for account, want := range map[string]int64{"a": 4000, "b": 1000} {
					r, _, _ := f.repositories.AccountInventory.Load(context.Background(), account)
					if r.Slots["0:358"].Count != want || len(r.Slots) != 1 {
						t.Fatalf("warehouse %s=%+v", account, r.Slots)
					}
				}
				for _, id := range []string{"1", "5"} {
					bag, _, _ := f.repositories.Inventory.Load(context.Background(), id)
					if len(bag.Slots) != 0 {
						t.Fatalf("character duplicate: %s %+v", id, bag.Slots)
					}
				}
				if f.service.currentOnlineItemTrade(1) != nil {
					t.Fatal("committed trade retained state")
				}
			}
			if action != "commit" {
				after, _, _ := f.repositories.AccountInventory.Load(context.Background(), onlineCrystalTradeAccount)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("withdraw/cancel changed assets")
				}
			}
		})
	}
}

func TestOnlineCrystalNative36RejectThenRetryAndAliasDuplicate(t *testing.T) {
	f := newOnlineCrystalTradeFixture(t, nil)
	// Unsupported soul/native lists must not create offers or poison retry.
	for _, slot := range []int16{0, 353, 360, 365, 366} {
		f.offerConn.write.Reset()
		f.peerConn.write.Reset()
		body := buildOnlineItemTradeMoveRequest(36, slot, 0, 1000, 4, 0)
		_, err := f.service.handleAlignedGameCommand(f.offerer, byte(dnfenum.GameCmdCommand), 19, body)
		if err != nil {
			t.Fatal(err)
		}
		ack, _ := splitGameServerUpperPacket(t, f.offerConn.write.Bytes())
		if !bytes.Equal(ack.Body, []byte{0, 1}) || f.peerConn.write.Len() != 0 || len(f.service.currentOnlineItemTrade(1).offers[1]) != 0 {
			t.Fatal("invalid native slot changed trade")
		}
	}
	f.offerConn.write.Reset()
	body := buildOnlineItemTradeMoveRequest(36, onlineCrystalTradeSlot, 0, 1000, 4, 0)
	crystalWireOffer(t, f, body)
	f.offerConn.write.Reset()
	// Canonical list0 and virtual list36 address the same physical source.
	duplicate := buildOnlineItemTradeMoveRequest(0, onlineCrystalTradeSlot, 0, 1000, 4, 0)
	_, err := f.service.handleAlignedGameCommand(f.offerer, byte(dnfenum.GameCmdCommand), 19, duplicate)
	if err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, f.offerConn.write.Bytes())
	if !bytes.Equal(ack.Body, []byte{0, 1}) || len(f.service.currentOnlineItemTrade(1).offers[1]) != 1 {
		t.Fatal("alias duplicated source")
	}
	// A rejected wrong destination must not silently remove the valid offer.
	f.offerConn.write.Reset()
	bad := buildOnlineItemTradeMoveRequest(4, 3, 0, 1000, 36, 360)
	_, err = f.service.handleAlignedGameCommand(f.offerer, byte(dnfenum.GameCmdCommand), 19, bad)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.service.currentOnlineItemTrade(1).offers[1]) != 1 {
		t.Fatal("invalid withdrawal erased valid offer")
	}
}

func TestOnlineCrystalNative36RealPVFSixItemsAndSceneQuantity(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current pinned PVF")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	switch fmt.Sprintf("%x", sha256.Sum256(data)) {
	case "a56cacf96087ba34ca5299e04ab65f8268f694b214b97dec9879fdebf46f5290",
		"94a2549598816198115340f84c0be2d31603ddeea58f70f27afe6f73b25e3df7":
	default:
		t.Fatal("current PVF hash mismatch")
	}
	archive, err := platformpvf.OpenBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	assertCurrentCrystalTradePVFItems(t, catalog)
	for index, item := range []int64{3033, 3034, 3035, 3036, 3037, 3262} {
		t.Run(strconv.FormatInt(item, 10), func(t *testing.T) {
			slot := int16(354 + index)
			count := int32(1000)
			f := newOnlineCrystalTradeFixture(t, nil)
			f.service.pvfItemCatalog = catalog
			saveOnlineCrystalTradeAccountSlots(t, f.repositories, map[string]repo.ItemStack{repo.AccountSharedInventorySlotKey(slot): {ItemID: item, Count: 5000}})
			body := buildOnlineItemTradeMoveRequest(36, slot, int32(item), count, 4, 0)
			if index == 0 {
				body = currentCrystalWireSceneBody(t)
				count = 2005
			}
			handled, err := f.service.handleAlignedGameCommand(f.offerer, byte(dnfenum.GameCmdCommand), 19, body)
			if !handled || err != nil {
				t.Fatalf("offer=%v/%v", handled, err)
			}
			ack, rest := splitGameServerUpperPacket(t, f.offerConn.write.Bytes())
			if len(rest) != 0 || len(ack.Body) != 11 || ack.Body[0] != 1 || ack.Body[1] != 36 {
				t.Fatalf("native36 realPVF ACK=%x", ack.Body)
			}
			for _, state := range []byte{currentItemTradeAddRequest, currentItemTradeReady} {
				for _, session := range []*gameSession{f.offerer, f.peer} {
					handled, err = f.service.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), 24, []byte{state})
					if !handled || err != nil {
						t.Fatalf("ready=%v/%v", handled, err)
					}
				}
			}
			for account, want := range map[string]int64{"a": 5000 - int64(count), "b": int64(count)} {
				r, found, err := f.repositories.AccountInventory.Load(context.Background(), account)
				if err != nil || !found || len(r.Slots) != 1 || r.Slots[repo.AccountSharedInventorySlotKey(slot)].ItemID != item || r.Slots[repo.AccountSharedInventorySlotKey(slot)].Count != want {
					t.Fatalf("realPVF %d %s=%+v/%v/%v want%d", item, account, r, found, err, want)
				}
			}
		})
	}
}

func TestOnlineCrystalNative36CannotRedirectOtherSource(t *testing.T) {
	for _, sourceSlot := range []int16{129, onlineCrystalTradeSlot} {
		t.Run(fmt.Sprint(sourceSlot), func(t *testing.T) {
			f := newOnlineCrystalTradeFixture(t, nil)
			if sourceSlot == 129 {
				err := f.repositories.Inventory.Save(context.Background(), repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{"0:129": {ItemID: onlineCrystalTradeItemID, Count: 5000}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			body := buildOnlineItemTradeMoveRequest(0, sourceSlot, 0, 1000, 4, 0)
			_, err := f.service.handleAlignedGameCommand(f.offerer, byte(dnfenum.GameCmdCommand), 19, body)
			if err != nil || len(f.service.currentOnlineItemTrade(1).offers[1]) != 1 {
				t.Fatalf("ordinary/canonical offer failed: %v", err)
			}
			destination := int16(354)
			f.offerConn.write.Reset()
			f.peerConn.write.Reset()
			wrong := buildOnlineItemTradeMoveRequest(4, 3, 0, 1000, 36, destination)
			_, err = f.service.handleAlignedGameCommand(f.offerer, byte(dnfenum.GameCmdCommand), 19, wrong)
			if err != nil {
				t.Fatal(err)
			}
			ack, _ := splitGameServerUpperPacket(t, f.offerConn.write.Bytes())
			if !bytes.Equal(ack.Body, []byte{0, 1}) || f.peerConn.write.Len() != 0 || len(f.service.currentOnlineItemTrade(1).offers[1]) != 1 {
				t.Fatal("native alias redirected an unrelated source or erased it on rejection")
			}
		})
	}
}
