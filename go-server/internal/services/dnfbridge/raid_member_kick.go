package dnfbridge

// Current clients write their own UID for a voluntary exit and the clicked
// member's UID for a forced exit. Never reinterpret a rejected target as self.
func (s *Service) handleCurrentRaidMemberKick(requester *gameSession, op uint16, targetID uint16) error {
	if !s.currentRaidAuthenticated(requester) {
		return s.sendRaidCommandFailure(requester, op)
	}
	owner, bound := s.boundGameSessionCharacterSnapshot(requester)
	state, found := s.runtimeRaidForMember(owner.character)
	if !bound || !found || state.LeaderID != owner.character || !s.currentRaidAdmissionVisible(requester, state) || runtimeRaidMemberIndex(state, targetID) < 0 {
		return s.sendRaidCommandFailure(requester, op)
	}
	target, online := s.onlineGameSessionCharacterSnapshot(targetID)
	if !online || !s.currentRaidAdmissionVisible(target.session, state) || !s.currentRaidAuthenticated(target.session) {
		return s.sendRaidCommandFailure(requester, op)
	}
	wasWaiting := s.currentAntonWaitingAreaPresence(target.session)
	s.raidMu.Lock()
	live := s.runtimeRaidByMemberLocked(owner.character)
	if live == nil || live.RaidKey != state.RaidKey || live.LeaderID != owner.character || runtimeRaidMemberIndex(*live, targetID) < 0 || !s.boundGameSessionCharacterCurrent(owner) || !s.boundGameSessionCharacterCurrent(target) {
		s.raidMu.Unlock()
		return s.sendRaidCommandFailure(requester, op)
	}
	next, removed, disbanded := s.removeRuntimeRaidMemberLocked(targetID)
	var snapshot runtimeRaidState
	if next != nil {
		snapshot = cloneRuntimeRaidState(*next)
	}
	var others []onlinePlayerInfo
	if removed && s.onlinePlayers != nil {
		if p, ok := s.onlinePlayers.PlayerForCharacter(targetID); ok && p.Instance != 0 && s.currentAntonWaitingArea(p.TownID, p.AreaID) {
			_, others = s.onlinePlayers.LeaveArea(targetID)
		}
	}
	s.raidMu.Unlock()
	if !removed {
		return s.sendRaidCommandFailure(requester, op)
	}
	if len(others) > 0 {
		s.broadcastTownPlayerLeave(targetID, others)
	}
	// Do not send success ACK665 to the leader: its native receiver clears
	// membership unconditionally. The authoritative roster confirms the kick.
	if err := s.finishOnlineRaidDeparture(target.session, op, snapshot, disbanded, wasWaiting, true); err != nil {
		// Removal has committed. A failing target socket must not close the
		// leader's event loop merely because the leader requested the kick.
		s.failRuntimeRaidRecipient(target.session, "forced_leave", err)
	}
	return nil
}
