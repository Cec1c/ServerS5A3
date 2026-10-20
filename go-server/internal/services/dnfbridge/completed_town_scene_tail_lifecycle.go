package dnfbridge

// Server-tail retirement only; never callback, actor or reward authorization.
// Access holds townMu. Pointer identity separates consecutive return routes.
type currentCompletedTownSceneTailOwner struct {
	characterID  uint16
	channelID    int
	serverID     int
	ownerContext byte
	connectionID string
}

func currentCompletedTownSceneTailOwnerMatchesLocked(s *gameSession, o *currentCompletedTownSceneTailOwner) bool {
	return s != nil && o != nil && s.completedTownSceneTailOwner == o && o.characterID != 0 &&
		s.selectedCharacterID == o.characterID && s.channel.ID == o.channelID &&
		s.channel.ServerID == o.serverID && s.connID == o.connectionID && currentTownActorOwnerContext(s) == o.ownerContext
}
func armCurrentCompletedTownSceneTailOwnerLocked(s *gameSession, characterID uint16) {
	if s == nil || characterID == 0 || s.selectedCharacterID != characterID ||
		s.initialTownRouteCharacterID != characterID || s.initialTownRouteStage < currentInitialTownRoutePlayerStateSent ||
		!s.sceneBootstrapTailDeferred || s.sceneBootstrapTailSent || s.initialTownLegacySceneReadyAccepted {
		return
	}
	s.completedTownSceneTailOwner = &currentCompletedTownSceneTailOwner{characterID: characterID,
		channelID: s.channel.ID, serverID: s.channel.ServerID, connectionID: s.connID, ownerContext: currentTownActorOwnerContext(s)}
}
func captureCurrentCompletedTownSceneTailOwnerLocked(s *gameSession) *currentCompletedTownSceneTailOwner {
	if s == nil {
		return nil
	}
	o := s.completedTownSceneTailOwner
	if !currentCompletedTownSceneTailOwnerMatchesLocked(s, o) || s.initialTownRouteCharacterID != o.characterID ||
		s.initialTownRouteStage < currentInitialTownRoutePlayerStateSent || !s.sceneBootstrapTailDeferred ||
		s.sceneBootstrapTailSent || s.initialTownLegacySceneReadyAccepted {
		return nil
	}
	return o
}
func finishCurrentCompletedTownSceneTailAfterAreaCommitLocked(s *gameSession, o *currentCompletedTownSceneTailOwner) {
	if !currentCompletedTownSceneTailOwnerMatchesLocked(s, o) || s.townSceneReadyCharacterID != o.characterID ||
		s.initialTownRouteCharacterID != 0 || s.initialTownRouteStage != currentInitialTownRouteIdle ||
		!s.sceneBootstrapTailDeferred || s.sceneBootstrapTailSent || s.initialTownLegacySceneReadyAccepted {
		return
	}
	s.sceneBootstrapTailDeferred = false
	resetCurrentDeferredSelectSceneTailProgress(s)
	s.completedTownSceneTailOwner = nil
}
