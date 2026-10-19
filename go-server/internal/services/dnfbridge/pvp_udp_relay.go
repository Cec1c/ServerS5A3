package dnfbridge

import (
	"fmt"
	"longheng.io/server/internal/modules/dnf/party"
)

func (r *currentPartyUDPRelay) closePVPRoom(id uint16) {
	if r == nil {
		return
	}
	r.mu.Lock()
	var legs []*currentPartyUDPRelayLeg
	for k, v := range r.legs {
		if k.pvp && k.partyID == id {
			delete(r.legs, k)
			delete(r.usedPorts, int(v.port))
			legs = append(legs, v)
		}
	}
	r.mu.Unlock()
	for _, v := range legs {
		_ = v.conn.Close()
	}
	r.logLegClosure(legs, "决斗房间退出")
}
func (s *Service) syncPVPRelay(room *currentPVPRoom, peers []*currentPVPMember) error {
	r := s.partyUDPRelay
	if r == nil || !r.Enabled() || len(peers) < 2 {
		return nil
	}
	bindings := make([]currentPartyUDPRelayBinding, 0, len(peers))
	for _, p := range peers {
		address := currentPVPObservedIPv4(p.session)
		if address == [4]byte{} {
			return fmt.Errorf("决斗中继缺少真实TCP地址")
		}
		bindings = append(bindings, currentPartyUDPRelayBinding{characterID: p.characterID, generation: p.session.characterGeneration, address: address, advertisedPort: currentPartyPeerPort(p.session)})
	}
	if !r.syncGroup(true, room.id, bindings) {
		return fmt.Errorf("决斗中继端口不足或成员失效")
	}
	return nil
}
func (s *Service) pvpEndpointBody(room *currentPVPRoom, target, actor *currentPVPMember, ep party.PeerEndpoint) []byte {
	r := s.partyUDPRelay
	if r != nil && r.Enabled() {
		r.mu.Lock()
		leg := r.legs[currentPartyUDPRelayLegKey{pvp: true, partyID: room.id, ownerID: target.characterID, peerID: actor.characterID}]
		if leg != nil {
			ep.IPv4 = r.advertise
			ep.Port = leg.port
		}
		r.mu.Unlock()
	}
	return s.buildPartyPeerEndpointInfoBody([]party.PeerEndpoint{ep})
}

func (s *Service) refreshPVPRelayAfterLeave(room *currentPVPRoom) {
	if room == nil || s.partyUDPRelay == nil {
		return
	}
	peers := []*currentPVPMember{}
	for _, p := range room.members {
		if p != nil {
			peers = append(peers, p)
		}
	}
	if len(peers) < 2 {
		s.partyUDPRelay.closePVPRoom(room.id)
		return
	}
	if err := s.syncPVPRelay(room, peers); err != nil {
		s.logGameEvent(nil, "决斗中继离房同步失败", "room_id", room.id, "error", err)
	}
}
