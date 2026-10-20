package dnfbridge

import "context"

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
		peer, id := identity.session, identity.character
		// Never synchronously wait for the manager's own event loop from its
		// inbound callback. Aggregate locks are released before queueing/IO.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			defer cancel()
			err := s.callGameSession(ctx, peer, "raid-live-roster-progress", func() error {
				if !s.boundGameSessionCharacterCurrent(identity) || !s.currentRaidAuthenticated(peer) {
					return nil
				}
				s.raidMu.Lock()
				live := s.runtimeRaidByMemberLocked(id)
				valid := live != nil && live.Started && live.RaidKey == state.RaidKey && live.Battle != nil &&
					live.Battle.InstanceID == state.Battle.InstanceID && live.Battle.Phase == state.Battle.Phase &&
					live.Battle.ProgressVersion >= state.Battle.ProgressVersion &&
					live.Battle.ChannelID == peer.channel.ID && live.Battle.ChannelType == peer.channel.Type &&
					live.Battle.MemberAccounts[id] == s.accountIDForSession(peer)
				var latest runtimeRaidState
				if valid {
					latest = cloneRuntimeRaidState(*live)
				}
				s.raidMu.Unlock()
				if !valid {
					return nil
				}
				return s.sendCurrentRaidProgressProjection(peer, latest, false, false)
			})
			if err != nil {
				s.logGameEvent(peer, "game-raid-roster-progress-send-failed", "raid_key", state.RaidKey, "error", err)
			}
		}()
	}
}
