package dnfbridge

// A shared room kill advances each present member on that member's own event
// queue. Capture the room and identity: a reused object key after moving rooms
// or changing characters must not become a new kill. Forced boss cleanup has
// no combat-death receipt and cannot produce quest progress.
func (s *Service) dispatchCurrentDungeonHuntDeathToPeersLocked(source *gameSession, runtime *runtimeDungeonState, objectKey uint32) {
	if s == nil || source == nil || runtime == nil || runtime.partyInstance == nil || !runtime.Room.HasCombatDeath(objectKey) {
		return
	}
	room, instanceID := runtime.Room, runtime.partyInstance.id
	for _, peer := range s.partyDungeonInstancePeers(runtime, source.selectedCharacterID) {
		identity, bound := s.boundGameSessionCharacterSnapshot(peer.Session)
		if !bound || identity.character != peer.CharacterID {
			continue
		}
		run := func() error {
			peer.Session.dungeon.mu.Lock()
			defer peer.Session.dungeon.mu.Unlock()
			if !s.boundGameSessionCharacterCurrent(identity) {
				return nil
			}
			current := peer.Session.dungeon.runtime
			if current == nil || current.Session != peer.Scene || current.Room != room || current.partyInstance == nil || current.partyInstance.id != instanceID {
				return nil
			}
			return s.persistCurrentDungeonHuntMonsterDeathLocked(peer.Session, current, objectKey)
		}
		if err := s.tryPostGameSessionCharacterEvent(peer.Session, "party-dungeon-hunt-progress", identity.character, identity.generation, run); err != nil {
			s.logGameEvent(source, "game-dungeon-party-hunt-progress-deferred", "peer_char_id", peer.CharacterID, "object_key", objectKey, "error", err)
		}
	}
}
