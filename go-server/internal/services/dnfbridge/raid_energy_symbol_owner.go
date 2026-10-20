package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Actual transmission last-action scripts emit CMT662 without an object ID.
// Bind that event to the server-frozen room's one transmission owner; the
// operand only identifies that source and never becomes a damage amount.
func (s *Service) recordCurrentAntonEnergySymbolLocked(session *gameSession, body []byte, now time.Time) (runtimeRaidState, bool, error) {
	if len(body) != 9 || binary.LittleEndian.Uint32(body) != 110 || body[8] != 2 {
		return runtimeRaidState{}, false, fmt.Errorf("energy symbol requires exact transmission subtraction")
	}
	r := session.dungeon.runtime
	if !s.currentRaidAuthenticated(session) || !s.currentRaidDungeonBindingRequired(session) || r == nil || r.raidEntry == nil || r.Dungeon.ID != 218 || r.Session == nil || r.Room == nil || r.townReturnPending || !dungeonRuntimeOwnsCharacter(r, session.selectedCharacterID) {
		return runtimeRaidState{}, false, fmt.Errorf("energy symbol has no current committed energy runtime")
	}
	scene, ok := r.Session.Scene()
	if !ok || scene.Map.Map.ID <= 0 || scene.Coordinate != r.Room.Snapshot().Coordinate || !session.postStartMapPlayerStateSent {
		return runtimeRaidState{}, false, fmt.Errorf("energy symbol room not committed")
	}
	e := r.raidEntry
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	if state == nil || !state.Started || state.Battle == nil || state.RaidKey != e.RaidKey {
		return runtimeRaidState{}, false, fmt.Errorf("energy raid owner unavailable")
	}
	b := state.Battle
	if b.Single || b.Mode != 1 || b.Anton == nil || b.InstanceID != e.InstanceID || b.Phase != 1 || e.Phase != 1 || b.Anton.Epoch() != e.Attempt || e.RunID == "" || b.ChannelID != e.ChannelID || b.ChannelType != e.ChannelType || session.channel.ID != e.ChannelID || session.channel.Type != e.ChannelType || b.MemberAccounts[session.selectedCharacterID] == "" || b.MemberAccounts[session.selectedCharacterID] != r.AccountID || r.AccountID != s.accountIDForSession(session) || b.PhaseCompleted || b.TimedOut || now.Before(b.EnterAfter) || !now.Before(b.Deadline) || now.Before(b.Anton.LastTime()) {
		return runtimeRaidState{}, false, fmt.Errorf("energy raid binding expired")
	}
	participant := false
	for _, id := range e.Participants {
		participant = participant || id == session.selectedCharacterID
	}
	if !participant {
		return runtimeRaidState{}, false, fmt.Errorf("energy reporter outside frozen run")
	}
	operand := binary.LittleEndian.Uint32(body[4:8])
	var source raid.AntonBarrierSource
	var objectID int64
	if expected, ok := b.Anton.TransmissionBarrierOperand(raid.AntonSmallTransmission); ok && operand == expected {
		if r.Session.Snapshot().Run.Status != worldmap.DungeonRunActive || r.randomizedObjects == nil || r.randomizedObjects.validate(r.Dungeon, r.MazeIndex) != nil {
			return runtimeRaidState{}, false, fmt.Errorf("small transmission has no frozen active selection")
		}
		count := 0
		for _, entry := range r.randomizedObjects.Entries {
			if entry.Source.Map == scene.Coordinate && entry.Source.ObjectID == 18865 && entry.SpawnMode == 1 && entry.Faction == 100 {
				count++
			}
		}
		if count != 1 {
			return runtimeRaidState{}, false, fmt.Errorf("small transmission room owner unavailable or ambiguous")
		}
		source, objectID = raid.AntonSmallTransmission, 18865
	} else if expected, ok := b.Anton.TransmissionBarrierOperand(raid.AntonLargeTransmission); ok && operand == expected {
		count := 0
		for _, object := range scene.PassiveObjects {
			if object.ObjectID == 18864 {
				count++
			}
		}
		if count != 1 {
			return runtimeRaidState{}, false, fmt.Errorf("large transmission room owner unavailable or ambiguous")
		}
		clear, found := b.ClearedRuns[e.RunID]
		if r.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted || !found || clear.DungeonID != 218 || clear.Phase != e.Phase || clear.Attempt != e.Attempt || !sameRaidParticipants(clear.Participants, e.Participants) {
			return runtimeRaidState{}, false, fmt.Errorf("large transmission completed run owner unavailable")
		}
		notifiedBoss := false
		for _, monster := range r.Room.Snapshot().Monsters {
			if monster.Definition.ID != 69311 {
				continue
			}
			_, accepted := r.Room.DefeatedDeathTarget(monster.ObjectKey)
			_, notified := r.Room.DeathResponseBody(monster.ObjectKey)
			notifiedBoss = notifiedBoss || (accepted && notified)
		}
		if !notifiedBoss {
			return runtimeRaidState{}, false, fmt.Errorf("large transmission has no accepted notified energy boss")
		}
		source, objectID = raid.AntonLargeTransmission, 18864
	} else {
		return runtimeRaidState{}, false, fmt.Errorf("energy operand is not a loaded transmission source")
	}
	// One ID is shared by every peer in this run and survives leaving/revisiting
	// this room. A replay cannot consume another peer's copy of the same device.
	eventID := fmt.Sprintf("%s/%s/%d/transmission%d", e.RunID, scene.Coordinate.String(), scene.Map.Map.ID, objectID)
	next, replay, err := b.Anton.ApplyBarrierDamage(eventID, source, 218, e.Attempt, now)
	if err != nil || replay {
		return runtimeRaidState{}, false, err
	}
	b.Anton, b.Dungeons, b.PhaseCompleted = next, next.Dungeons(), next.PhaseCleared()
	b.ProgressVersion++
	return cloneRuntimeRaidState(*state), true, nil
}

func (s *Service) handleCurrentAntonEnergySymbol(session *gameSession, typ uint16, body []byte) error {
	if session == nil {
		return nil
	}
	session.dungeon.mu.Lock()
	state, changed, err := s.recordCurrentAntonEnergySymbolLocked(session, body, s.gameplayNow())
	session.dungeon.mu.Unlock()
	if err != nil {
		s.logGameEvent(session, "game-raid-energy-symbol-rejected", "error", err)
		return s.sendRaidCommandFailure(session, typ)
	}
	if changed {
		s.queueCurrentRaidDungeonProgress(state)
	}
	return s.sendGameUpperRawClassCodec(session, typ, []byte{1}, 1, true)
}
