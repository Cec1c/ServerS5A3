package dnfbridge

import (
	"bytes"
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	onlineTradeAbortGoldBalance int64 = 1_000_000
	onlineTradeAbortGoldOffer   int32 = 100_000
)

// v134 closes the trade window through the explicit op24 command with
// one explicit body byte. The old client keeps its original close path.
func TestOnlineItemTradeV134ExplicitAbortCancelsBothSidesOnce(t *testing.T) {
	f := newOnlineCrystalTradeFixture(t, nil)
	trade := prepareOnlineTradeAbortOffers(t, f)
	assertItemTradeAbortAssetsUnchanged(t, f)

	sendOnlineTradeAbort(t, f.service, f.peer)

	assertItemTradeAbortCancelCount(t, f.offerConn, "offerer")
	assertItemTradeAbortCancelCount(t, f.peerConn, "peer")
	assertItemTradeAbortIndexEmpty(t, f.service)
	assertItemTradeAbortAssetsUnchanged(t, f)
	assertItemTradeAbortNoCompletion(t, f)

	// A delayed final confirmation must not recreate the removed trade or
	// consume the offers after both clients have already returned to town.
	for _, session := range []*gameSession{f.offerer, f.peer} {
		if _, err := f.service.handleOnlineItemTradeCommand(session, currentItemTradeReadyMsg, []byte{currentItemTradeReady}); err != nil {
			t.Fatalf("late final confirmation char=%d err=%v", session.selectedCharacterID, err)
		}
	}
	assertItemTradeAbortIndexEmpty(t, f.service)
	assertItemTradeAbortAssetsUnchanged(t, f)
	assertItemTradeAbortNoCompletion(t, f)

	// The original trade object may still be referenced by a queued path after
	// the index entry is gone. Re-cancelling it must not replay the op16 rows.
	f.offerConn.write.Reset()
	f.peerConn.write.Reset()
	f.service.cancelOnlineItemTradeProjection(trade, "duplicate_explicit_trade_window_closed", nil, true)
	assertItemTradeAbortNoCancelPackets(t, f)
	assertItemTradeAbortIndexEmpty(t, f.service)

	// Repeated production abort packets after removal are likewise inert.
	for _, session := range []*gameSession{f.offerer, f.peer} {
		if _, err := f.service.handleOnlineItemTradeCommand(session, currentItemTradeReadyMsg, []byte{currentItemTradeAbortRequest}); err != nil {
			t.Fatalf("repeated abort char=%d err=%v", session.selectedCharacterID, err)
		}
	}
	assertItemTradeAbortNoCancelPackets(t, f)
	assertItemTradeAbortIndexEmpty(t, f.service)
}

func TestOnlineItemTradeExplicitAbortDoesNotClearCommittingTrade(t *testing.T) {
	f := newOnlineCrystalTradeFixture(t, nil)
	trade := prepareOnlineTradeAbortOffers(t, f)

	f.service.tradeMu.Lock()
	trade.committing = true
	f.service.tradeMu.Unlock()
	f.offerConn.write.Reset()
	f.peerConn.write.Reset()

	sendOnlineTradeAbort(t, f.service, f.peer)

	if count := countTradePackets(t, f.offerConn.write.Bytes(), 0, currentItemTradeCancelMsg); count != 0 {
		t.Fatalf("committing trade sent offerer op16 count=%d", count)
	}
	if count := countTradePackets(t, f.peerConn.write.Bytes(), 0, currentItemTradeCancelMsg); count != 0 {
		t.Fatalf("committing trade sent peer op16 count=%d", count)
	}
	assertItemTradeAbortTradeIndexed(t, f.service, trade)
	if !trade.committing {
		t.Fatal("explicit abort cleared committing reservation")
	}
	assertItemTradeAbortAssetsUnchanged(t, f)
	assertItemTradeAbortNoCompletion(t, f)
}

func TestOnlineItemTradeExplicitAbortRejectsStaleOrMismatchedSession(t *testing.T) {
	t.Run("stale_session", func(t *testing.T) {
		f := newOnlineCrystalTradeFixture(t, nil)
		trade := prepareOnlineTradeAbortOffers(t, f)
		stale := &gameSession{
			conn: &bufferConn{}, channel: f.offerer.channel,
			selectedCharacterID: 1, accountID: onlineCrystalTradeAccount,
			loginPacketSeen: true, accountFromToken: true,
		}

		sendOnlineTradeAbort(t, f.service, stale)

		assertItemTradeAbortRejected(t, stale.conn.(*bufferConn), "stale session")
		assertItemTradeAbortTradeIndexed(t, f.service, trade)
		assertItemTradeAbortNoRealParticipantCancel(t, f)
		assertItemTradeAbortAssetsUnchanged(t, f)
	})

	t.Run("account_mismatch", func(t *testing.T) {
		f := newOnlineCrystalTradeFixture(t, nil)
		trade := prepareOnlineTradeAbortOffers(t, f)
		f.offerer.accountID = onlineCrystalTradePeerAccount

		sendOnlineTradeAbort(t, f.service, f.offerer)

		assertItemTradeAbortRejected(t, f.offerConn, "mismatched account")
		assertItemTradeAbortTradeIndexed(t, f.service, trade)
		if f.peerConn.write.Len() != 0 {
			t.Fatalf("mismatched abort leaked to peer: % X", f.peerConn.write.Bytes())
		}
		assertItemTradeAbortAssetsUnchanged(t, f)
	})
}

func prepareOnlineTradeAbortOffers(t *testing.T, f *onlineCrystalTradeFixture) *onlineItemTrade {
	t.Helper()
	setOnlineTradeGold(t, f, onlineTradeAbortGoldBalance, onlineTradeAbortGoldBalance)
	addOnlineTradeTestGold(t, f, false, onlineTradeAbortGoldOffer)
	addOnlineTradeTestCrystal(t, f, false)
	trade := f.service.currentOnlineItemTrade(1)
	if trade == nil {
		t.Fatal("trade was not indexed after offers")
	}
	if len(trade.offers[1]) != 1 || trade.gold[1] != int64(onlineTradeAbortGoldOffer) {
		t.Fatalf("prepared offers=%+v gold=%d", trade.offers[1], trade.gold[1])
	}
	f.offerConn.write.Reset()
	f.peerConn.write.Reset()
	return trade
}

func sendOnlineTradeAbort(t *testing.T, service *Service, session *gameSession) {
	t.Helper()
	if currentItemTradeReadyMsg != 24 || currentItemTradeAbortRequest != 6 {
		t.Fatalf("trade abort wire identity = op%d body%d, want op24 body6", currentItemTradeReadyMsg, currentItemTradeAbortRequest)
	}
	handled, err := service.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), currentItemTradeReadyMsg, []byte{currentItemTradeAbortRequest})
	if err != nil || !handled {
		t.Fatalf("explicit abort handled=%t err=%v", handled, err)
	}
}

func assertItemTradeAbortCancelCount(t *testing.T, conn *bufferConn, label string) {
	t.Helper()
	if count := countTradePackets(t, conn.write.Bytes(), 0, currentItemTradeCancelMsg); count != 1 {
		t.Fatalf("%s class0/op16 count=%d, want 1; wire=% X", label, count, conn.write.Bytes())
	}
}

func assertItemTradeAbortNoCancelPackets(t *testing.T, f *onlineCrystalTradeFixture) {
	t.Helper()
	for _, row := range []struct {
		label string
		conn  *bufferConn
	}{{"offerer", f.offerConn}, {"peer", f.peerConn}} {
		if count := countTradePackets(t, row.conn.write.Bytes(), 0, currentItemTradeCancelMsg); count != 0 {
			t.Fatalf("%s replayed op16 count=%d wire=% X", row.label, count, row.conn.write.Bytes())
		}
	}
}

func assertItemTradeAbortNoRealParticipantCancel(t *testing.T, f *onlineCrystalTradeFixture) {
	t.Helper()
	if f.offerConn.write.Len() != 0 {
		t.Fatalf("rejected abort wrote to offerer: % X", f.offerConn.write.Bytes())
	}
	if count := countTradePackets(t, f.peerConn.write.Bytes(), 0, currentItemTradeCancelMsg); count != 0 {
		t.Fatalf("rejected abort sent peer op16 count=%d", count)
	}
}

func assertItemTradeAbortRejected(t *testing.T, conn *bufferConn, label string) {
	t.Helper()
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.Classification != dnfproto.DefaultChannelClassification ||
		packet.Header.MsgID != currentItemTradeReadyMsg ||
		!bytes.Equal(packet.Body, []byte{0, 1}) || len(rest) != 0 {
		t.Fatalf("%s rejection class=%d msg=%d body=% X trailing=%d", label,
			packet.Header.Classification, packet.Header.MsgID, packet.Body, len(rest))
	}
}

func assertItemTradeAbortIndexEmpty(t *testing.T, service *Service) {
	t.Helper()
	service.tradeMu.Lock()
	defer service.tradeMu.Unlock()
	if len(service.itemTrades) != 0 {
		t.Fatalf("trade index entries=%d, want empty", len(service.itemTrades))
	}
}

func assertItemTradeAbortTradeIndexed(t *testing.T, service *Service, trade *onlineItemTrade) {
	t.Helper()
	service.tradeMu.Lock()
	defer service.tradeMu.Unlock()
	for _, characterID := range trade.participants {
		if service.itemTrades[characterID] != trade {
			t.Fatalf("trade index for character %d was cleared or replaced", characterID)
		}
	}
}

func assertItemTradeAbortNoCompletion(t *testing.T, f *onlineCrystalTradeFixture) {
	t.Helper()
	for _, row := range []struct {
		label string
		conn  *bufferConn
	}{{"offerer", f.offerConn}, {"peer", f.peerConn}} {
		if count := countTradePackets(t, row.conn.write.Bytes(), 0, currentItemTradeFinishMsg); count != 0 {
			t.Fatalf("%s received op18 completion count=%d", row.label, count)
		}
	}
}

func assertItemTradeAbortAssetsUnchanged(t *testing.T, f *onlineCrystalTradeFixture) {
	t.Helper()
	ctx := context.Background()
	account, found, err := f.repositories.AccountInventory.Load(ctx, onlineCrystalTradeAccount)
	if err != nil || !found || len(account.Slots) != 1 {
		t.Fatalf("account warehouse found=%t slots=%+v err=%v", found, account.Slots, err)
	}
	stack := account.Slots[dnfrepo.AccountSharedInventorySlotKey(onlineCrystalTradeSlot)]
	if stack.ItemID != onlineCrystalTradeItemID || stack.Count != onlineCrystalTradeCount {
		t.Fatalf("account crystal changed: %+v", stack)
	}
	bag, found, err := f.repositories.Inventory.Load(ctx, "1")
	if err != nil || !found || len(bag.Slots) != 0 {
		t.Fatalf("character bag found=%t slots=%+v err=%v", found, bag.Slots, err)
	}
	assertOnlineTradeGoldUnchanged(t, f, onlineTradeAbortGoldBalance, onlineTradeAbortGoldBalance)
}
