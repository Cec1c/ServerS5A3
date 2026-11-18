package dnfbridge

import "longheng.io/server/internal/modules/dnf/worldmap"

// Only a committed Fiend War clear may close a retained native death for free.
// The admission and clear journal stay authoritative after the encounter is
// removed. Ordinary settlement and active combat keep their own life rules.
// Caller owns this session's dungeon mutex; no transport runs under raidMu.
func (s *Service) currentFiendWarCompletedLifeOwner(p *gameSession, r *runtimeDungeonState) bool {
	if p == nil || r == nil || r.Session == nil || p.dungeon.runtime != r ||
		!isCurrentFiendWarChannel(p) || !s.currentRaidAuthenticated(p) ||
		!dungeonRuntimeOwnsCharacter(r, p.selectedCharacterID) || r.townReturnPending ||
		r.lifecycleToken == 0 || r.lifecycleToken != p.dungeon.runToken ||
		r.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted ||
		!completedDungeonSettlementEntryReady(r, p.selectedCharacterID) ||
		!currentDungeonSettlementCompletionReady(r, worldmap.DungeonRunCompleted) || r.raidEntry == nil {
		return false
	}
	e := r.raidEntry
	if e.ChannelType != 42 || p.channel.ID != e.ChannelID || e.RunID == "" || e.Attempt == 0 {
		return false
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(p.selectedCharacterID)
	if state == nil || !state.Started || state.RaidKey != e.RaidKey || state.Battle == nil {
		return false
	}
	b := state.Battle
	if b.Mode != 3 || b.InstanceID != e.InstanceID || b.Phase != e.Phase || b.TimedOut ||
		b.ChannelID != e.ChannelID || b.ChannelType != e.ChannelType ||
		b.MemberAccounts[p.selectedCharacterID] == "" || b.MemberAccounts[p.selectedCharacterID] != r.AccountID ||
		r.AccountID != s.accountIDForSession(p) {
		return false
	}
	cleared, ok := b.ClearedRuns[e.RunID]
	if !ok || int64(cleared.DungeonID) != r.Dungeon.ID || cleared.Phase != e.Phase ||
		cleared.Attempt != e.Attempt || !sameRaidParticipants(cleared.Participants, e.Participants) {
		return false
	}
	for _, id := range cleared.Participants {
		if id == p.selectedCharacterID {
			return true
		}
	}
	return false
}

func (s *Service) restoreCurrentFiendWarCompletedLifeLocked(p *gameSession, r *runtimeDungeonState, lateDeath bool) error {
	if !s.currentFiendWarCompletedLifeOwner(p, r) || !lateDeath && !r.deathStateSent && !r.deathReturnWaiting {
		return nil
	}
	if err := s.sendGameUpperRawClass(p, currentDungeonDeathStateMsgID,
		buildCurrentDungeonDeathStateBody(currentSceneActorObjectKey(p.selectedCharacterID), currentDungeonReviveStateType, 0), 0); err != nil {
		return err
	}
	s.cancelCurrentDungeonDeathReturnLocked(p, r, "fiendwar_committed_clear_life")
	r.deathStateSent = false
	markPartyDungeonLife(r, p.selectedCharacterID, false)
	r.fiendWarClearLifeProjectionPending = r.partyInstance != nil
	// No coin transaction or raid death accounting: the accepted deaths remain
	// in the frozen result journal. This only closes the completed scene actor.
	return nil
}

// The owner event drains this after releasing its dungeon mutex. No extra
// goroutine or fallible cross-owner enqueue is needed for the party projection.
func (s *Service) flushCurrentFiendWarClearLife(p *gameSession) {
	if p == nil {
		return
	}
	p.dungeon.mu.Lock()
	r := p.dungeon.runtime
	ready := r != nil && r.fiendWarClearLifeProjectionPending && s.currentFiendWarCompletedLifeOwner(p, r)
	if r != nil {
		r.fiendWarClearLifeProjectionPending = false
	}
	p.dungeon.mu.Unlock()
	if ready {
		if err := s.broadcastPartyDungeonLife(p, r, true); err != nil {
			s.logGameEvent(p, "game-fiendwar-clear-life-projection-failed", "error", err)
		}
	}
}
