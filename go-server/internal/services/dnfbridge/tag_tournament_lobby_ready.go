package dnfbridge

// 完成局已写完原角色mode0/mode1/24并发布休息室位置时，可以处理队伍与
// 再次挑战。旧登录尾包仍等自己的真实回调，不重放角色对象或伪造1345。
func currentTagTournamentLobbyInteractionReady(p *gameSession) bool {
	if currentTagTournamentLobbyReady(p) {
		return true
	}
	if p == nil || p.selectedCharacterID == 0 || p.dungeon.runtime != nil || p.confirmedDungeonReturnStatePending {
		return false
	}
	p.townMu.Lock()
	defer p.townMu.Unlock()
	position := p.townPositionSnapshot
	return p.sceneBootstrapTailDeferred && !p.sceneBootstrapTailSent &&
		p.initialTownRouteCharacterID == p.selectedCharacterID && p.initialTownRouteStage >= currentInitialTownRoutePlayerStateSent &&
		p.townSceneReadyCharacterID == p.selectedCharacterID && position.CharacterID == p.selectedCharacterID &&
		position.PositionValid && position.TownID == 48
}

// 完成局回城在真实城镇确认后会退休旧登录尾包，不能要求它重新变为sent。
// 只承接已提交的位置、角色及HUD世代；待决回城另由原生城镇请求确认。
func currentTagTournamentLobbyReady(p *gameSession) bool {
	if p == nil || p.dungeon.runtime != nil || p.confirmedDungeonReturnStatePending {
		return false
	}
	if p.sceneBootstrapTailSent && !p.sceneBootstrapTailDeferred {
		return true
	}
	if p.sceneBootstrapTailDeferred || !currentPartyTownReturnSceneReady(p) {
		return false
	}
	p.townMu.Lock()
	retired := p.initialTownRouteCharacterID == 0 && p.initialTownRouteStage == currentInitialTownRouteIdle && p.completedTownSceneTailOwner == nil
	p.townMu.Unlock()
	p.townPostTransition.mu.Lock()
	complete := p.townPostTransition.stage == currentTownPostTransitionIdle || p.townPostTransition.characterID == p.selectedCharacterID && p.townPostTransition.stage == currentTownPostTransitionComplete
	p.townPostTransition.mu.Unlock()
	return retired && complete
}
