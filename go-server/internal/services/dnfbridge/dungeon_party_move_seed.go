package dnfbridge

import "fmt"

// The shared room owns the seed before any member sends its first op29. A
// member can legitimately arrive ahead of the leader; per-session RNG choices
// must not split that same room. Cached visits retain their original seed.
func (runtime *runtimeDungeonState) freezePartyDungeonMoveSeed(plan *currentDungeonMovePlan) error {
	if runtime == nil || runtime.partyInstance == nil || plan.LayerBaseAck {
		return nil
	}
	instance := runtime.partyInstance
	key := runtimeDungeonRoomKeyFromScene(plan.Target)
	instance.mu.Lock()
	defer instance.mu.Unlock()
	shared, found := instance.rooms[key]
	if !found || shared.Room == nil {
		return fmt.Errorf("%w: shared target room missing before seed binding", errDungeonMoveRuntimeOwnerMismatch)
	}
	// Two planners can both observe a not-yet-registered room. The instance's
	// first registered actor ledger wins before either packet is written.
	plan.TargetRoom = shared.Room
	plan.NextObjectKey = max(plan.NextObjectKey, instance.nextObjectKey)
	if shared.MoveSeedKnown {
		plan.Seed = shared.MoveSeed
	} else {
		shared.MoveSeed, shared.MoveSeedKnown = plan.Seed, true
		instance.rooms[key] = shared
	}
	return nil
}

// Requires the member's dungeon lock. Only an exact current instance, room
// object, cached visit and seed can satisfy an already completed follow plan.
func runtimePartyMovePlanCommitted(runtime *runtimeDungeonState, plan runtimePartyDungeonMovePlan) bool {
	if runtime == nil || runtime.Session == nil || plan.instance == nil || plan.room == nil ||
		runtime.partyInstance != plan.instance || runtime.Room != plan.room || runtime.Seed != plan.seed {
		return false
	}
	scene, ok := runtime.Session.Scene()
	if !ok || runtimeDungeonRoomKeyFromScene(scene) != plan.target {
		return false
	}
	if _, err := runtime.currentDungeonRoomVisit(scene); err != nil {
		return false
	}
	instance := runtime.partyInstance
	instance.mu.Lock()
	defer instance.mu.Unlock()
	shared := instance.rooms[plan.target]
	return shared.Room == plan.room && shared.MoveSeedKnown && shared.MoveSeed == plan.seed &&
		instance.memberRuntimes[characterIDOfRuntimeDungeonState(runtime)] == runtime
}
