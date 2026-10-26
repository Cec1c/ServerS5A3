package dnfbridge

import (
	"slices"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// The caller owns session.dungeon.mu. Only the final room of a configured
// FiendWar boss is gated. commit must contain memory mutations only: raidMu
// keeps a peer's window-end report from reopening the pool between the check
// and the mutation, and must never cover persistence or transport callbacks.
func (s *Service) commitCurrentFiendWarBossWindowLocked(session *gameSession, runtime *runtimeDungeonState, commit func() error) (bool, error) {
	apply := func() (bool, error) {
		if commit == nil {
			return true, nil
		}
		return true, commit()
	}
	if session == nil || runtime == nil || runtime.Session == nil ||
		(!isCurrentFiendWarChannel(session) && (runtime.raidEntry == nil || runtime.raidEntry.ChannelType != 42)) {
		return apply()
	}
	scene, present := runtime.Session.Scene()
	if !present || !scene.Boss || !runtime.BossSet || scene.Coordinate != runtime.BossCoordinate {
		return apply()
	}
	// Authentication may read the repository; do it before taking raidMu.
	authenticated := s.currentRaidAuthenticated(session)
	s.raidMu.Lock()
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	if state != nil && state.Battle != nil && state.Battle.FiendWar != nil {
		if area, found := state.Battle.FiendWar.AreaForDungeon(int(runtime.Dungeon.ID)); !found || !strings.HasSuffix(area.Kind, "boss") {
			s.raidMu.Unlock()
			return apply()
		}
	}
	reason := "fiendwar_boss_owner_invalid"
	allowed := false
	e := runtime.raidEntry
	if authenticated && session.dungeon.runtime == runtime && dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) &&
		!runtime.townReturnPending && !runtime.townReturnOp24Sent && session.postStartMapPlayerStateSent &&
		currentDungeonRoomOwnsScene(runtime, scene) && e != nil && e.RunID != "" && slices.Contains(e.Participants, session.selectedCharacterID) &&
		state != nil && state.Started && state.RaidKey == e.RaidKey && state.Battle != nil {
		b := state.Battle
		now := time.Now()
		if b.FiendWar != nil && b.InstanceID == e.InstanceID && b.Phase == e.Phase &&
			e.ChannelType == 42 && session.channel.Type == e.ChannelType && session.channel.ID == e.ChannelID &&
			b.ChannelID == e.ChannelID && b.MemberAccounts[session.selectedCharacterID] != "" &&
			b.MemberAccounts[session.selectedCharacterID] == runtime.AccountID && runtime.AccountID == s.accountIDForSession(session) &&
			!b.TimedOut && !now.Before(b.EnterAfter) && now.Before(b.Deadline) {
			// Successful clear removes the encounter and advances this side to
			// state10. Preserve only the exact already-committed run's replay.
			if cleared, exists := b.ClearedRuns[e.RunID]; exists {
				allowed = runtime.bossDieCheckAccepted && runtime.Session.Snapshot().Run.Status == worldmap.DungeonRunCompleted &&
					cleared.DungeonID == uint32(runtime.Dungeon.ID) && cleared.Phase == e.Phase && cleared.Attempt == e.Attempt &&
					sameRaidParticipants(cleared.Participants, e.Participants)
			} else if !b.PhaseCompleted && !b.FiendWar.Complete && !b.FiendWar.Failed && validateCurrentFiendWarDungeon(b, session.selectedCharacterID, runtime) == nil {
				reason = "fiendwar_boss_damage_window_closed"
				allowed = b.FiendWar.BossDamageWindowOpen(int(runtime.Dungeon.ID))
			}
		}
	}
	if !allowed {
		s.raidMu.Unlock()
		s.logGameEvent(session, "game-fiendwar-boss-death-blocked", "dungeon_id", runtime.Dungeon.ID, "reason", reason)
		return false, nil
	}
	defer s.raidMu.Unlock()
	return apply()
}

// Split the existing forced-clear prepare/flush at its memory/transport
// boundary so the FiendWar check never holds raidMu while sending packets.
func (s *Service) forceCurrentDungeonCombatEndWithFiendWarGuardLocked(session *gameSession, runtime *runtimeDungeonState, objectKey uint32, source string) (int, int, bool, error) {
	var prepared currentDungeonForcedCombatEnd
	allowed, err := s.commitCurrentFiendWarBossWindowLocked(session, runtime, func() error {
		var prepareErr error
		prepared, prepareErr = s.prepareCurrentDungeonForcedCombatEndLocked(runtime, objectKey)
		return prepareErr
	})
	if !allowed || err != nil {
		return len(prepared.Targets), 0, false, err
	}
	visual, cleared, err := s.flushCurrentDungeonForcedCombatEndLocked(session, runtime, objectKey, source, prepared)
	return len(prepared.Targets), visual, cleared, err
}
