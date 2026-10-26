package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestDelilahNPCGoldDoesNotUsePersonalMachineRollbackWallet(t *testing.T) {
	repositories, catalog := mustCurrentDisjointTestAssets(t, map[string]dnfrepo.ItemStack{
		"0:0":  {ItemID: 0, Count: 7},
		"0:65": {ItemID: 5000, Count: 1},
	})
	setDelilahCostTestGold(t, repositories, 500)
	service := currentDisjointTestService(repositories, catalog)
	service.options.expertJobGoldSource = expertJobGoldInventoryWallet
	connection := &bufferConn{}
	session := &gameSession{conn: connection, selectedCharacterID: 19}
	attachDelilahCostCompletedRuntime(t, session)
	if err := service.handleCurrentDisjointItem(session, currentDisjointItemTestBody(65, 0, -1, 1000)); err != nil {
		t.Fatal(err)
	}
	ack, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(ack.Body) == 0 || ack.Body[0] != 1 {
		t.Fatalf("exact-balance NPC request failed: %x", ack.Body)
	}
	wallet, rest := splitGameServerUpperPacket(t, trailing)
	if len(rest) != 0 || wallet.Header.Classification != 0 || wallet.Header.MsgID != 14 || !bytes.Equal(wallet.Body, buildCurrentGoldStateBody(0)) {
		t.Fatalf("zero wallet=%x rest=%d", wallet.Body, len(rest))
	}
	if got := loadDelilahCostTestGold(t, repositories); got != 0 {
		t.Fatalf("character gold=%d want zero", got)
	}
	inventory, _, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Slots["0:0"].Count != 7 {
		t.Fatal("NPC fee changed personal-machine legacy wallet")
	}
}

type delilahReplyFailConn struct {
	bufferConn
	writes int
	failAt int
	err    error
}

func (c *delilahReplyFailConn) Write(data []byte) (int, error) {
	c.writes++
	if c.writes == c.failAt {
		return 0, c.err
	}
	return c.bufferConn.Write(data)
}

func TestDelilahCommittedReplyFailureDoesNotSendBusinessRejection(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		repositories, catalog := mustCurrentDisjointTestAssets(t, map[string]dnfrepo.ItemStack{"0:65": {ItemID: 5000, Count: 1}})
		setDelilahCostTestGold(t, repositories, 2000)
		wantErr := errors.New("controlled disjoint reply failure")
		connection := &delilahReplyFailConn{failAt: failAt, err: wantErr}
		service := currentDisjointTestService(repositories, catalog)
		session := &gameSession{conn: connection, selectedCharacterID: 19}
		attachDelilahCostCompletedRuntime(t, session)
		err := service.handleCurrentDisjointItem(session, currentDisjointItemTestBody(65, 0, -1, 1000))
		if !errors.Is(err, wantErr) {
			t.Fatalf("failure at %d: err=%v", failAt, err)
		}
		if connection.writes != failAt {
			t.Fatalf("failure at %d attempted %d writes: extra rejection or retry", failAt, connection.writes)
		}
		if got := loadDelilahCostTestGold(t, repositories); got != 1500 {
			t.Fatalf("committed gold=%d want 1500", got)
		}
		inventory, _, err := repositories.Inventory.Load(context.Background(), "19")
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := inventory.Slots["0:65"]; exists {
			t.Fatal("source deletion was not committed")
		}
		if failAt == 2 {
			ack, tail := splitGameServerUpperPacket(t, connection.write.Bytes())
			if len(tail) != 0 || ack.Header.MsgID != 26 || len(ack.Body) == 0 || ack.Body[0] != 1 {
				t.Fatal("wrong completed ACK before wallet failure")
			}
		}
	}
}

func TestDelilahInvalidBatchKeepsGoldAndEverySource(t *testing.T) {
	repositories, catalog := mustCurrentDisjointTestAssets(t, map[string]dnfrepo.ItemStack{
		"0:65": {ItemID: 5000, Count: 1}, "0:66": {ItemID: 5000, Count: 2},
	})
	setDelilahCostTestGold(t, repositories, 2000)
	service := currentDisjointTestService(repositories, catalog)
	connection := &bufferConn{}
	session := &gameSession{conn: connection, selectedCharacterID: 19}
	attachDelilahCostCompletedRuntime(t, session)
	if err := service.handleCurrentDisjointItem(session, currentDisjointBatchBody([]int16{65, 66}, -1, 1000)); err != nil {
		t.Fatal(err)
	}
	failure, tail := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(tail) != 0 || !bytes.Equal(failure.Body, []byte{0, 19}) {
		t.Fatalf("failure=%x tail=%d", failure.Body, len(tail))
	}
	if got := loadDelilahCostTestGold(t, repositories); got != 2000 {
		t.Fatalf("rejected batch charged gold=%d", got)
	}
	inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
	if inventory.Slots["0:65"].Count != 1 || inventory.Slots["0:66"].Count != 2 {
		t.Fatal("rejected batch changed sources")
	}
	assertDelilahCostNoAccountReward(t, repositories)
}
