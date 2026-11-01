package party

// LeaveDungeonExpected preserves the active instance's party identity and fixed
// slots when its leader leaves. A remaining singleton still owns its dungeon.
func (m *RuntimePartyManager) LeaveDungeonExpected(userID uint16, generation uint64, partyID uint16) RuntimePartyResult {
	if m == nil || userID == 0 || generation == 0 || partyID == 0 {
		return runtimePartyFailure("invalid_leave")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, member := m.partyMemberLocked(userID)
	if p == nil || member == nil {
		return runtimePartyFailure("not_in_party")
	}
	if p.id != partyID {
		return runtimePartyFailure("stale_party")
	}
	if member.SessionGeneration != generation {
		return runtimePartyFailure("stale_session")
	}
	previous := snapshotRuntimeParty(p)
	wasLeader := p.leader == userID
	p.remove(userID)
	delete(m.byUser, userID)
	m.clearInvitesForLocked(userID, generation)
	if p.count() == 0 {
		delete(m.parties, p.id)
		return RuntimePartyResult{OK: true, Previous: &previous, TargetUser: userID, Disbanded: true}
	}
	if wasLeader {
		p.leader = p.membersBySlot()[0].UserID
		p.settings.UserID, p.settings.IsLeader = p.leader, true
	}
	result := runtimePartySuccess(p, userID)
	result.Previous, result.LeaderMoved = &previous, wasLeader
	return result
}

// LeaveExpected checks the party generation and session in the same critical
// section as the mutation. A snapshot check followed by Leave would allow a
// concurrent team replacement to remove the member from the new party.
func (m *RuntimePartyManager) LeaveExpected(userID uint16, generation uint64, partyID uint16) RuntimePartyResult {
	if m == nil || userID == 0 || generation == 0 || partyID == 0 {
		return runtimePartyFailure("invalid_leave")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, member := m.partyMemberLocked(userID)
	if p == nil || member == nil {
		return runtimePartyFailure("not_in_party")
	}
	if p.id != partyID {
		return runtimePartyFailure("stale_party")
	}
	return m.leaveLocked(userID, generation, false)
}
