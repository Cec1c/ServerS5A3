package dnfbridge

// CMD1345/u32(2) requests the native daily/season mission lists. NOTI1283
// contains two u32 counts followed by (u32 id, u32 progress, u8 claimed)
// records. The native receiver fills absent records from etc/agitwar.etc.
const currentGuildWarMissionsNotification uint16 = 1283

func (s *Service) handleCurrentGuildWarMissionQuery(session *gameSession, request gameplayRequest) error {
	if !requireDefaultGameplayClassification(s, session, request, "game-guild-war-mission-class-rejected", "command_class_mismatch") ||
		!isCurrentLegacyTownSceneReadyAck(request.Body) {
		return nil
	}
	return s.sendCurrentGuildWarMissions(session)
}

func (s *Service) sendCurrentGuildWarMissions(session *gameSession) error {
	if !s.options.guildEssenceEnabled {
		return nil
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return nil
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return nil
	}
	defer release()
	actor, err := s.currentGuildOwnedSelectedCharacter(session)
	if err != nil {
		return nil
	}
	_, registry, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return err
	}
	_, member, found := registry.GuildForCharacter(actor.CharacterID)
	if !found || member.AccountID != actor.AccountID || actor.Stats["delete_flag"] != 0 {
		return nil
	}
	if !session.townMu.TryLock() {
		return nil
	}
	defer session.townMu.Unlock()
	if session.townSceneReadyCharacterID != identity.character || !s.boundGameSessionCharacterCurrent(identity) {
		return nil
	}
	// Formal war settlement is not implemented yet: there are no committed
	// progress/claim rows. Two empty collections expose the real PVF catalog
	// at zero progress; practice and ordinary clears must not fabricate it.
	return s.sendGameUpperRawClass(session, currentGuildWarMissionsNotification, make([]byte, 8), 0)
}
