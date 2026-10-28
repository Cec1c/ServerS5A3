package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/party"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func (s *Service) currentTownPVPMode(mode byte) bool {
	return s.options.currentClientVersion == "official-24.9" && (mode == 4 || mode == 13)
}

// V179 menu keys 25/180 send modes 4/13 for street/guild-arena challenges.
// They have their own 443/444 dialogs and battle lifecycle. The ordinary
// RuntimePartyManager cannot accept them: doing so creates a dungeon party.
// Until that gameplay owner exists, reject before recording or acknowledging
// an invitation. ACK10 reads mode and a u32 even on failure.
func (s *Service) rejectCurrentTownPVPRequest(session *gameSession, typ uint16, req party.RequestPeerRequest) error {
	s.logGameEvent(session, "game-town-pvp-request-unavailable", "mode", req.Mode,
		"target_id", req.TargetID, "reason", "town_pvp_gameplay_owner_unavailable")
	return s.sendCurrentTownPVPFailure(session, typ, 3, req.Mode)
}

// Retire only an authenticated, pending request left by the previous route.
// V179 error 133 closes the target's 0x155 dialog and the inviter's 0x156
// dialog. Accept, reject, cancel and timeout all leave ordinary parties intact.
func (s *Service) rejectCurrentTownPVPResponse(session *gameSession, typ uint16, req party.ResponsePeerRequest) error {
	acceptor, acceptorBound := s.boundGameSessionCharacterSnapshot(session)
	inviter, inviterBound := s.onlineGameSessionCharacterSnapshot(req.TargetID)
	if !acceptorBound || !inviterBound || inviter.session == nil {
		return nil
	}
	manager := s.runtimePartyManagerForService()
	if manager == nil {
		return nil
	}
	if _, consumed := manager.ConsumePeerRequest(acceptor.character, acceptor.generation,
		inviter.character, inviter.generation, req.Mode); !consumed {
		return nil
	}
	s.logGameEvent(session, "game-town-pvp-pending-request-retired", "mode", req.Mode,
		"target_id", req.TargetID, "response", req.Value,
		"reason", "town_pvp_gameplay_owner_unavailable")
	if err := s.sendCurrentTownPVPFailure(session, typ, 133, req.Mode); err != nil {
		return err
	}
	return s.sendCurrentTownPVPFailure(inviter.session, 10, 133, req.Mode)
}

func (s *Service) sendCurrentTownPVPFailure(session *gameSession, typ uint16, code, mode byte) error {
	body := []byte{0, code, mode}
	if typ == 10 {
		body = append(body, 0, 0, 0, 0)
	}
	return s.sendGameUpperRawClassCodec(session, typ, body, dnfproto.DefaultChannelClassification, true)
}
