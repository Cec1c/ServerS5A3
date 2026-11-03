package dnfbridge

import "strings"

// An explicit channel exit is not a lost connection: the player has left the
// raid channel and must not retain a reconnect seat there.
func (s *Service) leaveNativeAntonRaidOnChannelExit(session *gameSession) {
	if s == nil || session == nil || !s.uses90NativeClient() || !s.currentRaidAuthenticated(session) {
		return
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return
	}
	s.raidMu.Lock()
	state := s.runtimeRaidByMemberLocked(identity.character)
	if state == nil || !state.CurrentLayout || state.RouteOrRaidType != 0 || !s.boundGameSessionCharacterCurrent(identity) || !s.raidPresenceContext(session, *state, strings.TrimSpace(session.accountID)) {
		s.raidMu.Unlock()
		return
	}
	key := state.RaidKey
	hadParticipation := false
	if state.Battle != nil {
		_, hadParticipation = state.Battle.Participation[identity.character]
	}
	delete(s.currentRaidDisconnects, identity.character)
	for id, pending := range s.currentRaidInvitations {
		if pending.inviter.character == identity.character || pending.recipient.character == identity.character {
			delete(s.currentRaidInvitations, id)
		}
	}
	for id, pending := range s.currentRaidApplications {
		if pending.applicant.character == identity.character || pending.leader.character == identity.character {
			delete(s.currentRaidApplications, id)
		}
	}
	remaining, removed, disbanded := s.removeRuntimeRaidMemberLocked(identity.character)
	if disbanded && state.Battle != nil {
		// Keep committed battle/reward work alive without retaining a roster seat.
		state.LeaderID = 0
		s.raids[key] = state
	}
	var snapshot runtimeRaidState
	if remaining != nil {
		snapshot = cloneRuntimeRaidState(*remaining)
	}
	s.raidMu.Unlock()
	if !removed {
		return
	}
	s.logGameEvent(session, "game-native90-anton-channel-exit-raid-leave", "raid_key", key, "character_id", identity.character, "disbanded", disbanded)
	if disbanded {
		return
	}
	for _, member := range snapshot.Members {
		peer, online := s.onlineGameSession(member.CharID)
		if online && s.currentRaidAdmissionVisible(peer, snapshot) {
			if err := s.sendRuntimeRaidMetadata(peer, snapshot, 2); err != nil {
				s.failRuntimeRaidRecipient(peer, "channel_exit_metadata", err)
			}
		}
	}
	if err := s.broadcastRuntimeRaidRefresh(snapshot); err != nil {
		s.logWarn("raid channel exit roster projection failed", "error", err)
	}
	if hadParticipation {
		s.queueCurrentRaidDungeonProgress(snapshot)
	}
}
