package dnfbridge

const currentGuildRefreshOpcode uint16 = 339

// V110's guild window requests 339/352 before the 1300/1301 queries. Refresh
// the existing NOTI70 model; do not invent a class1/339 acknowledgement or
// replay the login/membership-change notifications for an ordinary refresh.
func (s *Service) handleCurrentGuildRefresh(session *gameSession, request gameplayRequest) error {
	if !requireDefaultGameplayClassification(s, session, request, "game-guild-refresh-class-rejected", "command_class_mismatch") {
		return nil
	}
	if len(request.Body) != 0 {
		return nil
	}
	source, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return nil
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{source})
	if err != nil {
		return nil
	}
	defer release()
	actor, err := s.currentGuildOwnedSelectedCharacter(session)
	if err != nil {
		return nil
	}
	_, reg, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return err
	}
	g, member, found := reg.GuildForCharacter(actor.CharacterID)
	if !found || member.AccountID != actor.AccountID {
		return nil
	}
	if !session.townMu.TryLock() {
		return nil
	}
	defer session.townMu.Unlock()
	if session.townSceneReadyCharacterID != source.character || !s.boundGameSessionCharacterCurrent(source) {
		return nil
	}
	s.logGameEvent(session, "game-guild-refresh", "guild_id", g.ID, "members", len(g.Members))
	return s.sendGameUpperRawClass(session, 70, buildCurrentGuildManagementBody(g, len(s.currentGuildOnlineChannels(g, session)), s.gameplayNow(), member.Grade), 0)
}
