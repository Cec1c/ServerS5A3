package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func tradeInvitationFixture(t *testing.T) *onlineCrystalTradeFixture {
	t.Helper()
	f := newOnlineCrystalTradeFixture(t, nil)
	setOnlineTradeGold(t, f, 1_000_000, 1_000_000)
	for _, id := range []string{"1", "5"} {
		record, _, err := f.repositories.Character.Load(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		record.CreatedAt = time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
		if err := f.repositories.Character.Save(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	f.service.clearOnlineItemTrade(f.service.currentOnlineItemTrade(1))
	return f
}

func TestTradeInvitationRejectConsumesPendingWithoutOpeningTrade(t *testing.T) {
	f := tradeInvitationFixture(t)
	requestTradeInvitation(t, f)
	if err := f.service.handleOnlineResponsePeer(f.peer, 11, []byte{1, 0, 1, 0, 0, 0, 0, 133, 0}); err != nil {
		t.Fatal(err)
	}
	if f.service.currentOnlineItemTrade(1) != nil || f.peerConn.write.Len() != 0 {
		t.Fatal("rejection opened recipient trade")
	}
	if f.offerConn.write.Len() == 0 {
		t.Fatal("requester not informed")
	}
	f.offerConn.write.Reset()
	if err := f.service.handleOnlineResponsePeer(f.peer, 11, []byte{1, 0, 1, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if f.service.currentOnlineItemTrade(1) != nil || f.peerConn.write.Len() != 0 || f.offerConn.write.Len() != 0 {
		t.Fatal("late accept resurrected rejected invite")
	}
	assertItemTradeAbortAssetsUnchanged(t, f)
}

func TestTradeInvitationInvalidCharacterMetadataCannotOpen(t *testing.T) {
	for _, mutate := range []func(*onlineCrystalTradeFixture){
		func(f *onlineCrystalTradeFixture) {
			r, _, _ := f.repositories.Character.Load(context.Background(), "1")
			r.AccountID = "someone-else"
			_ = f.repositories.Character.Save(context.Background(), r)
		},
		func(f *onlineCrystalTradeFixture) {
			r, _, _ := f.repositories.Character.Load(context.Background(), "1")
			r.CreatedAt = time.Time{}
			_ = f.repositories.Character.Save(context.Background(), r)
		},
	} {
		f := tradeInvitationFixture(t)
		mutate(f)
		if err := f.service.handleOnlineRequestPeer(f.offerer, 10, []byte{5, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0}); err != nil {
			t.Fatal(err)
		}
		ack, _ := splitGameServerUpperPacket(t, f.offerConn.write.Bytes())
		if !bytes.Equal(ack.Body, []byte{0, 19, 1}) || f.peerConn.write.Len() != 0 {
			t.Fatal("invalid repository metadata invited peer")
		}
	}
}

func TestTradeInvitationSlowWriterReleasesWireLock(t *testing.T) {
	f := tradeInvitationFixture(t)
	local, remote := net.Pipe()
	defer remote.Close()
	defer local.Close()
	f.peer.conn = local
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := f.service.sendCurrentItemTradeInvitationFrame(ctx, f.peer, 11, 1, []byte{1, 1, 0, 1, 0}); err == nil {
		t.Fatal("blocked write succeeded")
	}
	if !f.peer.wireMu.TryLock() {
		t.Fatal("wire lock retained")
	}
	f.peer.wireMu.Unlock()
	if _, err := remote.Write([]byte{1}); err == nil {
		t.Fatal("failed peer stream remains open")
	}
}

func TestTradeInvitationHandshakeFailureClearsOpeningState(t *testing.T) {
	for _, failRequester := range []bool{false, true} {
		f := tradeInvitationFixture(t)
		requestTradeInvitation(t, f)
		target, conn, op, class := f.peer, f.peerConn, uint16(11), byte(1)
		if failRequester {
			target, conn, op, class = f.offerer, f.offerConn, 8, 0
		}
		target.conn = &onlineItemTradeStageFailingConn{bufferConn: conn, msgID: op, classification: class, err: errors.New("handshake write failed")}
		_ = f.service.handleOnlineResponsePeer(f.peer, 11, []byte{1, 0, 1, 0, 0, 0, 0})
		if f.service.currentOnlineItemTrade(1) != nil || f.service.currentOnlineItemTrade(5) != nil {
			t.Fatal("failed handshake retained trade")
		}
		assertItemTradeAbortAssetsUnchanged(t, f)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		for _, session := range []*gameSession{f.offerer, f.peer} {
			identity, _ := f.service.boundGameSessionCharacterSnapshot(session)
			r, err := f.service.reserveCurrentCharacterAssets(ctx, identity, session.accountID)
			if err != nil {
				t.Fatal(err)
			}
			f.service.releaseCurrentCharacterAssets(r)
		}
		cancel()
	}
}

func TestTradeInvitationBusyRequestDoesNotDestroyExistingTrade(t *testing.T) {
	f := newOnlineCrystalTradeFixture(t, nil)
	old := f.service.currentOnlineItemTrade(1)
	if err := f.service.handleOnlineRequestPeer(f.offerer, 10, []byte{5, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, f.offerConn.write.Bytes())
	if !bytes.Equal(ack.Body, []byte{0, 19, 1}) || f.peerConn.write.Len() != 0 || f.service.currentOnlineItemTrade(1) != old {
		t.Fatal("busy invitation changed active trade")
	}
}

func TestTradeInvitationExpiredHandshakeResetsHealthyPeer(t *testing.T) {
	f := tradeInvitationFixture(t)
	requestTradeInvitation(t, f)
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	f.offerer.conn = local // Requester stops reading the acceptance notice.
	if err := f.service.handleOnlineResponsePeer(f.peer, 11, []byte{1, 0, 1, 0, 0, 0, 0}); err != nil {
		t.Fatalf("failed requester closed healthy recipient: %v", err)
	}
	ack, rest := splitGameServerUpperPacket(t, f.peerConn.write.Bytes())
	reset, rest := splitGameServerUpperPacket(t, rest)
	if ack.Header.MsgID != 11 || reset.Header.MsgID != currentItemTradeCancelMsg ||
		!bytes.Equal(reset.Body, []byte{0, 0}) || len(rest) != 0 {
		t.Fatal("healthy recipient did not receive empty rollback after timeout")
	}
	if f.service.currentOnlineItemTrade(1) != nil || f.service.currentOnlineItemTrade(5) != nil {
		t.Fatal("timed out trade remains")
	}
	assertItemTradeAbortAssetsUnchanged(t, f)
}

func TestTradeInvitationOpeningCannotMutateOffersOrConfirm(t *testing.T) {
	f := tradeInvitationFixture(t)
	trade := f.service.beginOnlineItemTradeState(f.offerer, f.peer, true)
	if trade == nil {
		t.Fatal("opening trade not created")
	}
	for _, op := range []uint16{currentItemTradeMoveMsg, currentItemTradeReadyMsg} {
		if handled, err := f.service.handleOnlineItemTradeCommand(f.offerer, op, []byte{3}); !handled || err != nil {
			t.Fatalf("opening command %d: %v %v", op, handled, err)
		}
	}
	if len(trade.offers[1]) != 0 || trade.gold[1] != 0 || trade.states[1] != 0 || trade.committing {
		t.Fatal("opening trade changed before both notifications")
	}
	assertItemTradeAbortAssetsUnchanged(t, f)
}

func TestTradeInvitationSuccessfulHandshakeAndNativePackets(t *testing.T) {
	f := tradeInvitationFixture(t)
	var packets []map[string]any
	capture := func(stage string) {
		for _, row := range []struct {
			name string
			conn *bufferConn
		}{{"requester", f.offerConn}, {"recipient", f.peerConn}} {
			for wire := row.conn.write.Bytes(); len(wire) > 0; {
				packet, rest := splitGameServerUpperPacket(t, wire)
				wire = rest
				packets = append(packets, map[string]any{"stage": stage, "peer": row.name, "opcode": packet.Header.MsgID, "class": packet.Header.Classification, "body": packet.Body})
			}
			row.conn.write.Reset()
		}
	}
	if err := f.service.handleOnlineRequestPeer(f.offerer, 10, []byte{5, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	capture("invite")
	if err := f.service.handleOnlineResponsePeer(f.peer, 11, []byte{1, 0, 1, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	trade := f.service.currentOnlineItemTrade(1)
	packet, _ := splitGameServerUpperPacket(t, f.offerConn.write.Bytes())
	created := uint32(time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC).Unix())
	if packet.Header.MsgID != 8 || len(packet.Body) != 12 || binary.LittleEndian.Uint32(packet.Body[3:7]) != 1_000_000 || binary.LittleEndian.Uint32(packet.Body[8:]) != created {
		t.Fatalf("invalid native trade notice: %x", packet.Body)
	}
	if trade == nil || trade != f.service.currentOnlineItemTrade(5) || trade.opening {
		t.Fatal("handshake did not finish")
	}
	capture("accept")
	assertItemTradeAbortAssetsUnchanged(t, f)
	// An actual invitation joins the existing item and gold transaction path.
	addOnlineTradeTestCrystal(t, f, false)
	addOnlineTradeTestGold(t, f, false, 100_000)
	capture("offers")
	for _, session := range []*gameSession{f.offerer, f.peer} {
		if handled, err := f.service.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), currentItemTradeReadyMsg, []byte{5}); !handled || err != nil {
			t.Fatalf("lock %v %v", handled, err)
		}
	}
	capture("locked")
	assertItemTradeAbortAssetsUnchanged(t, f)
	if err := f.service.handleOnlineItemTradeState(f.offerer, 3); err != nil {
		t.Fatal(err)
	}
	capture("one-consent")
	assertItemTradeAbortAssetsUnchanged(t, f)
	if err := f.service.handleOnlineItemTradeState(f.peer, 3); err != nil {
		t.Fatal(err)
	}
	capture("committed")
	if f.service.currentOnlineItemTrade(1) != nil {
		t.Fatal("committed trade remains")
	}
	account, _, err := f.repositories.AccountInventory.Load(context.Background(), onlineCrystalTradeAccount)
	if err != nil || account.Slots["0:358"].Count != 4000 {
		t.Fatalf("source crystal=%v %v", account.Slots, err)
	}
	peerAccount, _, err := f.repositories.AccountInventory.Load(context.Background(), onlineCrystalTradePeerAccount)
	if err != nil || peerAccount.Slots["0:358"].Count != 1000 {
		t.Fatalf("recipient crystal=%v %v", peerAccount.Slots, err)
	}
	for id, want := range map[string]int64{"1": 900_000, "5": 1_097_000} {
		if got := onlineGoldFeeCharacterGold(t, f, id); got != want {
			t.Fatalf("%s gold=%d want%d", id, got, want)
		}
	}
	assertNoRepeatedOnlineGoldFeeSettlement(t, f, map[string]int64{"1": 900_000, "5": 1_097_000})
	if name := os.Getenv("DNF_TRADE_INVITATION_PACKETS"); name != "" {
		b, err := json.MarshalIndent(packets, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func requestTradeInvitation(t *testing.T, f *onlineCrystalTradeFixture) {
	t.Helper()
	if err := f.service.handleOnlineRequestPeer(f.offerer, 10, []byte{5, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	f.offerConn.write.Reset()
	f.peerConn.write.Reset()
}

func TestTradeInvitationMovedPeerNeverReceivesSuccess(t *testing.T) {
	f := tradeInvitationFixture(t)
	requestTradeInvitation(t, f)
	f.service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 5, TownID: 39, AreaID: 0, Session: f.peer})
	if err := f.service.handleOnlineResponsePeer(f.peer, 11, []byte{1, 0, 1, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, f.peerConn.write.Bytes())
	if len(ack.Body) == 0 || ack.Body[0] != 0 {
		t.Fatalf("moved peer received false success: %x", ack.Body)
	}
	if f.service.currentOnlineItemTrade(1) != nil || f.service.currentOnlineItemTrade(5) != nil {
		t.Fatal("out of area trade created")
	}
	assertItemTradeAbortAssetsUnchanged(t, f)
}

func TestTradeInvitationBeginPreservesExistingTrade(t *testing.T) {
	for _, committing := range []bool{false, true} {
		f := newOnlineCrystalTradeFixture(t, nil)
		setOnlineTradeGold(t, f, 1_000_000, 1_000_000)
		old := f.service.currentOnlineItemTrade(1)
		old.committing = committing
		third := &gameSession{conn: &bufferConn{}, channel: f.peer.channel, selectedCharacterID: 9, accountID: "c"}
		f.service.bindGameSessionCharacter(third, 9)
		f.service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 9, TownID: 38, AreaID: 0, Session: third})
		f.service.beginOnlineItemTrade(f.offerer, third)
		if f.service.currentOnlineItemTrade(1) != old || f.service.currentOnlineItemTrade(5) != old || f.service.currentOnlineItemTrade(9) != nil {
			t.Fatalf("new invite replaced existing trade, committing=%v", committing)
		}
		assertItemTradeAbortAssetsUnchanged(t, f)
	}
}
