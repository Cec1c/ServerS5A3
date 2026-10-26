package dnfbridge

import (
	"fmt"
	"net"
	"testing"
)

const testPVPUDPRelayScopePortCount = 80

func TestPVPRelayEightSeatsAndOrdinaryPartyUseSeparateNamespaces(t *testing.T) {
	portStart := testPVPUDPRelayScopePortStart(t)
	t.Logf("relay fixture bindIP=127.0.0.1 range=%d-%d", portStart, portStart+testPVPUDPRelayScopePortCount-1)
	r, err := newCurrentPartyUDPRelay(true, "127.0.0.1", portStart, testPVPUDPRelayScopePortCount, func(message string, args ...any) {
		t.Logf("%s %v", message, args)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	pair := []currentPartyUDPRelayBinding{{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}}, {characterID: 102, generation: 1, address: [4]byte{127, 0, 0, 1}}}
	if !r.Sync(1, pair) || !r.syncGroup(true, 1, pair) {
		t.Fatal("同编号队伍与决斗房间应能并存")
	}
	_, partyPort, ok := r.Endpoint(1, 101, 102)
	if !ok {
		t.Fatal("队伍端点丢失")
	}
	r.mu.Lock()
	pvpLeg := r.legs[currentPartyUDPRelayLegKey{pvp: true, partyID: 1, ownerID: 101, peerID: 102}]
	r.mu.Unlock()
	if pvpLeg == nil || pvpLeg.port == partyPort {
		t.Fatal("决斗与队伍共用了端口")
	}
	eight := make([]currentPartyUDPRelayBinding, 8)
	for i := range eight {
		eight[i] = currentPartyUDPRelayBinding{characterID: uint16(201 + i), generation: 1, address: [4]byte{127, 0, 0, 1}}
	}
	if !r.syncGroup(true, 2, eight) {
		t.Fatal("8席决斗中继不完整")
	}
	if r.Sync(3, eight) {
		t.Fatal("普通队伍人数上限被决斗修改")
	}
	count := 0
	ports := map[uint16]bool{}
	r.mu.Lock()
	for k, v := range r.legs {
		if k.pvp && k.partyID == 2 {
			count++
			if ports[v.port] {
				t.Error("决斗腿端口重复")
			}
			ports[v.port] = true
		}
	}
	r.mu.Unlock()
	if count != 56 {
		t.Fatal("8席应有56条有向腿", count)
	}
	r.CloseParty(1)
	r.mu.Lock()
	pvpPresent := r.legs[currentPartyUDPRelayLegKey{pvp: true, partyID: 1, ownerID: 101, peerID: 102}] != nil
	r.mu.Unlock()
	if !pvpPresent {
		t.Fatal("普通队伍解散清理了决斗")
	}
	if !r.Sync(1, pair) {
		t.Fatal("重建普通队伍失败")
	}
	_, partyPort, ok = r.Endpoint(1, 101, 102)
	if !ok {
		t.Fatal("普通队伍端点不存在")
	}
	r.closePVPRoom(1)
	_, after, ok := r.Endpoint(1, 101, 102)
	if !ok || after != partyPort {
		t.Fatal("决斗退出影响同编号普通队伍")
	}
	r.closePVPRoom(2)
	r.mu.Lock()
	remaining := len(r.legs)
	r.mu.Unlock()
	if remaining != 2 {
		t.Fatal("决斗退出泄漏中继腿", remaining)
	}
}

func testPVPUDPRelayScopePortStart(t *testing.T) int {
	t.Helper()
	var lastErr error
	lastRange := "not allocated"
	for attempt := 0; attempt < 20; attempt++ {
		lastRange = "not allocated"
		listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			lastErr = err
			continue
		}
		port := listener.LocalAddr().(*net.UDPAddr).Port
		lastRange = fmt.Sprintf("%d-%d", port, port+testPVPUDPRelayScopePortCount-1)
		if port+testPVPUDPRelayScopePortCount-1 > 65535 {
			_ = listener.Close()
			lastErr = fmt.Errorf("eighty-port range starting at %d exceeds 65535", port)
			continue
		}
		// Hold the whole eighty-port range together, as in the relay fixture probe.
		listeners := []*net.UDPConn{listener}
		for offset := 1; offset < testPVPUDPRelayScopePortCount; offset++ {
			next, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port + offset})
			if err != nil {
				lastErr = err
				break
			}
			listeners = append(listeners, next)
		}
		for _, held := range listeners {
			_ = held.Close()
		}
		if len(listeners) == testPVPUDPRelayScopePortCount {
			// The relay binds after these closes; a TOCTOU window still remains.
			return port
		}
	}
	t.Fatalf("could not allocate a usable eighty-port UDP test range after 20 attempts: bindIP=127.0.0.1 range=%s error=%v", lastRange, lastErr)
	return 0
}
