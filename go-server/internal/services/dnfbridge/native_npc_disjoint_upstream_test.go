package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func TestNativeNPCBatchKeepsUpstreamEntryBeforeAssets(t *testing.T) {
	s, p, repos, trace, _ := nativeNPCBatchFixture(t, 2)
	t.Setenv("DNFBRIDGE_NATIVE_RESULT_DELIVERY", "on")
	packets := []dnfproto.LatestGameStreamPacket{
		nativeResultTestPacket(26, currentDisjointItemTestBody(10, 0, -1, 1000)),
		nativeResultTestPacket(26, currentDisjointItemTestBody(11, 0, -1, 1000)),
	}
	beforeBag, _, _ := repos.Inventory.Load(context.Background(), "19")
	beforeChar, _, _ := repos.Character.Load(context.Background(), "19")
	if handled, err := s.handleNativeNPCDisjointBatch(p, packets); handled || err != nil {
		t.Fatal("收费或不准入入口应走原路径", handled, err)
	}
	if trace.transactions != 0 || p.nativeResultDelivery != nil || p.nativeNPCDisjointReplay != nil {
		t.Fatal("入口拒绝前预先结算或创建批次")
	}
	for _, packet := range packets {
		if err := s.handleGameStreamPacket(p, packet); err != nil {
			t.Fatal(err)
		}
	}
	afterBag, _, _ := repos.Inventory.Load(context.Background(), "19")
	afterChar, _, _ := repos.Character.Load(context.Background(), "19")
	if trace.transactions != 0 || !reflect.DeepEqual(beforeBag, afterBag) || !reflect.DeepEqual(beforeChar, afterChar) {
		t.Fatal("城镇伪造德利拉请求改变了装备或金币")
	}
	wire := splitAllGameServerUpperPackets(t, p.conn.(*channelSceneWriteConn).write.Bytes())
	if len(wire) != 2 {
		t.Fatal("入口拒绝应逐件失败", len(wire))
	}
	for _, packet := range wire {
		if packet.Header.MsgID != 26 || packet.Body[0] != 0 {
			t.Fatal("入口拒绝仍回成功")
		}
	}
}
