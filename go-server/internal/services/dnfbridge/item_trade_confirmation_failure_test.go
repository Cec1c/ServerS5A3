package dnfbridge

import (
	"context"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestOnlineCrystalFinalConfirmationAckFailureCancelsBeforeCommit(t *testing.T) {
	for _, order := range []bool{false, true} {
		name := "offerer_last"
		if order {
			name = "recipient_last"
		}
		t.Run(name, func(t *testing.T) {
			f := newOnlineCrystalTradeFixture(t, nil)
			ctx := context.Background()
			request := buildOnlineItemTradeMoveRequest(repo.MainInventoryListType, 358, 0, 1000, currentItemTradeListType, 0)
			if handled, err := f.service.handleAlignedGameCommand(f.offerer, byte(dnfenum.GameCmdCommand), currentItemTradeMoveMsg, request); !handled || err != nil {
				t.Fatalf("offer=%t/%v", handled, err)
			}
			for _, session := range []*gameSession{f.offerer, f.peer} {
				if err := f.service.handleOnlineItemTradeState(session, currentItemTradeAddRequest); err != nil {
					t.Fatal(err)
				}
			}
			last, first := f.offerer, f.peer
			lastConn, firstConn := f.offerConn, f.peerConn
			if order {
				last, first = f.peer, f.offerer
				lastConn, firstConn = f.peerConn, f.offerConn
			}
			if err := f.service.handleOnlineItemTradeState(first, currentItemTradeReady); err != nil {
				t.Fatal(err)
			}
			lastConn.write.Reset()
			firstConn.write.Reset()
			injected := errors.New("final confirmation ACK write rejected")
			last.conn = &onlineItemTradeStageFailingConn{
				bufferConn: lastConn, msgID: currentItemTradeReadyMsg, classification: byte(dnfenum.GameCmdCommand), err: injected,
			}
			if err := f.service.handleOnlineItemTradeState(last, currentItemTradeReady); !errors.Is(err, injected) {
				t.Fatalf("confirmation error=%v, want injected error", err)
			}
			if f.service.currentOnlineItemTrade(1) != nil || f.service.currentOnlineItemTrade(5) != nil {
				t.Fatal("failed precommit ACK stranded a committing trade")
			}
			cancellations := 0
			for wire := firstConn.write.Bytes(); len(wire) > 0; {
				packet, rest := splitGameServerUpperPacket(t, wire)
				wire = rest
				if packet.Header.Classification == 0 && packet.Header.MsgID == currentItemTradeCancelMsg {
					cancellations++
				}
				if packet.Header.Classification == 0 && packet.Header.MsgID == currentItemTradeFinishMsg {
					t.Fatal("failed final confirmation emitted a completion")
				}
			}
			if cancellations != 1 {
				t.Fatalf("healthy recipient cancel notifications=%d, want one", cancellations)
			}
			for _, session := range []*gameSession{f.offerer, f.peer} {
				if err := f.service.handleOnlineItemTradeState(session, currentItemTradeReady); err != nil {
					t.Fatal(err)
				}
			}
			a, _, err := f.repositories.AccountInventory.Load(ctx, "a")
			if err != nil || a.Slots["0:358"].Count != 5000 {
				t.Fatalf("failed confirmation debited source: %+v err=%v", a, err)
			}
			b, _, err := f.repositories.AccountInventory.Load(ctx, "b")
			if err != nil || len(b.Slots) != 0 {
				t.Fatalf("failed confirmation credited recipient: %+v err=%v", b, err)
			}
		})
	}
}
