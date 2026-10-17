package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	onlineTradeCancelEmpty    = []byte{0, 0}
	onlineTradeCancelOffer358 = []byte{1, 0, 3, 0, 0, 0x66, 1}
	onlineTradeCancelGold     = []byte{1, 0, 0, 0, 0, 0, 0}
)

func TestOnlineTradeOfferWriteFailureCancelsWholeTrade(t *testing.T) {
	for _, test := range []struct {
		name             string
		remove           bool
		fromPeer         bool
		failMsgID        uint16
		failClass        byte
		failPeer         bool
		wantSenderCancel []byte
		wantPeerCancel   []byte
	}{
		{name: "add_offerer_ack", failMsgID: currentItemTradeMoveMsg, failClass: 1, wantSenderCancel: onlineTradeCancelEmpty, wantPeerCancel: onlineTradeCancelEmpty},
		{name: "add_peer_ack", fromPeer: true, failMsgID: currentItemTradeMoveMsg, failClass: 1, wantSenderCancel: onlineTradeCancelEmpty, wantPeerCancel: onlineTradeCancelEmpty},
		{name: "add_offerer_peer_change", failMsgID: currentItemTradeChangeMsg, failPeer: true, wantSenderCancel: onlineTradeCancelOffer358, wantPeerCancel: onlineTradeCancelEmpty},
		{name: "add_peer_peer_change", fromPeer: true, failMsgID: currentItemTradeChangeMsg, failPeer: true, wantSenderCancel: onlineTradeCancelOffer358, wantPeerCancel: onlineTradeCancelEmpty},
		{name: "remove_offerer_ack", remove: true, failMsgID: currentItemTradeMoveMsg, failClass: 1, wantSenderCancel: onlineTradeCancelOffer358, wantPeerCancel: onlineTradeCancelEmpty},
		{name: "remove_peer_ack", remove: true, fromPeer: true, failMsgID: currentItemTradeMoveMsg, failClass: 1, wantSenderCancel: onlineTradeCancelOffer358, wantPeerCancel: onlineTradeCancelEmpty},
		{name: "remove_offerer_peer_change", remove: true, failMsgID: currentItemTradeChangeMsg, failPeer: true, wantSenderCancel: onlineTradeCancelEmpty, wantPeerCancel: onlineTradeCancelEmpty},
		{name: "remove_peer_peer_change", remove: true, fromPeer: true, failMsgID: currentItemTradeChangeMsg, failPeer: true, wantSenderCancel: onlineTradeCancelEmpty, wantPeerCancel: onlineTradeCancelEmpty},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newOnlineCrystalTradeFixture(t, nil)
			saveOnlineTradeAccountSlots(t, f, onlineCrystalTradePeerAccount, map[string]dnfrepo.ItemStack{
				dnfrepo.AccountSharedInventorySlotKey(onlineCrystalTradeSlot): {
					ItemID: onlineCrystalTradeItemID, Count: onlineCrystalTradeCount,
				},
			})
			if test.remove {
				addOnlineTradeTestCrystal(t, f, test.fromPeer)
			}
			f.offerConn.write.Reset()
			f.peerConn.write.Reset()

			sender, peer := f.offerer, f.peer
			senderConn, peerConn := f.offerConn, f.peerConn
			if test.fromPeer {
				sender, peer = peer, sender
				senderConn, peerConn = peerConn, senderConn
			}
			injected := errors.New("injected trade offer projection write failure")
			targetSession, targetConn := sender, senderConn
			if test.failPeer {
				targetSession, targetConn = peer, peerConn
			}
			targetSession.conn = &onlineItemTradeStageFailingConn{
				bufferConn: targetConn, msgID: test.failMsgID, classification: test.failClass, err: injected,
			}

			request := buildOnlineItemTradeMoveRequest(
				dnfrepo.MainInventoryListType, onlineCrystalTradeSlot, 0, onlineCrystalTradeMovedCount,
				currentItemTradeListType, 0,
			)
			if test.remove {
				request = buildOnlineItemTradeMoveRequest(
					currentItemTradeListType, int16(currentItemTradeOfferFirst), 0, onlineCrystalTradeMovedCount,
					dnfrepo.MainInventoryListType, onlineCrystalTradeSlot,
				)
			}
			handled, err := f.service.handleAlignedGameCommand(sender, byte(dnfenum.GameCmdCommand), currentItemTradeMoveMsg, request)
			if !handled || !errors.Is(err, injected) {
				t.Fatalf("handled=%t err=%v, want injected projection failure", handled, err)
			}
			assertOnlineTradeCancelledAndCannotCommit(t, f, sender, peer, test.wantSenderCancel, test.wantPeerCancel)
			assertOnlineTradeAccountBalances(t, f)
		})
	}
}

func TestOnlineTradeGoldWriteFailureCancelsWholeTrade(t *testing.T) {
	for _, test := range []struct {
		name             string
		remove           bool
		fromPeer         bool
		failMsgID        uint16
		failClass        byte
		failPeer         bool
		wantSenderCancel []byte
		wantPeerCancel   []byte
	}{
		{name: "add_offerer_ack", failMsgID: currentItemTradeMoveMsg, failClass: 1, wantSenderCancel: onlineTradeCancelEmpty, wantPeerCancel: onlineTradeCancelEmpty},
		{name: "add_peer_ack", fromPeer: true, failMsgID: currentItemTradeMoveMsg, failClass: 1, wantSenderCancel: onlineTradeCancelEmpty, wantPeerCancel: onlineTradeCancelEmpty},
		{name: "add_offerer_peer_change", failMsgID: currentItemTradeChangeMsg, failPeer: true, wantSenderCancel: onlineTradeCancelGold, wantPeerCancel: onlineTradeCancelEmpty},
		{name: "add_peer_peer_change", fromPeer: true, failMsgID: currentItemTradeChangeMsg, failPeer: true, wantSenderCancel: onlineTradeCancelGold, wantPeerCancel: onlineTradeCancelEmpty},
		{name: "remove_offerer_ack", remove: true, failMsgID: currentItemTradeMoveMsg, failClass: 1, wantSenderCancel: onlineTradeCancelGold, wantPeerCancel: onlineTradeCancelEmpty},
		{name: "remove_peer_ack", remove: true, fromPeer: true, failMsgID: currentItemTradeMoveMsg, failClass: 1, wantSenderCancel: onlineTradeCancelGold, wantPeerCancel: onlineTradeCancelEmpty},
		{name: "remove_offerer_peer_change", remove: true, failMsgID: currentItemTradeChangeMsg, failPeer: true, wantSenderCancel: onlineTradeCancelEmpty, wantPeerCancel: onlineTradeCancelEmpty},
		{name: "remove_peer_peer_change", remove: true, fromPeer: true, failMsgID: currentItemTradeChangeMsg, failPeer: true, wantSenderCancel: onlineTradeCancelEmpty, wantPeerCancel: onlineTradeCancelEmpty},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newOnlineCrystalTradeFixture(t, nil)
			setOnlineTradeGold(t, f, 1_000_000, 1_000_000)
			if test.remove {
				addOnlineTradeTestGold(t, f, test.fromPeer, 100_000)
			}
			f.offerConn.write.Reset()
			f.peerConn.write.Reset()

			sender, peer := f.offerer, f.peer
			senderConn, peerConn := f.offerConn, f.peerConn
			if test.fromPeer {
				sender, peer = peer, sender
				senderConn, peerConn = peerConn, senderConn
			}
			injected := errors.New("injected trade gold projection write failure")
			targetSession, targetConn := sender, senderConn
			if test.failPeer {
				targetSession, targetConn = peer, peerConn
			}
			targetSession.conn = &onlineItemTradeStageFailingConn{
				bufferConn: targetConn, msgID: test.failMsgID, classification: test.failClass, err: injected,
			}

			request := buildOnlineItemTradeMoveRequest(dnfrepo.MainInventoryListType, 0, 0, 100_000, currentItemTradeListType, 0)
			if test.remove {
				request = buildOnlineItemTradeMoveRequest(currentItemTradeListType, 0, 0, 0, dnfrepo.MainInventoryListType, 0)
			}
			handled, err := f.service.handleAlignedGameCommand(sender, byte(dnfenum.GameCmdCommand), currentItemTradeMoveMsg, request)
			if !handled || !errors.Is(err, injected) {
				t.Fatalf("handled=%t err=%v, want injected projection failure", handled, err)
			}
			assertOnlineTradeCancelledAndCannotCommit(t, f, sender, peer, test.wantSenderCancel, test.wantPeerCancel)
			assertOnlineTradeGoldUnchanged(t, f, 1_000_000, 1_000_000)
		})
	}
}

// Only the newly unacknowledged row is absent locally. Earlier ACKs must
// continue to restore their assigned rows, including a previously set wallet.
func TestOnlineTradeAddAckFailurePreservesAcknowledgedCancellationRows(t *testing.T) {
	for _, gold := range []bool{false, true} {
		t.Run(map[bool]string{false: "item", true: "gold"}[gold], func(t *testing.T) {
			f := newOnlineCrystalTradeFixture(t, nil)
			addOnlineTradeTestCrystal(t, f, false)
			setOnlineTradeGold(t, f, 1_000_000, 1_000_000)
			if gold {
				addOnlineTradeTestGold(t, f, false, 100_000)
			} else {
				prepareOnlineTradeSecondCrystal(t, f)
			}
			f.offerConn.write.Reset()
			f.peerConn.write.Reset()
			injected := errors.New("new offer ACK failed after earlier acknowledged offer")
			f.offerer.conn = &onlineItemTradeStageFailingConn{bufferConn: f.offerConn, msgID: currentItemTradeMoveMsg, classification: 1, err: injected}
			request := inventory.MoveItemspaceRequest{SourceListType: 0, SourceSlotIndex: 354, MoveCount: 100, DestinationListType: 4}
			if gold {
				request.SourceSlotIndex = 0
				request.MoveCount = 200_000
			}
			if err := f.service.handleOnlineItemTradeMove(f.offerer, request); !errors.Is(err, injected) {
				t.Fatalf("err=%v", err)
			}
			want := onlineTradeCancelOffer358
			if gold {
				want = []byte{2, 0, 0, 0, 0, 0, 0, 3, 0, 0, 0x66, 1}
			}
			assertOnlineTradeCancelledAndCannotCommit(t, f, f.offerer, f.peer, want, onlineTradeCancelEmpty)
			assertOnlineTradeGoldUnchanged(t, f, 1_000_000, 1_000_000)
		})
	}
}

func saveOnlineTradeAccountSlots(t *testing.T, f *onlineCrystalTradeFixture, accountID string, slots map[string]dnfrepo.ItemStack) {
	t.Helper()
	if err := f.repositories.AccountInventory.Save(context.Background(), dnfrepo.AccountInventoryRecord{
		AccountID: accountID,
		Slots:     slots,
	}); err != nil {
		t.Fatal(err)
	}
}

func setOnlineTradeGold(t *testing.T, f *onlineCrystalTradeFixture, first, second int64) {
	t.Helper()
	for _, record := range []dnfrepo.CharacterRecord{
		{CharacterID: "1", AccountID: onlineCrystalTradeAccount, Stats: map[string]int64{"gold": first}},
		{CharacterID: "5", AccountID: onlineCrystalTradePeerAccount, Stats: map[string]int64{"gold": second}},
	} {
		if err := f.repositories.Character.Save(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
}

func addOnlineTradeTestCrystal(t *testing.T, f *onlineCrystalTradeFixture, fromPeer bool) {
	t.Helper()
	sender := f.offerer
	if fromPeer {
		sender = f.peer
	}
	request := buildOnlineItemTradeMoveRequest(
		dnfrepo.MainInventoryListType, onlineCrystalTradeSlot, 0, onlineCrystalTradeMovedCount,
		currentItemTradeListType, 0,
	)
	handled, err := f.service.handleAlignedGameCommand(sender, byte(dnfenum.GameCmdCommand), currentItemTradeMoveMsg, request)
	if !handled || err != nil {
		t.Fatalf("prepare crystal offer handled=%t err=%v", handled, err)
	}
}

func addOnlineTradeTestGold(t *testing.T, f *onlineCrystalTradeFixture, fromPeer bool, count int32) {
	t.Helper()
	sender := f.offerer
	if fromPeer {
		sender = f.peer
	}
	request := buildOnlineItemTradeMoveRequest(dnfrepo.MainInventoryListType, 0, 0, count, currentItemTradeListType, 0)
	handled, err := f.service.handleAlignedGameCommand(sender, byte(dnfenum.GameCmdCommand), currentItemTradeMoveMsg, request)
	if !handled || err != nil {
		t.Fatalf("prepare gold offer handled=%t err=%v", handled, err)
	}
}

func assertOnlineTradeCancelledAndCannotCommit(t *testing.T, f *onlineCrystalTradeFixture, first, second *gameSession, wantSenderCancel, wantPeerCancel []byte) {
	t.Helper()
	if f.service.currentOnlineItemTrade(first.selectedCharacterID) != nil ||
		f.service.currentOnlineItemTrade(second.selectedCharacterID) != nil {
		t.Fatal("projection failure left the trade registered")
	}
	senderConn, peerConn := f.offerConn, f.peerConn
	if first == f.peer {
		senderConn, peerConn = f.peerConn, f.offerConn
	}
	assertOnlineTradeCancelBody(t, senderConn.write.Bytes(), wantSenderCancel)
	assertOnlineTradeCancelBody(t, peerConn.write.Bytes(), wantPeerCancel)
	for index, conn := range []*bufferConn{senderConn, peerConn} {
		if count := countTradePackets(t, conn.write.Bytes(), 0, currentItemTradeFinishMsg); count != 0 {
			t.Fatalf("participant %d received completion after projection failure", index)
		}
		conn.write.Reset()
	}
	for _, state := range []byte{currentItemTradeAddRequest, currentItemTradeReady} {
		for _, session := range []*gameSession{first, second} {
			if err := f.service.handleOnlineItemTradeState(session, state); err != nil {
				t.Fatalf("post-cancel state %d char=%d err=%v", state, session.selectedCharacterID, err)
			}
		}
	}
	if f.service.currentOnlineItemTrade(first.selectedCharacterID) != nil ||
		f.service.currentOnlineItemTrade(second.selectedCharacterID) != nil {
		t.Fatal("post-cancel state recreated the trade")
	}
	for index, conn := range []*bufferConn{f.offerConn, f.peerConn} {
		if count := countTradePackets(t, conn.write.Bytes(), 0, currentItemTradeFinishMsg); count != 0 {
			t.Fatalf("participant %d committed after cancellation", index)
		}
	}
}

func assertOnlineTradeCancelBody(t *testing.T, data, want []byte) {
	t.Helper()
	found := false
	for len(data) > 0 {
		packet, rest := splitGameServerUpperPacket(t, data)
		if packet.Header.Classification == 0 && packet.Header.MsgID == currentItemTradeCancelMsg {
			if found {
				t.Fatal("multiple cancellation packets for one participant")
			}
			found = true
			if !bytes.Equal(packet.Body, want) {
				t.Fatalf("cancel body=% X, want % X", packet.Body, want)
			}
		}
		data = rest
	}
	if !found {
		t.Fatal("cancellation packet not sent")
	}
}

func countTradePackets(t *testing.T, data []byte, classification byte, msgID uint16) int {
	t.Helper()
	count := 0
	for len(data) > 0 {
		packet, rest := splitGameServerUpperPacket(t, data)
		if packet.Header.Classification == classification && packet.Header.MsgID == msgID {
			count++
		}
		data = rest
	}
	return count
}

func assertOnlineTradeAccountBalances(t *testing.T, f *onlineCrystalTradeFixture) {
	t.Helper()
	for _, accountID := range []string{onlineCrystalTradeAccount, onlineCrystalTradePeerAccount} {
		account, found, err := f.repositories.AccountInventory.Load(context.Background(), accountID)
		if err != nil || !found {
			t.Fatalf("account %s load found=%t err=%v", accountID, found, err)
		}
		stack := account.Slots[dnfrepo.AccountSharedInventorySlotKey(onlineCrystalTradeSlot)]
		if stack.ItemID != onlineCrystalTradeItemID || stack.Count != onlineCrystalTradeCount {
			t.Fatalf("account %s changed after failed projection: %+v", accountID, stack)
		}
	}
}

func assertOnlineTradeGoldUnchanged(t *testing.T, f *onlineCrystalTradeFixture, first, second int64) {
	t.Helper()
	for characterID, want := range map[string]int64{"1": first, "5": second} {
		character, found, err := f.repositories.Character.Load(context.Background(), characterID)
		if err != nil || !found {
			t.Fatalf("character %s load found=%t err=%v", characterID, found, err)
		}
		if got := character.Stats["gold"]; got != want {
			t.Fatalf("character %s gold=%d, want %d", characterID, got, want)
		}
	}
}

func prepareOnlineTradeSecondCrystal(t *testing.T, f *onlineCrystalTradeFixture) {
	t.Helper()
	account, _, err := f.repositories.AccountInventory.Load(context.Background(), onlineCrystalTradeAccount)
	if err != nil {
		t.Fatal(err)
	}
	account.Slots[dnfrepo.AccountSharedInventorySlotKey(354)] = dnfrepo.ItemStack{ItemID: 3033, Count: 5000}
	if err := f.repositories.AccountInventory.Save(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(bridgePVFSource{
		"monster/monster.lst": "", "equipment/equipment.lst": "",
		"stackable/stackable.lst": "3037 `a.stk`\n3033 `b.stk`\n",
		"stackable/a.stk":         "[stackable type] `[material]`\n[attach type] `[free]`\n",
		"stackable/b.stk":         "[stackable type] `[material]`\n[attach type] `[free]`\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.service.pvfItemCatalog = catalog
}
