package dnfbridge

// The pinned official writer sends 788 with a single output equipment ID;
// class1 callback 1fde960 consumes the common status/error prefix only.
// The paired client candidate must also admit 788 at its receive bitmap.
func epicBookGameplayModule() gameplayModuleDefinition {
	handle := func(s *Service, session *gameSession, r gameplayRequest) error {
		return s.handleCurrentEpicBookCraft(session, r.Body)
	}
	return gameplayModuleDefinition{Name: "epic-book", LegacyHandlers: map[uint16]gameplayHandler{788: handle}, UpperHandlers: map[uint16]gameplayHandler{788: defaultClassGameplayHandler("game-epic-book-class-blocked", "epic_book_command_class_mismatch", (*Service).handleCurrentEpicBookCraft)}}
}
