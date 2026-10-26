package dnfbridge

import (
	"encoding/binary"
	"fmt"
)

// Published v158 class0/op153 (0x20331e0) updates HP only for actors already
// present in the active scene lookup (0x296d730). Missing actors are skipped,
// not cached. Refresh after each complete town actor projection, before work
// on other actors can postpone the existing final local-roster refresh.
// This is an HP update only: it must not redeclare op9 or restart op11 links.
func (s *Service) sendCurrentTownPartyHPAfterActor(target, actor *gameSession) error {
	body := s.currentTownPartyRealtimeBody(target, actor)
	if len(body) == 0 {
		return nil
	}
	return s.sendGameUpperRawClassCodec(target, 0x0099, body, 0, true)
}

// A receiver can finish rebuilding its town scene while every party peer is
// in another area. Same-area actor replay then has no rows and cannot restore
// the party HP projection. Refresh only the existing authoritative party HP:
// this does not create an actor, redeclare a party or restart peer links.
func (s *Service) sendCurrentTownPartyHPAfterCrossAreaLoading(target *gameSession) error {
	if s == nil || s.onlinePlayers == nil || target == nil || target.selectedCharacterID == 0 {
		return nil
	}
	manager := s.runtimePartyManagerForService()
	snapshot, found := manager.SnapshotByUser(target.selectedCharacterID, 0)
	if !found {
		return nil
	}
	for _, member := range runtimePartyMembers(snapshot.StateFor(target.selectedCharacterID)) {
		if member.UserID == target.selectedCharacterID ||
			s.onlinePlayers.PeerInSameArea(target.selectedCharacterID, member.UserID) {
			continue
		}
		identity, current := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !current || identity.session == nil {
			continue
		}
		body := s.currentTownPartyRealtimeBody(target, identity.session)
		if len(body) == 0 {
			continue
		}
		// The body contains the full current table, including explicit zero HP.
		return s.sendGameUpperRawClassCodec(target, 0x0099, body, 0, true)
	}
	return nil
}

// Capture the existing town HUD projection, not a new combat-HP source. In
// particular, do not interpret the party subsystem's default percentage as
// measured HP or change its established town roster serialization here.
func (s *Service) currentTownPartyRealtimeBody(target, actor *gameSession) []byte {
	if !s.currentTownPartyHPProjectionEligible(target, actor) ||
		s.sessionHasActiveDungeonRuntime(target) || s.sessionHasActiveDungeonRuntime(actor) {
		return nil
	}
	return s.currentTownPartyRealtimeBodyOutsideDungeon(target, actor)
}

func (s *Service) currentTownPartyHPProjectionEligible(target, actor *gameSession) bool {
	return s != nil && s.options.currentClientVersion == "official-24.9" &&
		target != nil && actor != nil && target != actor &&
		target.selectedCharacterID != actor.selectedCharacterID &&
		!isCurrentFiendWarChannel(target) && !isCurrentFiendWarChannel(actor)
}

// Called with the receiver's wire held. Dungeon handlers can hold dungeon.mu
// while waiting for that wire, so never wait for either dungeon lock here.
// A busy snapshot asks the caller to release appearance AND wire and retry
// within its original deadline. No dungeon lock survives the snapshot.
func (s *Service) tryCurrentTownPartyRealtimeBodyOnWire(target, actor *gameSession) ([]byte, bool) {
	if !s.currentTownPartyHPProjectionEligible(target, actor) {
		return nil, true
	}
	if !target.dungeon.mu.TryLock() {
		return nil, false
	}
	if !actor.dungeon.mu.TryLock() {
		target.dungeon.mu.Unlock()
		return nil, false
	}
	active := (target.dungeon.runtime != nil && target.dungeon.runtime.Session != nil) ||
		(actor.dungeon.runtime != nil && actor.dungeon.runtime.Session != nil)
	actor.dungeon.mu.Unlock()
	target.dungeon.mu.Unlock()
	if active {
		return nil, true
	}
	return s.currentTownPartyRealtimeBodyOutsideDungeon(target, actor), true
}

// Both callers establish the existing no-active-dungeon condition first.
// The wire caller also keeps all later HP writes behind this projection.
func (s *Service) currentTownPartyRealtimeBodyOutsideDungeon(target, actor *gameSession) []byte {
	channel := currentTownPresenceChannelID(target)
	if channel == 0 || channel != currentTownPresenceChannelID(actor) {
		return nil
	}
	targetBound, ok := s.boundGameSessionCharacterSnapshot(target)
	if !ok {
		return nil
	}
	actorBound, ok := s.boundGameSessionCharacterSnapshot(actor)
	if !ok || actorBound.character == targetBound.character {
		return nil
	}
	// A cached session roster may survive a leave or connection replacement.
	// Only the current manager membership authorizes this additional update.
	manager := s.runtimePartyManagerForService()
	snapshot, found := manager.SnapshotByUser(targetBound.character, 0)
	if !found {
		return nil
	}
	state := snapshot.StateFor(targetBound.character)
	if !containsRuntimePartyMember(runtimePartyMembers(state), actorBound.character) ||
		!s.boundGameSessionCharacterCurrent(targetBound) || !s.boundGameSessionCharacterCurrent(actorBound) {
		return nil
	}
	if state.PartyID <= 0 || len(runtimePartyMembers(state)) <= 1 {
		return nil
	}
	return runtimePartyRealtimeInfoBody(state)
}

// Mode8 contains a mode0/mode3 pair with the same first-record offsets as
// mode0. Project the receiver's current town HP onto a delivery copy: the
// batch template may also serve a viewer outside this ordinary town party.
func (s *Service) currentTownPartyProfileWithHP(target, actor *gameSession, body []byte) ([]byte, error) {
	realtime := s.currentTownPartyRealtimeBody(target, actor)
	return projectCurrentTownPartyHPIntoProfile(body, realtime, selectedCharacterID(actor))
}

func projectCurrentTownPartyHPIntoProfile(body, realtime []byte, actorID uint16) ([]byte, error) {
	if len(realtime) == 0 {
		return body, nil
	}
	if len(body) < 3 || body[0] != currentCrossSceneMode8Subtype {
		return nil, fmt.Errorf("town party profile is not mode8")
	}
	projected := append([]byte(nil), body...)
	projected[0] = currentCrossSceneMode0Subtype
	if err := projectCurrentTownPartyHPIntoMode0(projected, realtime, actorID); err != nil {
		return nil, err
	}
	projected[0] = currentCrossSceneMode8Subtype
	return projected, nil
}

// Initialize only this actor's mode0 HP byte from the exact existing op153
// presentation value. The native mode0 reader otherwise clears descriptor+31C
// before the later op153 restores it, on both creation and refresh paths.
// Explicit zero in an op153 projection remains zero; absent or malformed rows
// never supply a default. The caller restricts this to current ordinary-town
// party sessions, leaving dungeon life notifications and other actors alone.
func projectCurrentTownPartyHPIntoMode0(mode0, realtime []byte, actorID uint16) error {
	if len(realtime) == 0 {
		return nil
	}
	if actorID == 0 || actorID == ^uint16(0) || realtime[0] < 2 || realtime[0] > 4 ||
		len(realtime) != 1+int(realtime[0])*5 {
		return fmt.Errorf("invalid town realtime HP projection")
	}
	hp, matches := byte(0), 0
	for offset := 1; offset < len(realtime); offset += 5 {
		if binary.LittleEndian.Uint16(realtime[offset:offset+2]) == actorID {
			hp = realtime[offset+2]
			matches++
		}
	}
	if matches != 1 || hp > 100 {
		return fmt.Errorf("town HP projection actor identity or percentage invalid")
	}
	const keyOffset = 5 + 0x47
	if len(mode0) < keyOffset+2 || mode0[0] != 0 ||
		binary.LittleEndian.Uint16(mode0[1:3]) != 1 ||
		binary.LittleEndian.Uint16(mode0[keyOffset:keyOffset+2]) != currentSceneActorObjectKey(actorID) {
		return fmt.Errorf("town mode0 HP projection identity differs")
	}
	_, tailStart, ok := currentSceneObjectLevelForLog(mode0)
	if !ok || tailStart >= len(mode0) {
		return fmt.Errorf("town mode0 HP projection tail missing")
	}
	tail := mode0[tailStart:]
	equipEnd, ok := currentSceneObjectEquipSummaryEnd(tail, 6)
	if !ok || equipEnd+4 >= len(tail) {
		return fmt.Errorf("town mode0 HP projection equipment boundary invalid")
	}
	// v113 is four bytes after the variable-length equipment table; v114
	// follows it. Keep all names, equipment, clone links and tail bytes exact.
	tail[equipEnd+4] = hp
	return nil
}
