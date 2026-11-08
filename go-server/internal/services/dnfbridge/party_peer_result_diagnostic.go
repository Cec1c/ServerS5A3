package dnfbridge

import "encoding/hex"

// The current incident uses legacy class1/op97, not the upper observation route.
// Its body semantics remain unproven. Observe without claiming the command,
// replying, changing membership, or treating relay traffic as native readiness.
func (s *Service) observeCurrentLegacyPeerResult(p *gameSession, cmd byte, typ uint16, body []byte) {
	if s.options.currentClientVersion != "official-24.9" || p == nil || cmd != 1 || typ != 97 || p.selectedCharacterID == 0 || p.peerResultDiagnosticCount >= 64 {
		return
	}
	p.peerResultDiagnosticCount++
	party := runtimePartyStateSnapshot(p)
	prefix := body[:min(len(body), 16)]
	s.logInfo("game-party-legacy-peer-result-observed",
		"conn_id", p.connID, "channel_id", p.channel.ID,
		"char_id", p.selectedCharacterID, "character_generation", p.characterGeneration,
		"party_id", party.PartyID, "party_member_count", len(runtimePartyMembers(party)),
		"cmd", cmd, "type", typ, "body_len", len(body), "body_prefix_hex", hex.EncodeToString(prefix),
		"body_truncated", len(body) > len(prefix), "observation", p.peerResultDiagnosticCount,
		"layout", "unproven", "behavior", "unchanged_fallback_no_readiness_inference")
}
