package dnfbridge

import (
	"fmt"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

// Check resident room ownership before sending the leader's op29. This is not
// a door-position quorum: internal follower movement carries no client arrival
// evidence. No dungeon lock is held while acquiring another member's lock.
// Members may still disconnect after this check; socket writes are not atomic.
func (s *Service) preflightRuntimePartyRoomOwners(leader *gameSession, request dungeoncmd.MoveMapRequest) error {
	if leader == nil || leader.selectedCharacterID == 0 {
		return nil
	}
	state := s.runtimePartyStateForSession(leader)
	if state.PartyID <= 0 || state.UserID != leader.selectedCharacterID {
		return nil
	}
	leader.dungeon.mu.Lock()
	runtime := leader.dungeon.runtime
	if runtime == nil || runtime.Session == nil || runtime.Room == nil {
		leader.dungeon.mu.Unlock()
		return nil // The established local move handler owns this refusal.
	}
	scene, ok := runtime.Session.Scene()
	if !ok || (scene.Coordinate.X == int64(request.NextX) && scene.Coordinate.Y == int64(request.NextY)) {
		leader.dungeon.mu.Unlock()
		return nil // Keep the existing same-coordinate layer/ACK path unchanged.
	}
	source := runtimeDungeonRoomKeyFromScene(scene)
	dungeonID, mazeIndex, seed := runtime.Dungeon.ID, runtime.MazeIndex, runtime.Seed
	instance := runtime.partyInstance
	leader.dungeon.mu.Unlock()

	for _, member := range runtimePartyMembers(state) {
		if member.UserID == 0 || member.UserID == leader.selectedCharacterID {
			continue
		}
		follower, found := s.onlineGameSession(member.UserID)
		if !found || follower == leader {
			return fmt.Errorf("party member %d has no resident game session before room transition", member.UserID)
		}
		followerState := s.runtimePartyStateForSession(follower)
		if followerState.PartyID != state.PartyID || followerState.UserID != state.UserID {
			return fmt.Errorf("party member %d changed party before room transition", member.UserID)
		}
		follower.dungeon.mu.Lock()
		err := validateRuntimePartyRoomOwner(follower.dungeon.runtime, source, dungeonID, mazeIndex, seed, instance)
		follower.dungeon.mu.Unlock()
		if err != nil {
			return fmt.Errorf("party member %d cannot follow room transition: %w", member.UserID, err)
		}
	}
	return nil
}

// Caller holds this member's dungeon lock. Validate both the session scene and
// its cached room/seed; matching coordinates alone can name a previous run.
func validateRuntimePartyRoomOwner(runtime *runtimeDungeonState, source runtimeDungeonRoomKey, dungeonID int64, mazeIndex int, seed uint32, instance *dungeonPartyInstance) error {
	if runtime == nil || runtime.Session == nil || runtime.Room == nil {
		return errDungeonWorldMapUnavailable
	}
	scene, ok := runtime.Session.Scene()
	if !ok || runtimeDungeonRoomKeyFromScene(scene) != source ||
		runtime.Dungeon.ID != dungeonID || runtime.MazeIndex != mazeIndex || runtime.Seed != seed ||
		(instance != nil && runtime.partyInstance != instance) {
		return fmt.Errorf("%w: party member source room/run differs from leader", errDungeonMoveRuntimeOwnerMismatch)
	}
	_, err := runtime.currentDungeonRoomVisit(scene)
	return err
}
