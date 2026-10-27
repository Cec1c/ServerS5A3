package dnfbridge

import "testing"

func TestPVPRelayEightSeatsAndOrdinaryPartyUseSeparateNamespaces(t *testing.T) {
	// Two two-person groups plus eight PVP seats need 60 directed ports.
	// Reserve a verified 64-port block instead of assuming the 80 ports after
	// one ephemeral socket are free (or even below port 65536).
	r, err := newCurrentPartyUDPRelay(true, "127.0.0.1", r137FreeRange(t), 64, nil)
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
