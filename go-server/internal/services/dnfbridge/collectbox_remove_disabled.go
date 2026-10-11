package dnfbridge

// The user explicitly disabled withdrawals. Captured op1023 is u8 box,
// u32 item. Keep the registry unchanged and restore its complete UI state.
// EXE 1438F50 reads ordinary failure result/error bytes.
func (s *Service) handleCurrentCollectboxRemoveDisabled(session *gameSession, body []byte) error {
	if session == nil || session.selectedCharacterID == 0 || len(body) != 5 {
		return nil
	}
	catalog, ok := s.currentCollectboxCatalog()
	if !ok {
		return nil
	}
	box, ok := catalog.Box(int32(body[0]))
	if !ok {
		return nil
	}
	s.logGameEvent(session, "game-collectbox-remove-disabled", "char_id", session.selectedCharacterID, "box_index", box.Index, "reason", "user_disabled_withdrawal")
	if err := s.sendGameUpperRawClass(session, currentCollectboxRemoveItemMsgID, []byte{0, 0x12}, 1); err != nil {
		return err
	}
	return s.sendCurrentCollectboxStateWithItems(session, box, session.collectboxSelectedIndex == box.Index,
		"collectbox_withdrawal_disabled", s.currentCollectboxRegisteredItemIDs(session, box))
}
