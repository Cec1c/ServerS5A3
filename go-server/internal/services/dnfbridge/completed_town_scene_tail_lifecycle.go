package dnfbridge

const currentTownEnterSelectReasonCompletedReturnTailPending = "completed_dungeon_return_town_scene_tail_pending"

// Server-tail retirement only; never callback, actor or reward authorization.
// Access holds townMu. Pointer identity separates consecutive return routes.
type currentCompletedTownSceneTailOwner struct {
	characterID   uint16
	channelID     int
	serverID      int
	ownerContext  byte
	connectionID  string
	clientSettled bool
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

// currentCompletedTownReturnPendingSettlementLocked reports whether a
// completed-dungeon return has emitted its town route but the client has not
// yet acknowledged town landing. Caller holds townMu.
func currentCompletedTownReturnPendingSettlementLocked(s *gameSession) bool {
	if s == nil {
		return false
	}
	o := s.completedTownSceneTailOwner
	return o != nil && !o.clientSettled && currentCompletedTownSceneTailOwnerMatchesLocked(s, o)
}

func currentCompletedTownReturnPendingSettlement(s *gameSession) bool {
	if s == nil {
		return false
	}
	s.townMu.Lock()
	defer s.townMu.Unlock()
	return currentCompletedTownReturnPendingSettlementLocked(s)
}

func markCurrentCompletedTownReturnSettledLocked(s *gameSession) {
	if s == nil {
		return
	}
	if o := s.completedTownSceneTailOwner; o != nil && currentCompletedTownSceneTailOwnerMatchesLocked(s, o) {
		o.clientSettled = true
	}
}

func markCurrentCompletedTownReturnSettled(s *gameSession) {
	if s == nil {
		return
	}
	s.townMu.Lock()
	markCurrentCompletedTownReturnSettledLocked(s)
	s.townMu.Unlock()
}
