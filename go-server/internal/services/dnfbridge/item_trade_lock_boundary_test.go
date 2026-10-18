package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestOnlineTradeLockedOfferCannotChangeBeforeFinalConsent(t *testing.T) {
	for _, operation := range []string{"add_item", "remove_item", "add_gold", "remove_gold"} {
		t.Run(operation, func(t *testing.T) {
			f := newOnlineCrystalTradeFixture(t, nil)
			addOnlineTradeTestCrystal(t, f, false)
			if operation == "add_item" {
				prepareOnlineTradeSecondCrystal(t, f)
			}
			setOnlineTradeGold(t, f, 1_000_000, 1_000_000)
			addOnlineTradeTestGold(t, f, false, 100_000)
			if err := f.service.handleOnlineItemTradeState(f.offerer, currentItemTradeAddRequest); err != nil {
				t.Fatal(err)
			}
			trade := f.service.currentOnlineItemTrade(1)
			beforeOffers := snapshotOnlineItemTradeOffers(trade, 1)
			beforeGold := trade.gold[1]
			f.offerConn.write.Reset()
			f.peerConn.write.Reset()
			request := inventory.MoveItemspaceRequest{SourceListType: 0, SourceSlotIndex: onlineCrystalTradeSlot, MoveCount: 100, DestinationListType: 4}
			switch operation {
			case "add_item":
				request.SourceSlotIndex = 354
			case "remove_item":
				request.SourceListType = 4
				request.SourceSlotIndex = 3
				request.DestinationListType = 0
				request.DestinationSlotIndex = onlineCrystalTradeSlot
			case "add_gold":
				request.SourceSlotIndex = 0
				request.MoveCount = 200_000
			case "remove_gold":
				request.SourceListType = 4
				request.SourceSlotIndex = 0
				request.DestinationListType = 0
			}
			if err := f.service.handleOnlineItemTradeMove(f.offerer, request); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeOffers, snapshotOnlineItemTradeOffers(trade, 1)) || trade.gold[1] != beforeGold {
				t.Fatal("locked offer or gold changed")
			}
			packet, rest := splitGameServerUpperPacket(t, f.offerConn.write.Bytes())
			if packet.Header.Classification != 1 || packet.Header.MsgID != 19 || !bytes.Equal(packet.Body, []byte{0, 1}) || len(rest) != 0 || f.peerConn.write.Len() != 0 {
				t.Fatalf("locked move was not rejected: body=%x", packet.Body)
			}
		})
	}
}

func TestOnlineTradeRepeatedLockCannotActAsFinalConsent(t *testing.T) {
	f := newOnlineCrystalTradeFixture(t, nil)
	addOnlineTradeTestCrystal(t, f, false)
	for _, s := range []*gameSession{f.offerer, f.peer} {
		if err := f.service.handleOnlineItemTradeState(s, currentItemTradeAddRequest); err != nil {
			t.Fatal(err)
		}
	}
	f.offerConn.write.Reset()
	f.peerConn.write.Reset()
	for _, s := range []*gameSession{f.offerer, f.peer} {
		if err := f.service.handleOnlineItemTradeState(s, currentItemTradeAddRequest); err != nil {
			t.Fatal(err)
		}
	}
	trade := f.service.currentOnlineItemTrade(1)
	if trade == nil || trade.states[1] != 1 || trade.states[5] != 1 || trade.committing {
		t.Fatal("repeated lock crossed final consent boundary")
	}
	assertOnlineTradeAccountUnchangedAfterLockedReplay(t, f)
	for _, s := range []*gameSession{f.offerer, f.peer} {
		if err := f.service.handleOnlineItemTradeState(s, currentItemTradeReady); err != nil {
			t.Fatal(err)
		}
	}
	if f.service.currentOnlineItemTrade(1) != nil {
		t.Fatal("explicit final consent did not complete")
	}
}

func TestOnlineTradeUnlockCannotInvalidateOtherFinalConsent(t *testing.T) {
	f := newOnlineCrystalTradeFixture(t, nil)
	addOnlineTradeTestCrystal(t, f, false)
	for _, s := range []*gameSession{f.offerer, f.peer} {
		if err := f.service.handleOnlineItemTradeState(s, currentItemTradeAddRequest); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.service.handleOnlineItemTradeState(f.peer, currentItemTradeReady); err != nil {
		t.Fatal(err)
	}
	if err := f.service.handleOnlineItemTradeState(f.offerer, 0); err != nil {
		t.Fatal(err)
	}
	trade := f.service.currentOnlineItemTrade(1)
	if trade == nil || trade.states[1] != 1 || trade.states[5] != 3 {
		t.Fatal("unlock invalidated already recorded peer consent")
	}
}

func assertOnlineTradeAccountUnchangedAfterLockedReplay(t *testing.T, f *onlineCrystalTradeFixture) {
	t.Helper()
	r, _, err := f.repositories.AccountInventory.Load(context.Background(), onlineCrystalTradeAccount)
	if err != nil || r.Slots[dnfrepo.AccountSharedInventorySlotKey(onlineCrystalTradeSlot)].Count != onlineCrystalTradeCount {
		t.Fatal("lock replay moved assets")
	}
}
