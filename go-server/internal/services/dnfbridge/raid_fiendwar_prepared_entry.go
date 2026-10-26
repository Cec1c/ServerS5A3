package dnfbridge

// A prepared FiendWar follower is continuing an already reserved encounter.
// AreaOpen governs new selections and can close when that same run reserves it.
// Only the exact committed runtime may use its frozen admission for this pass.
func (s *Service) currentPreparedDungeonSelectionAllowed(session *gameSession, dungeonID uint32, prepared *runtimePartyPreparedDungeonEntry) bool {
	if prepared == nil || !isCurrentFiendWarChannel(session) {
		return s.currentRaidDungeonSelectionAllowed(session, dungeonID)
	}
	if prepared.session != session || !prepared.committed || prepared.runtime == nil || prepared.runtime.raidEntry == nil || prepared.runtime.Dungeon.ID != int64(dungeonID) {
		return false
	}
	session.dungeon.mu.Lock()
	current := session.dungeon.runtime == prepared.runtime
	session.dungeon.mu.Unlock()
	if !current {
		return false
	}
	return s.validateCurrentRaidDungeonEntry(session, prepared.runtime) == nil
}
