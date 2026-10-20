package dnfbridge

// Raid map rewards belong to the raid phase/card owner. The current client
// returns from a completed raid map directly through op72 or op42 without
// sending the ordinary op69/70/71 card handshake. Require the accepted clear
// journal rather than manufacturing ordinary cards or their asset grants.
// Caller holds the reporting session's dungeon mutex.
func (s *Service) currentRaidCompletedMapExitReady(session *gameSession, runtime *runtimeDungeonState) bool {
	return s.currentRaidCompletedMapExitReceiptReady(session, runtime, false)
}

// Only town return may use a timer-invalidated terminal run receipt.
func (s *Service) currentRaidCompletedMapTownReturnReady(session *gameSession, runtime *runtimeDungeonState) bool {
	return s.currentRaidCompletedMapExitReceiptReady(session, runtime, true)
}

func (s *Service) currentRaidCompletedMapExitReceiptReady(session *gameSession, runtime *runtimeDungeonState, allowExpired bool) bool {
	if s.options.currentClientVersion != "official-24.9" || !s.currentRaidAuthenticated(session) ||
		runtime == nil || runtime.raidEntry == nil || session.dungeon.runtime != runtime ||
		!completedDungeonSettlementResultReady(runtime, session.selectedCharacterID) ||
		runtime.Dungeon.ID <= 0 || runtime.Dungeon.ID > int64(^uint32(0)) {
		return false
	}
	e := runtime.raidEntry
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	if state == nil || !state.Started || state.Battle == nil || state.RaidKey != e.RaidKey {
		return false
	}
	b := state.Battle
	if b.InstanceID != e.InstanceID || e.RunID == "" || b.ChannelID != e.ChannelID ||
		b.ChannelType != e.ChannelType || session.channel.ID != e.ChannelID || session.channel.Type != e.ChannelType ||
		b.MemberAccounts[session.selectedCharacterID] == "" || b.MemberAccounts[session.selectedCharacterID] != runtime.AccountID ||
		runtime.AccountID != s.accountIDForSession(session) {
		return false
	}
	run, found := b.ClearedRuns[e.RunID]
	// An accepted return-only receipt remains owned by this run when the same
	// instance advances to phase two. Its first-phase attempt must not be
	// compared with the new phase's reset epoch. Retry/selector never use it.
	expiredPhaseCurrent := b.Anton != nil && e.Phase == 0 &&
		(b.Phase == 0 && b.Anton.Phase() == 0 && b.Anton.Epoch() > e.Attempt ||
			b.Phase == 1 && b.Anton.Phase() == 1)
	if !found && allowExpired && b.Mode == 1 && !b.Single && expiredPhaseCurrent {
		run, found = b.ExpiredCompletedRuns[e.RunID]
	}
	if !found && allowExpired && b.Mode == 1 && !b.Single && currentAntonMapReturnOwnerMatches(session, runtime) && runtime.antonMapReturnOwner.Completed {
		return true
	}
	if !found || run.DungeonID != uint32(runtime.Dungeon.ID) || run.Phase != e.Phase || run.Attempt != e.Attempt ||
		!sameRaidParticipants(run.Participants, e.Participants) {
		return false
	}
	for _, id := range run.Participants {
		if id == session.selectedCharacterID {
			return true
		}
	}
	return false
}

// Caller holds session.dungeon.mu. Raid maps use the accepted clear journal;
// ordinary maps keep their committed card/reward boundary.
func (s *Service) currentCompletedDungeonActionReady(session *gameSession, runtime *runtimeDungeonState) bool {
	if session == nil {
		return false
	}
	if runtime != nil && runtime.raidEntry != nil {
		return s.currentRaidCompletedMapExitReady(session, runtime)
	}
	return completedDungeonSettlementExitReady(runtime, session.selectedCharacterID)
}
