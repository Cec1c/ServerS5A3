package dnfbridge

import (
	"encoding/binary"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// Instance-owned life state survives room moves. Reviving invalidates every
// old door vote for that participant so a living player must arrive again.
func markPartyDungeonLife(runtime *runtimeDungeonState, characterID uint16, dead bool) {
	if runtime == nil || runtime.partyInstance == nil || characterID == 0 {
		return
	}
	instance := runtime.partyInstance
	instance.mu.Lock()
	defer instance.mu.Unlock()
	if _, present := instance.members[characterID]; !present {
		return
	}
	if instance.deadMembers == nil {
		instance.deadMembers = make(map[uint16]bool, 4)
	}
	instance.deadMembers[characterID] = dead
	for _, votes := range instance.doorArrivals {
		delete(votes, characterID)
	}
}

func partyDungeonHasPartner(runtime *runtimeDungeonState, characterID uint16) bool {
	if runtime == nil || runtime.partyInstance == nil {
		return false
	}
	instance := runtime.partyInstance
	instance.mu.Lock()
	defer instance.mu.Unlock()
	for member := range instance.members {
		if member != characterID {
			return true
		}
	}
	return false
}

// Scene rebuilding can create fresh actors after the first life notification.
// Replay only authoritative dead actors after all actor/roster followups.
func (s *Service) replayPartyDungeonDeadStateAfterLoading(session *gameSession) error {
	if session == nil {
		return nil
	}
	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	var instance *dungeonPartyInstance
	if runtime != nil {
		instance = runtime.partyInstance
	}
	session.dungeon.mu.Unlock()
	if instance == nil {
		return nil
	}
	instance.mu.Lock()
	dead := make(map[uint16]bool)
	for id, value := range instance.deadMembers {
		if _, present := instance.members[id]; present && value {
			dead[id] = true
		}
	}
	instance.mu.Unlock()
	if len(dead) == 0 {
		return nil
	}
	for id := range dead {
		body := buildCurrentDungeonDeathStateBody(currentSceneActorObjectKey(id), 0, 0)
		if err := s.sendGameUpperRawClass(session, currentDungeonDeathStateMsgID, body, 0); err != nil {
			return err
		}
	}
	return s.sendGameUpperRawClass(session, 153, partyDungeonLifeRealtimeBody(runtimePartyStateSnapshot(session), dead), 0)
}

func partyDungeonLifeRealtimeBody(state alignedcmd.PartyState, dead map[uint16]bool) []byte {
	body := runtimePartyRealtimeInfoBody(state)
	for offset := 1; offset+5 <= len(body); offset += 5 {
		if dead[binary.LittleEndian.Uint16(body[offset:offset+2])] {
			body[offset+2] = 0
		}
	}
	return body
}

// Called after releasing the caller's dungeon mutex. Snapshot the instance,
// then send without holding it or acquiring another participant's mutex.
func (s *Service) broadcastPartyDungeonLife(session *gameSession, runtime *runtimeDungeonState, revived bool) error {
	if session == nil || runtime == nil || runtime.partyInstance == nil {
		return nil
	}
	state := runtimePartyStateSnapshot(session)
	instance := runtime.partyInstance
	instance.mu.Lock()
	peers := make(map[uint16]*gameSession, len(instance.sessions))
	dead := make(map[uint16]bool, len(instance.deadMembers))
	for id, peer := range instance.sessions {
		if _, present := instance.members[id]; present {
			peers[id] = peer
		}
	}
	for id, value := range instance.deadMembers {
		dead[id] = value
	}
	instance.mu.Unlock()
	value := byte(0)
	if revived {
		value = currentDungeonReviveStateType
	}
	body := buildCurrentDungeonDeathStateBody(currentSceneActorObjectKey(session.selectedCharacterID), value, 0)
	hp := partyDungeonLifeRealtimeBody(state, dead)
	var firstError error
	delivered := 0
	for id, peer := range peers {
		if peer == nil || peer.selectedCharacterID != id || runtimePartyStateSnapshot(peer).PartyID != state.PartyID {
			continue
		}
		// The caller already received its native death/revive notification.
		if peer != session {
			if err := s.sendGameUpperRawClass(peer, currentDungeonDeathStateMsgID, body, 0); err != nil {
				if firstError == nil {
					firstError = err
				}
				continue
			}
		}
		if err := s.sendGameUpperRawClass(peer, 153, hp, 0); err != nil {
			if firstError == nil {
				firstError = err
			}
			continue
		}
		delivered++
	}
	s.logGameEvent(session, "game-dungeon-party-life-synchronized", "char_id", session.selectedCharacterID,
		"instance_id", instance.id, "revived", revived, "receivers", delivered, "sequence", "native_op32_then_op153")
	return firstError
}
