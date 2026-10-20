package dnfbridge

import (
	"fmt"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// These events come from committed server scenes, never from a symbol/state
// request. Caller owns the reporting dungeon mutex; projections are queued
// asynchronously and cannot run in this critical section.
func (s *Service) reduceCurrentLukeSceneLocked(session *gameSession, runtime *runtimeDungeonState, kind string, now time.Time) (runtimeRaidState, bool, error) {
	if runtime == nil || runtime.raidEntry == nil {
		return runtimeRaidState{}, false, nil
	}
	// A replaced socket can lose its canonical character binding before its
	// serialized shutdown runs. Only that closed event loop may retire its own
	// committed run; packet-driven events still require the current binding.
	closingRetirement := kind == "retire" && session != nil && session.events != nil && !session.events.active.Load()
	if closingRetirement {
		kind = "giveup"
	}
	if (!s.currentRaidAuthenticated(session) && !closingRetirement) || session.dungeon.runtime != runtime || runtime.Session == nil || !dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) {
		return runtimeRaidState{}, false, fmt.Errorf("Luke event has no committed authenticated runtime")
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	e := runtime.raidEntry
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	if state == nil || !state.Started || state.Battle == nil {
		return runtimeRaidState{}, false, nil
	}
	b := state.Battle
	if b.Mode != 2 || b.Single {
		return runtimeRaidState{}, false, nil
	}
	if b.PhaseCompleted || b.TimedOut {
		return runtimeRaidState{}, false, nil
	}
	if !currentRaidHasTwoPhaseProgram(b) || state.RaidKey != e.RaidKey || b.InstanceID != e.InstanceID || b.Phase != e.Phase || b.Anton.Epoch() != e.Attempt || e.RunID == "" ||
		b.ChannelID != session.channel.ID || b.ChannelType != session.channel.Type || e.ChannelID != b.ChannelID || e.ChannelType != b.ChannelType ||
		b.MemberAccounts[session.selectedCharacterID] != runtime.AccountID || runtime.AccountID != s.accountIDForSession(session) {
		return runtimeRaidState{}, false, fmt.Errorf("Luke event binding changed")
	}
	participant := false
	for _, id := range e.Participants {
		participant = participant || id == session.selectedCharacterID
	}
	if !participant || runtime.Dungeon.ID <= 0 || runtime.Dungeon.ID > int64(^uint32(0)) {
		return runtimeRaidState{}, false, fmt.Errorf("Luke event participant/dungeon unavailable")
	}
	if now.Before(b.Anton.LastTime()) {
		now = b.Anton.LastTime()
	}
	if now.Before(b.EnterAfter) || !now.Before(b.Deadline) {
		return runtimeRaidState{}, false, fmt.Errorf("Luke event phase expired")
	}
	if _, done := b.ClearedRuns[e.RunID]; done {
		return runtimeRaidState{}, false, nil
	}
	luke := &raid.LukeProgression{AntonProgression: b.Anton}
	var next *raid.LukeProgression
	var err error
	switch kind {
	case "enter":
		if runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
			return runtimeRaidState{}, false, fmt.Errorf("Luke entry run is not active")
		}
		next, _, err = luke.EnterDungeon(e.RunID, uint32(runtime.Dungeon.ID), e.Attempt, now)
		if err == nil {
			err = s.commitCurrentLukeRouteLocked(b, session.selectedCharacterID, uint32(runtime.Dungeon.ID))
		}
	case "map":
		if runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive || !luke.HasEnteredRun(e.RunID, uint32(runtime.Dungeon.ID)) {
			return runtimeRaidState{}, false, fmt.Errorf("Luke map has no accepted active entry")
		}
		scene, ok := runtime.Session.Scene()
		if !ok || scene.Map.Map.ID <= 0 || scene.Map.Map.ID > int64(^uint32(0)) {
			return runtimeRaidState{}, false, fmt.Errorf("Luke committed map unavailable")
		}
		next, err = luke.EnterMap(uint32(scene.Map.Map.ID), now)
	case "giveup":
		if runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunAbandoned {
			return runtimeRaidState{}, false, fmt.Errorf("Luke giveup has no committed abandonment")
		}
		if b.RunDepartures == nil {
			b.RunDepartures = map[string]map[uint16]bool{}
		}
		if b.RunDepartures[e.RunID] == nil {
			b.RunDepartures[e.RunID] = map[uint16]bool{}
		}
		b.RunDepartures[e.RunID][session.selectedCharacterID] = true
		for _, id := range e.Participants {
			if !b.RunDepartures[e.RunID][id] {
				return cloneRuntimeRaidState(*state), false, nil
			}
		}
		if luke.HasEnteredRun(e.RunID, uint32(runtime.Dungeon.ID)) {
			next, err = luke.GiveUpDungeon(e.RunID, uint32(runtime.Dungeon.ID), now)
		} else {
			next, err = luke.CancelReservation(e.RunID, uint32(runtime.Dungeon.ID))
		}
	default:
		return runtimeRaidState{}, false, fmt.Errorf("unknown Luke scene event")
	}
	if err != nil {
		return runtimeRaidState{}, false, err
	}
	changed := !b.Anton.SameProjection(next.AntonProgression)
	b.Anton, b.Dungeons = next.AntonProgression, next.Dungeons()
	if changed {
		b.ProgressVersion++
	}
	return cloneRuntimeRaidState(*state), changed, nil
}

func (s *Service) cancelUncommittedLukeEntry(runtime *runtimeDungeonState) {
	if runtime == nil || runtime.raidEntry == nil {
		return
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	e := runtime.raidEntry
	state := s.raids[e.RaidKey]
	if state == nil || state.Battle == nil || state.Battle.Mode != 2 || state.Battle.InstanceID != e.InstanceID || state.Battle.Phase != e.Phase || !currentRaidHasTwoPhaseProgram(state.Battle) {
		return
	}
	next, err := (&raid.LukeProgression{AntonProgression: state.Battle.Anton}).CancelReservation(e.RunID, uint32(runtime.Dungeon.ID))
	if err == nil {
		state.Battle.Anton = next.AntonProgression
	}
}

func (s *Service) retireCurrentLukeSessionRun(session *gameSession) {
	if session == nil {
		return
	}
	session.dungeon.mu.Lock()
	r := session.dungeon.runtime
	if r == nil || r.raidEntry == nil || session.channel.Type != 32 || r.Session == nil || r.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		session.dungeon.mu.Unlock()
		return
	}
	if err := r.Session.Abandon(); err != nil {
		session.dungeon.mu.Unlock()
		return
	}
	kind := "giveup"
	if session.events != nil && !session.events.active.Load() {
		kind = "retire"
	}
	state, changed, err := s.reduceCurrentLukeSceneLocked(session, r, kind, time.Now())
	session.dungeon.mu.Unlock()
	if err != nil {
		s.logGameEvent(session, "game-luke-session-retire-rejected", "error", err)
	}
	if changed {
		s.queueCurrentRaidDungeonProgress(state)
	}
}

func (s *Service) recordCurrentLukeScene(session *gameSession, runtime *runtimeDungeonState, kind string) {
	session.dungeon.mu.Lock()
	state, changed, err := s.reduceCurrentLukeSceneLocked(session, runtime, kind, time.Now())
	session.dungeon.mu.Unlock()
	if err != nil {
		s.logGameEvent(session, "game-luke-scene-event-rejected", "kind", kind, "error", err)
	}
	if changed {
		s.queueCurrentRaidDungeonProgress(state)
	}
}
