package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"time"
)

// The native ACT producer has no object ID. A report can credit only the one
// actual indexed raid machine in the current committed room. Peers share its
// run/room event identity. This budget prevents repeats; it cannot independently
// prove a client-owned passive object's death.
func (s *Service) recordCurrentLukeGeneratorLocked(session *gameSession, body []byte, now time.Time) (runtimeRaidState, bool, error) {
	if session == nil || len(body) != 9 || body[8] != 1 || binary.LittleEndian.Uint32(body[4:8]) != 1 {
		return runtimeRaidState{}, false, fmt.Errorf("Luke generator requires exact increment action")
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || session.events != nil && !session.events.active.Load() {
		return runtimeRaidState{}, false, fmt.Errorf("Luke generator session binding unavailable")
	}
	r := session.dungeon.runtime
	if !s.currentRaidAuthenticated(session) || !s.currentRaidDungeonBindingRequired(session) || r == nil || r.raidEntry == nil || r.Session == nil || r.Room == nil || r.townReturnPending || !dungeonRuntimeOwnsCharacter(r, session.selectedCharacterID) || r.Session.Snapshot().Run.Status != worldmap.DungeonRunActive || !session.postStartMapPlayerStateSent {
		return runtimeRaidState{}, false, fmt.Errorf("Luke generator requires current committed active owner")
	}
	scene, ok := r.Session.Scene()
	if !ok || scene.Map.Map.ID <= 0 || scene.Coordinate != r.Room.Snapshot().Coordinate {
		return runtimeRaidState{}, false, fmt.Errorf("Luke generator room not committed")
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	e := r.raidEntry
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	if state == nil || !state.Started || state.Battle == nil || state.RaidKey != e.RaidKey {
		return runtimeRaidState{}, false, fmt.Errorf("Luke generator instance unavailable")
	}
	b := state.Battle
	if b.Single || b.Mode != 2 || b.Anton == nil || !b.Anton.IsLuke() || b.InstanceID != e.InstanceID || b.Phase != 0 || e.Phase != 0 || b.Anton.Epoch() != e.Attempt || e.RunID == "" || b.ChannelID != e.ChannelID || b.ChannelType != e.ChannelType || session.channel.ID != e.ChannelID || session.channel.Type != e.ChannelType || b.MemberAccounts[session.selectedCharacterID] == "" || b.MemberAccounts[session.selectedCharacterID] != r.AccountID || r.AccountID != s.accountIDForSession(session) || b.PhaseCompleted || b.TimedOut || now.Before(b.EnterAfter) || !now.Before(b.Deadline) || now.Before(b.Anton.LastTime()) {
		return runtimeRaidState{}, false, fmt.Errorf("Luke generator binding expired")
	}
	participant := false
	for _, id := range e.Participants {
		participant = participant || id == session.selectedCharacterID
	}
	if !participant {
		return runtimeRaidState{}, false, fmt.Errorf("Luke generator outside frozen participants")
	}
	luke := &raid.LukeProgression{AntonProgression: b.Anton}
	id := uint32(r.Dungeon.ID)
	if symbol := luke.SanctumGeneratorSymbol(id); symbol == 0 || symbol != binary.LittleEndian.Uint32(body) {
		return runtimeRaidState{}, false, fmt.Errorf("Luke generator symbol does not belong to dungeon")
	}
	budget := 0
	for _, o := range scene.PassiveObjects {
		if luke.SanctumGeneratorOwner(id, o.ObjectID) {
			budget++
		}
	}
	if budget != 1 {
		return runtimeRaidState{}, false, fmt.Errorf("Luke generator room source unavailable or ambiguous")
	}
	event := fmt.Sprintf("%s/%s/%d", e.RunID, scene.Coordinate.String(), scene.Map.Map.ID)
	// Lock order matches existing freezeCurrentRaidDungeonGroupEntry:
	// dungeon.mu -> raidMu -> Service.mu. Keep canonical socket ownership fixed
	// through the pure reducer; no DB, transport, peer queue or callback here.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gameSessions[identity.character] != session || session.selectedCharacterID != identity.character || session.characterGeneration != identity.generation || session.events != nil && !session.events.active.Load() {
		return runtimeRaidState{}, false, fmt.Errorf("Luke generator canonical session changed")
	}
	next, replay, err := luke.CreditSanctumGenerator(e.RunID, event, id, e.Attempt, now)
	if err != nil || replay {
		return runtimeRaidState{}, false, err
	}
	b.Anton = next.AntonProgression
	b.Dungeons = next.Dungeons()
	b.ProgressVersion++
	return cloneRuntimeRaidState(*state), true, nil
}
func (s *Service) handleCurrentLukeGeneratorSymbol(session *gameSession, body []byte) error {
	if session == nil {
		return nil
	}
	session.dungeon.mu.Lock()
	state, changed, err := s.recordCurrentLukeGeneratorLocked(session, body, s.gameplayNow())
	session.dungeon.mu.Unlock()
	if err != nil {
		s.logGameEvent(session, "game-raid-luke-generator-rejected", "error", err)
		return nil
	}
	if changed {
		s.queueCurrentRaidDungeonProgress(state)
	}
	// Existing native producer is a notification request. No unproven class1/662
	// ACK consumer or permit bit is added; state is published through raid symbols.
	return nil
}
