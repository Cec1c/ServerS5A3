package dnfbridge

// Roster-only changes share the projection revision, but never drive dungeon
// settlement, reward flow, phase start or reconnect reset. Queue after NOTI591.
func (s *Service) queueCurrentRaidRosterProgress(state runtimeRaidState) {
	if s.options.currentClientVersion != "official-24.9" || !state.Started || state.Battle == nil {
		return
	}
	for _, member := range state.Members {
		identity, found := s.onlineGameSessionCharacterSnapshot(member.CharID)
		if !found {
			continue
		}
		s.requestCurrentRaidRefreshKind(identity, state, 1)
	}
}
