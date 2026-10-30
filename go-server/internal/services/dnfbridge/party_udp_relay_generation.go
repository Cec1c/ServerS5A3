package dnfbridge

import (
	"net"
	"time"
)

const currentPartyRelayWriteTimeout = 50 * time.Millisecond

// Pair ownership is checked at the operation itself. Close/rejoin may retain
// the numeric keys while replacing the sockets and both character bindings.
func (r *currentPartyUDPRelay) pairCurrentLocked(leg, opposite *currentPartyUDPRelayLeg) bool {
	return !r.closed && leg != nil && opposite != nil &&
		r.legs[leg.key] == leg && r.legs[opposite.key] == opposite &&
		leg.key.pvp == opposite.key.pvp && leg.key.partyID == opposite.key.partyID &&
		leg.key.ownerID == opposite.key.peerID && leg.key.peerID == opposite.key.ownerID
}

// Keep the final pointer check and UDP write in the same topology critical
// section, including writes through a long-lived shared probe socket. No
// logger or Service callback runs under this lock.
func (r *currentPartyUDPRelay) writeCurrentPairDatagram(leg, opposite *currentPartyUDPRelayLeg, conn *net.UDPConn, payload []byte, target *net.UDPAddr, observed ...bool) (int, error) {
	if r == nil || conn == nil || target == nil {
		return 0, net.ErrClosed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.pairCurrentLocked(leg, opposite) {
		return 0, net.ErrClosed
	}
	if len(observed) > 0 && observed[0] && r.targetBelongsToOtherMemberLocked(opposite, target) {
		return 0, net.ErrClosed
	}
	// A full OS send buffer must not hold the lifecycle mutex indefinitely.
	if err := conn.SetWriteDeadline(time.Now().Add(currentPartyRelayWriteTimeout)); err != nil {
		return 0, err
	}
	return conn.WriteToUDP(payload, target)
}

// A shared public IP does not make another active character's learned port a
// candidate for this member. Check at the write, so learning a competing port
// after candidate collection also prevents cross-party delivery.
func (r *currentPartyUDPRelay) targetBelongsToOtherMemberLocked(opposite *currentPartyUDPRelayLeg, target *net.UDPAddr) bool {
	for _, current := range r.legs {
		if current.key.ownerID == opposite.key.ownerID && current.binding == opposite.binding {
			continue
		}
		known := current.learnedEndpointSnapshot()
		if known != nil && known.Port == target.Port && known.IP.Equal(target.IP) {
			return true
		}
	}
	return false
}

func (r *currentPartyUDPRelay) writeCurrentLegDatagram(leg *currentPartyUDPRelayLeg, payload []byte, target *net.UDPAddr) (int, error) {
	if r == nil || leg == nil || leg.conn == nil || target == nil {
		return 0, net.ErrClosed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.legs[leg.key] != leg {
		return 0, net.ErrClosed
	}
	if err := leg.conn.SetWriteDeadline(time.Now().Add(currentPartyRelayWriteTimeout)); err != nil {
		return 0, err
	}
	return leg.conn.WriteToUDP(payload, target)
}
