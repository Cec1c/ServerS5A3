package dnfbridge

// NOTI9 kind3 removes a party record and its links; it does not erase the
// active player manager entry. Current NOTI6 (2035d10 -> 2970de0) consumes a
// u16 key and removes that active actor. Call only for a departed member of
// the caller's exact dungeon-instance snapshot, before the survivor roster.
func (s *Service) sendDungeonDepartedActorRemoval(receiver *gameSession, departingID uint16, source string) error {
	if s == nil || s.options.currentClientVersion != "official-24.9" || departingID == 0 || departingID == 0xffff {
		return nil
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(receiver)
	if !ok || identity.character == departingID || !s.boundGameSessionCharacterCurrent(identity) {
		return nil
	}
	if err := s.sendCurrentSceneFixedClass0Packet(receiver, currentTownUserLeaveMsgID, buildTownUserLeaveBody(departingID), "dungeon_departed_active_actor"); err != nil {
		return err
	}
	s.logGameEvent(receiver, "game-dungeon-departed-active-actor-remove",
		"source", source, "receiver_char_id", identity.character, "departing_char_id", departingID,
		"msg_id", currentTownUserLeaveMsgID, "classification", 0, "body_len", 2,
		"body_source", "v140_native_2035d10_u16_key_to_2970de0_active_manager_erase")
	return nil
}
