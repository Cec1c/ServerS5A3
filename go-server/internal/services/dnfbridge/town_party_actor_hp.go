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

// Capture the existing town HUD projection, not a new combat-HP source. In
// particular, do not interpret the party subsystem's default percentage as
// measured HP or change its established town roster serialization here.
func (s *Service) currentTownPartyRealtimeBody(target, actor *gameSession) []byte {
	if s == nil || s.options.currentClientVersion != "official-24.9" ||
		target == nil || actor == nil || target == actor ||
		target.selectedCharacterID == actor.selectedCharacterID ||
		s.sessionHasActiveDungeonRuntime(target) || s.sessionHasActiveDungeonRuntime(actor) ||
		isCurrentFiendWarChannel(target) || isCurrentFiendWarChannel(actor) {
		return nil
	}
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
	if len(realtime) == 0 {
		return body, nil
	}
	if len(body) < 3 || body[0] != currentCrossSceneMode8Subtype {
		return nil, fmt.Errorf("town party profile is not mode8")
	}
	projected := append([]byte(nil), body...)
	projected[0] = currentCrossSceneMode0Subtype
	if err := projectCurrentTownPartyHPIntoMode0(projected, realtime, selectedCharacterID(actor)); err != nil {
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
