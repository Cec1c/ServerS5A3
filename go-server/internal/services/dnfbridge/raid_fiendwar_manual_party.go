package dnfbridge

import "longheng.io/server/internal/modules/dnf/dnfenum"

func (s *Service) currentFiendWarAutomaticPartyOnly(p *gameSession) bool {
	return s != nil && s.options.currentClientVersion == "official-24.9" && isCurrentFiendWarChannel(p)
}

// Eight raid seats are individual identities. Only encounter admission owns a
// temporary combat party; client lobby mutations must never change that graph.
// Peer requests are decoded separately to preserve trade and their mode field.
func (s *Service) rejectCurrentFiendWarManualPartyCommand(p *gameSession, typ uint16) (bool, error) {
	if !s.currentFiendWarAutomaticPartyOnly(p) {
		return false, nil
	}
	switch dnfenum.CmdPacket(typ) {
	case dnfenum.CmdPacketSetPartyInfo, dnfenum.CmdPacketEntryIntoParty,
		dnfenum.CmdPacketWalkoutPartyMember, dnfenum.CmdPacketChangeHost,
		dnfenum.CmdPacketChangePartyMemberPosition:
		return true, s.sendPartyCommandFailure(p, typ, partyCommandFailureGeneric)
	case dnfenum.CmdPacketEnterWarroom:
		// Existing directory reply has no result envelope. Do not project a party.
		return true, s.sendGameUpperRawClass(p, currentPartyDirectoryJoin, nil, 0)
	case dnfenum.CmdPacketRegisterQuickParty, dnfenum.CmdPacketDirectEntranceDungeonQuickParty:
		// Registration has no verified failure ACK. Consume without enrolling.
		return true, nil
	default:
		return false, nil
	}
}
