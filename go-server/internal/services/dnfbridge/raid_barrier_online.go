package dnfbridge

import (
	"fmt"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// The caller holds only its dungeon mutex. This path consumes an already
// accepted, notified room actor death; CMT662 and client amounts are not inputs.
func (s *Service) recordCurrentAntonConstructorDeathLocked(session *gameSession, runtime *runtimeDungeonState, objectKey uint32, now time.Time) (projections []runtimeRaidState, err error) {
	if runtime == nil || runtime.raidEntry == nil || runtime.Room == nil {
		return nil, nil
	}
	target, accepted := runtime.Room.DefeatedDeathTarget(objectKey)
	if !accepted {
		return nil, fmt.Errorf("barrier constructor lacks accepted actor death")
	}
	monster, found := runtime.Room.MonsterByReference(target.Reference, runtimeDungeonMonsterDefeated)
	if !found {
		return nil, nil
	}
	ref := strings.ToLower(strings.ReplaceAll(monster.Definition.Path, "\\", "/"))
	const prefix = "monster/newmonsters/anton/phase3/constructor/constructor"
	index := -1
	for n, suffix := range []string{"", "_1", "_2", "_3"} {
		if ref == prefix+suffix+".mob" {
			index = n
		}
	}
	if index < 0 {
		return nil, nil
	}
	if _, sent := runtime.Room.DeathResponseBody(objectKey); !sent || runtime.Dungeon.ID != int64(221+index) ||
		session == nil || !s.currentRaidDungeonBindingRequired(session) || !s.currentRaidAuthenticated(session) ||
		session.dungeon.runtime != runtime || runtime.Session == nil || !dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) ||
		runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive || runtime.townReturnPending || now.IsZero() {
		return nil, fmt.Errorf("barrier constructor requires current owned notified hatchery actor")
	}
	var clockErrors []error
	projections, clockErrors = s.advanceCurrentRaidClocks(now)
	for _, clockErr := range clockErrors {
		s.logWarn("raid barrier clock deferred", "error", clockErr)
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	e := runtime.raidEntry
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	if state == nil || !state.Started || state.Battle == nil || state.RaidKey != e.RaidKey {
		return projections, fmt.Errorf("barrier raid instance unavailable")
	}
	b := state.Battle
	if b.Single || b.Mode != 1 || b.Anton == nil || b.InstanceID != e.InstanceID || b.Phase != 1 || e.Phase != 1 ||
		b.Anton.Epoch() != e.Attempt || e.RunID == "" || session.channel.ID != e.ChannelID || session.channel.Type != e.ChannelType ||
		b.MemberAccounts[session.selectedCharacterID] == "" || b.MemberAccounts[session.selectedCharacterID] != runtime.AccountID || runtime.AccountID != s.accountIDForSession(session) ||
		b.PhaseCompleted || b.TimedOut || now.Before(b.EnterAfter) || !now.Before(b.Deadline) || now.Before(b.Anton.LastTime()) {
		return projections, fmt.Errorf("barrier constructor binding/attempt/owner expired")
	}
	participant := false
	for _, id := range e.Participants {
		participant = participant || id == session.selectedCharacterID
	}
	if !participant {
		return projections, fmt.Errorf("barrier constructor reporter outside frozen group")
	}
	if _, cleared := b.ClearedRuns[e.RunID]; cleared {
		return projections, fmt.Errorf("barrier constructor run already cleared")
	}
	scene, ok := runtime.Session.Scene()
	if !ok || scene.Coordinate != runtime.Room.Snapshot().Coordinate {
		return projections, fmt.Errorf("barrier constructor current room mismatch")
	}
	eventID := fmt.Sprintf("%s/%s/%d/%d", e.RunID, scene.Coordinate.String(), scene.Map.Map.ID, monster.ObjectKey)
	next, replay, err := b.Anton.ApplyBarrierDamage(eventID, raid.AntonHatcheryConstructor, uint32(runtime.Dungeon.ID), e.Attempt, now)
	if err != nil {
		return projections, err
	}
	if replay {
		return projections, nil
	}
	b.Anton, b.Dungeons, b.PhaseCompleted = next, next.Dungeons(), next.PhaseCleared()
	b.ProgressVersion++
	return append(projections, cloneRuntimeRaidState(*state)), nil
}
