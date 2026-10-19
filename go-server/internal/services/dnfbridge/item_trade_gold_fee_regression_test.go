package dnfbridge

import (
	"bytes"
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	onlineGoldFeeRegressionBalance  int64 = 50_000
	onlineGoldFeeRegressionFirst    int32 = 9_999
	onlineGoldFeeRegressionSecond   int32 = 10_001
	onlineGoldFeeRegressionFirstFee int64 = 299
	onlineGoldFeeRegressionPeerFee  int64 = 300
)

func newOnlineGoldFeeTradeFixture(t *testing.T, firstGold, secondGold int64) *onlineCrystalTradeFixture {
	t.Helper()
	return newOnlineCrystalTradeFixture(t, func(f *onlineCrystalTradeFixture) {
		ctx := context.Background()
		for id, gold := range map[string]int64{"1": firstGold, "5": secondGold} {
			character, found, err := f.repositories.Character.Load(ctx, id)
			if err != nil || !found {
				t.Fatalf("load character %s found=%t err=%v", id, found, err)
			}
			if character.Stats == nil {
				character.Stats = make(map[string]int64)
			}
			character.Stats["gold"] = gold
			if err := f.repositories.Character.Save(ctx, character); err != nil {
				t.Fatalf("save character %s gold: %v", id, err)
			}
		}
	})
}

func depositOnlineGoldForRegression(t *testing.T, f *onlineCrystalTradeFixture, session *gameSession, amount int32) {
	t.Helper()
	request := buildOnlineItemTradeMoveRequest(dnfrepo.MainInventoryListType, 0, 0, amount, currentItemTradeListType, 0)
	handled, err := f.service.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), currentItemTradeMoveMsg, request)
	if err != nil || !handled {
		t.Fatalf("character %d gold offer %d handled=%t err=%v", session.selectedCharacterID, amount, handled, err)
	}
}

func confirmOnlineGoldFeeTradeForRegression(t *testing.T, f *onlineCrystalTradeFixture) {
	t.Helper()
	for _, state := range []byte{currentItemTradeAddRequest, currentItemTradeReady} {
		for _, session := range []*gameSession{f.offerer, f.peer} {
			handled, err := f.service.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), currentItemTradeReadyMsg, []byte{state})
			if err != nil || !handled {
				t.Fatalf("character %d state %d handled=%t err=%v", session.selectedCharacterID, state, handled, err)
			}
		}
	}
	if f.service.currentOnlineItemTrade(1) != nil || f.service.currentOnlineItemTrade(5) != nil {
		t.Fatal("terminal trade retained live state")
	}
}

func onlineGoldFeeFinishBodies(t *testing.T, wire []byte) [][]byte {
	t.Helper()
	var bodies [][]byte
	for len(wire) > 0 {
		packet, rest := splitGameServerUpperPacket(t, wire)
		if packet.Header.Classification == 0 && packet.Header.MsgID == currentItemTradeFinishMsg {
			bodies = append(bodies, append([]byte(nil), packet.Body...))
		}
		wire = rest
	}
	return bodies
}

func assertSingleOnlineGoldFeeFinishBody(t *testing.T, wire []byte, want []byte) {
	t.Helper()
	bodies := onlineGoldFeeFinishBodies(t, wire)
	if len(bodies) != 1 {
		t.Fatalf("op18 finish packets=%d bodies=% X, want one % X", len(bodies), bodies, want)
	}
	if !bytes.Equal(bodies[0], want) {
		t.Fatalf("op18 finish body=% X, want % X", bodies[0], want)
	}
}

func assertOnlineGoldFeeWallet(t *testing.T, wire []byte, wantGold int64) {
	t.Helper()
	want := buildCurrentGoldStateBody(wantGold)
	original := wire
	for len(wire) > 0 {
		packet, rest := splitGameServerUpperPacket(t, wire)
		if packet.Header.Classification == 0 && packet.Header.MsgID == uint16(dnfenum.CmdPacketWalkoutPartyMember) &&
			bytes.Equal(packet.Body, want) {
			return
		}
		wire = rest
	}
	t.Fatalf("authoritative wallet for gold=%d not sent; wire=% X", wantGold, original)
}

func onlineGoldFeeCharacterGold(t *testing.T, f *onlineCrystalTradeFixture, id string) int64 {
	t.Helper()
	character, found, err := f.repositories.Character.Load(context.Background(), id)
	if err != nil || !found {
		t.Fatalf("load character %s found=%t err=%v", id, found, err)
	}
	return character.Stats["gold"]
}

func assertNoRepeatedOnlineGoldFeeSettlement(t *testing.T, f *onlineCrystalTradeFixture, wants map[string]int64) {
	t.Helper()
	f.offerConn.write.Reset()
	f.peerConn.write.Reset()
	for repeat := 0; repeat < 2; repeat++ {
		for _, session := range []*gameSession{f.offerer, f.peer} {
			if _, err := f.service.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), currentItemTradeReadyMsg, []byte{currentItemTradeReady}); err != nil {
				t.Fatalf("duplicate final state from character %d: %v", session.selectedCharacterID, err)
			}
		}
	}
	if data := f.offerConn.write.Bytes(); len(data) != 0 {
		t.Fatalf("duplicate final state wrote to offerer: % X", data)
	}
	if data := f.peerConn.write.Bytes(); len(data) != 0 {
		t.Fatalf("duplicate final state wrote to peer: % X", data)
	}
	for id, want := range wants {
		if got := onlineGoldFeeCharacterGold(t, f, id); got != want {
			t.Fatalf("character %s gold after duplicate final state=%d, want %d", id, got, want)
		}
	}
}

func TestOnlineItemTradePureGoldFinishRowsAndFloorFee(t *testing.T) {
	t.Run("both participants receive a gold row", func(t *testing.T) {
		f := newOnlineGoldFeeTradeFixture(t, onlineGoldFeeRegressionBalance, onlineGoldFeeRegressionBalance)
		depositOnlineGoldForRegression(t, f, f.offerer, onlineGoldFeeRegressionFirst)
		depositOnlineGoldForRegression(t, f, f.peer, onlineGoldFeeRegressionSecond)
		f.offerConn.write.Reset()
		f.peerConn.write.Reset()

		confirmOnlineGoldFeeTradeForRegression(t, f)
		assertSingleOnlineGoldFeeFinishBody(t, f.offerConn.write.Bytes(), []byte{1, 0, 0, 0, 0, 0})
		assertSingleOnlineGoldFeeFinishBody(t, f.peerConn.write.Bytes(), []byte{1, 0, 0, 0, 0, 0})

		wants := map[string]int64{
			"1": onlineGoldFeeRegressionBalance - int64(onlineGoldFeeRegressionFirst) + int64(onlineGoldFeeRegressionSecond) - onlineGoldFeeRegressionPeerFee,
			"5": onlineGoldFeeRegressionBalance - int64(onlineGoldFeeRegressionSecond) + int64(onlineGoldFeeRegressionFirst) - onlineGoldFeeRegressionFirstFee,
		}
		for id, want := range wants {
			if got := onlineGoldFeeCharacterGold(t, f, id); got != want {
				t.Fatalf("character %s gold=%d, want %d", id, got, want)
			}
		}
		assertOnlineGoldFeeWallet(t, f.offerConn.write.Bytes(), wants["1"])
		assertOnlineGoldFeeWallet(t, f.peerConn.write.Bytes(), wants["5"])
		assertNoRepeatedOnlineGoldFeeSettlement(t, f, wants)
	})

	t.Run("one-way gold omits the zero-recipient row", func(t *testing.T) {
		f := newOnlineGoldFeeTradeFixture(t, onlineGoldFeeRegressionBalance, onlineGoldFeeRegressionBalance)
		depositOnlineGoldForRegression(t, f, f.offerer, onlineGoldFeeRegressionFirst)
		f.offerConn.write.Reset()
		f.peerConn.write.Reset()

		confirmOnlineGoldFeeTradeForRegression(t, f)
		assertSingleOnlineGoldFeeFinishBody(t, f.offerConn.write.Bytes(), []byte{0, 0})
		assertSingleOnlineGoldFeeFinishBody(t, f.peerConn.write.Bytes(), []byte{1, 0, 0, 0, 0, 0})

		wants := map[string]int64{
			"1": onlineGoldFeeRegressionBalance - int64(onlineGoldFeeRegressionFirst),
			"5": onlineGoldFeeRegressionBalance + int64(onlineGoldFeeRegressionFirst) - onlineGoldFeeRegressionFirstFee,
		}
		for id, want := range wants {
			if got := onlineGoldFeeCharacterGold(t, f, id); got != want {
				t.Fatalf("character %s gold=%d, want %d", id, got, want)
			}
		}
		assertNoRepeatedOnlineGoldFeeSettlement(t, f, wants)
	})
}

func TestOnlineItemTradeMixedGoldAndItemFinishRows(t *testing.T) {
	ctx := context.Background()
	f := newOnlineGoldFeeTradeFixture(t, onlineGoldFeeRegressionBalance, onlineGoldFeeRegressionBalance)
	request := buildOnlineItemTradeMoveRequest(dnfrepo.MainInventoryListType, onlineCrystalTradeSlot, 0, onlineCrystalTradeMovedCount, currentItemTradeListType, 0)
	handled, err := f.service.handleAlignedGameCommand(f.offerer, byte(dnfenum.GameCmdCommand), currentItemTradeMoveMsg, request)
	if err != nil || !handled {
		t.Fatalf("crystal offer handled=%t err=%v", handled, err)
	}
	depositOnlineGoldForRegression(t, f, f.offerer, onlineGoldFeeRegressionFirst)
	depositOnlineGoldForRegression(t, f, f.peer, onlineGoldFeeRegressionSecond)
	f.offerConn.write.Reset()
	f.peerConn.write.Reset()

	confirmOnlineGoldFeeTradeForRegression(t, f)
	assertSingleOnlineGoldFeeFinishBody(t, f.offerConn.write.Bytes(), []byte{1, 0, 0, 0, 0, 0})
	assertSingleOnlineGoldFeeFinishBody(t, f.peerConn.write.Bytes(), []byte{
		2, 0,
		0, 0, 0, 0,
		3, 0, 0x66, 0x01,
	})

	wants := map[string]int64{
		"1": onlineGoldFeeRegressionBalance - int64(onlineGoldFeeRegressionFirst) + int64(onlineGoldFeeRegressionSecond) - onlineGoldFeeRegressionPeerFee,
		"5": onlineGoldFeeRegressionBalance - int64(onlineGoldFeeRegressionSecond) + int64(onlineGoldFeeRegressionFirst) - onlineGoldFeeRegressionFirstFee,
	}
	for id, want := range wants {
		if got := onlineGoldFeeCharacterGold(t, f, id); got != want {
			t.Fatalf("character %s gold=%d, want %d", id, got, want)
		}
	}
	offererAccount, found, err := f.repositories.AccountInventory.Load(ctx, onlineCrystalTradeAccount)
	if err != nil || !found || offererAccount.Slots[dnfrepo.AccountSharedInventorySlotKey(onlineCrystalTradeSlot)].Count != onlineCrystalTradeCount-onlineCrystalTradeMovedCount {
		t.Fatalf("offerer crystal after mixed trade=%+v found=%t err=%v", offererAccount.Slots, found, err)
	}
	peerAccount, found, err := f.repositories.AccountInventory.Load(ctx, onlineCrystalTradePeerAccount)
	if err != nil || !found || peerAccount.Slots[dnfrepo.AccountSharedInventorySlotKey(onlineCrystalTradeSlot)].Count != onlineCrystalTradeMovedCount {
		t.Fatalf("peer crystal after mixed trade=%+v found=%t err=%v", peerAccount.Slots, found, err)
	}
	assertNoRepeatedOnlineGoldFeeSettlement(t, f, wants)
}
