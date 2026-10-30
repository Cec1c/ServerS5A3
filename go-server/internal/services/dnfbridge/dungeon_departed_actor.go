package dnfbridge

import "longheng.io/server/internal/modules/dnf/alignedcmd"

// NOTI9 kind3 removes a party record and its links; it does not erase the
// active player manager entry. Current NOTI6 (2035d10 -> 2970de0) consumes a
// u16 key and removes that active actor. Call only for a departed member of
// the caller's exact dungeon-instance snapshot, after the survivor roster has
// consumed the old manager slots and retired its missing network peers.
func (s *Service) sendDungeonDepartedActorRemoval(receiver *gameSession, departingID uint16, source string, retained ...alignedcmd.PartyState) error {
	if s == nil || s.options.currentClientVersion != "official-24.9" || departingID == 0 || departingID == 0xffff {
		return nil
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(receiver)
	if !ok || identity.character == departingID || !s.boundGameSessionCharacterCurrent(identity) {
		return nil
	}
	var expected *alignedcmd.PartyState
	if len(retained) > 0 {
		expected = &retained[0]
	}
	sent, err := s.sendRetiredPartyPacket(receiver, currentTownUserLeaveMsgID, buildTownUserLeaveBody(departingID), expected, nil, true)
	if err != nil {
		return err
	}
	if !sent {
		return nil
	}
	s.logGameEvent(receiver, "game-dungeon-departed-active-actor-remove",
		"source", source, "receiver_char_id", identity.character, "departing_char_id", departingID,
		"msg_id", currentTownUserLeaveMsgID, "classification", 0, "body_len", 2,
		"body_source", "v140_native_2035d10_u16_key_to_2970de0_active_manager_erase")
	return nil
}
