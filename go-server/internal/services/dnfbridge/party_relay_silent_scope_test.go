package dnfbridge

import "testing"

func TestSilentPeerKickRejectsRetiredPairAndPVPIdentifierCollision(t *testing.T) {
	for _, mode := range []string{"current", "retired", "pvp"} {
		t.Run(mode, func(t *testing.T) {
			pvp := mode == "pvp"
			leg := &currentPartyUDPRelayLeg{key: currentPartyUDPRelayLegKey{pvp: pvp, partyID: 77, ownerID: 101, peerID: 202}}
			opposite := &currentPartyUDPRelayLeg{key: currentPartyUDPRelayLegKey{pvp: pvp, partyID: 77, ownerID: 202, peerID: 101}}
			calls := 0
			r := &currentPartyUDPRelay{
				legs: map[currentPartyUDPRelayLegKey]*currentPartyUDPRelayLeg{leg.key: leg, opposite.key: opposite},
				onSilentPeer: func(partyID, memberID uint16) bool {
					calls++
					if partyID != 77 || memberID != 202 {
						t.Fatalf("unexpected roster target: %d/%d", partyID, memberID)
					}
					return true
				},
			}
			if mode == "retired" {
				r.legs[leg.key] = &currentPartyUDPRelayLeg{key: leg.key}
				r.legs[opposite.key] = &currentPartyUDPRelayLeg{key: opposite.key}
			}
			r.kickSilentPeer(leg, opposite, partyUDPRelaySilentPeerKickPhaseDelay)
			want := 0
			if mode == "current" {
				want = 1
			}
			if calls != want || opposite.kicks != uint32(want) {
				t.Fatalf("callback count=%d budget=%d want=%d", calls, opposite.kicks, want)
			}
		})
	}
}
