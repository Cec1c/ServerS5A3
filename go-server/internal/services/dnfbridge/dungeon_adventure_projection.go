package dnfbridge

// Only the UI projection is deferred. The ordinary EXP transaction has already
// persisted the account's adventure/capsule balances before this is called.
// Keep the large roster out of the TCP stream while this room is still fighting.
func (s *Service) sendCurrentMonsterAdventureInfo(session *gameSession, runtime *runtimeDungeonState, source string) error {
	if session == nil {
		return nil
	}
	if runtime != nil && runtime == session.dungeon.runtime && s.currentDungeonDeathBurstEnabled(session) {
		runtime.monsterAdventureInfoPending = true
		return nil
	}
	return s.sendCurrentAdventureInfoPushFromAccount(session, session.selectedCharacterID, source)
}

// Called after the complete death command/burst, including boss-forced visual
// deaths. Calling this from the EXP flush would put the roster ahead of those
// remaining death notifications again. The caller owns the dungeon lock or the
// session event; no timer or background goroutine reads this projection flag.
func (s *Service) flushCurrentMonsterAdventureInfo(session *gameSession, runtime *runtimeDungeonState) error {
	if session == nil || session.dungeonDeathBurst != nil || runtime == nil || runtime != session.dungeon.runtime || !runtime.monsterAdventureInfoPending ||
		!dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) || session.loginRevoked.Load() || runtime.Session == nil {
		return nil
	}
	scene, ok := runtime.Session.Scene()
	if !ok || !scene.Cleared {
		return nil
	}
	if err := s.sendCurrentAdventureInfoPushFromAccount(session, session.selectedCharacterID, "monster_experience_room_complete"); err != nil {
		return err
	}
	runtime.monsterAdventureInfoPending = false
	return nil
}
