package dnfbridge

// The dungeon entry commits owner zero independently of the selected channel.
// The channel-alignment setting applies to town actors only: using it during
// an equipment redraw recreates a town actor inside the active dungeon.
func currentSelectedActorAppearanceOwnerContext(session *gameSession) byte {
	if session != nil && session.preDungeonContextPlayerStateSent {
		return currentSceneObjectContext
	}
	return currentTownActorOwnerContext(session)
}
