package dnfbridge

import "longheng.io/server/internal/modules/dnf/alignedcmd"

// The matched official client's NOTI9 kind2 reads memberCount directly after
// the ten-byte header, then four control bytes. Kind3 additionally announces
// DSTR 627 (disband); ordinary departures must use the empty kind2 roster.
func (s *Service) buildCurrentPartyDepartureBody(characterID uint16, owner byte) []byte {
	body := buildCurrentSceneOp9ActorRemovalBodyInContext(currentSceneActorObjectKey(characterID), owner, s.currentSceneActorRoute())
	body[9] = 2
	return append(body, 0, 0, 0, 0, 0)
}

func (s *Service) sendRuntimePartyDeparture(receiver *gameSession, previous alignedcmd.PartyState) error {
	if s.options.currentClientVersion != "official-24.9" {
		return s.sendRuntimePartyMemberRemoved(receiver, selectedCharacterID(receiver), alignedcmd.PartyState{})
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(receiver)
	if !bound {
		return nil
	}
	current := s.currentPartyRetirementGuard(receiver, nil)
	members := runtimePartyMembers(previous)
	// The native slot setter clears each former member's actor->party pointer.
	// A peer's empty record therefore also detaches the local actor. If that
	// packet comes first, NOTI9's subsequent self record sees no previous
	// local party and skips the HUD/network teardown branch. Retire the local
	// record first while its before/after identity is still observable.
	ordered := []uint16{identity.character}
	for _, member := range members {
		if member.UserID != identity.character {
			ordered = append(ordered, member.UserID)
		}
	}
	owners := []byte{currentSceneObjectContext}
	if channel := currentClientSelectedChannel(receiver); channel != currentSceneObjectContext {
		owners = append([]byte{channel}, owners...)
	}
	for _, id := range ordered {
		for _, owner := range owners {
			if id == 0 {
				continue
			}
			sent, err := s.sendRetiredPartyPacket(receiver, 9, s.buildCurrentPartyDepartureBody(id, owner), nil, current, false)
			if err != nil || !sent {
				return err
			}
		}
	}
	_, err := s.sendRetiredPartyPacket(receiver, 0x99, runtimePartyRealtimeInfoBody(alignedcmd.PartyState{}), nil, current, false)
	if err != nil {
		return err
	}
	if s.onlinePlayers == nil {
		return nil
	}
	for _, peer := range s.onlinePlayers.PeersInSameArea(identity.character) {
		peerIdentity, bound := s.boundGameSessionCharacterSnapshot(peer.Session)
		if !bound {
			continue
		}
		guard := func() bool { return current() && s.boundGameSessionCharacterCurrent(peerIdentity) }
		owner := currentClientSelectedChannel(peer.Session)
		if _, err := s.sendRetiredPartyPacket(peer.Session, 9, s.buildCurrentPartyDepartureBody(identity.character, owner), nil, guard, false); err != nil {
			return err
		}
	}
	return nil
}
