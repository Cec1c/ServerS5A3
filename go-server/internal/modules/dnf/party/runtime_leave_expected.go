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
	// 单变量实验（默认 off）：副本内 2 -> 1 同样解散。
	// 否则 dungeon_party_member_exit.go 会把 1 人 op9 发给图里最后一个人，
	// 第二人的血条残留在房间里，最后一人过不了门、回城后还卡在残影队伍里。
	if disbandOnSingleton() && p.count() == 1 {
		for _, survivor := range p.membersBySlot() {
			delete(m.byUser, survivor.UserID)
		}
		delete(m.parties, p.id)
		disbanded := previous
		return RuntimePartyResult{OK: true, Previous: &previous, TargetUser: userID, Disbanded: true, Retired: &disbanded}
	}
	// 不重排槽位：客户端 .rprior 会跳过 0xFFFF 空槽，但要求每个非空槽位
	// memberUsers[esi] 的槽位下标与记录里的 esi 完全一致；在副本内把 2P/3P
	// 前移会触发客户端整队重绑，表现为血槽重排、互相看不见。
	// 因此 1P 中途退图后保留稀疏槽位（[0xFFFF, 2P, 3P]），只移交 leader。
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
