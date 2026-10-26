package dnfbridge

import "testing"

// A retired socket can finish its town/character cleanup after the replacement
// has joined the same shared room. That cleanup owns the old runtime only.
func TestR185PartyInstanceLateRetirementPreservesReplacement(t *testing.T) {
	for _, publicCleanup := range []bool{false, true} {
		t.Run(map[bool]string{false: "release_boundary", true: "character_select"}[publicCleanup], func(t *testing.T) {
			service, leader, old, leaderRuntime, oldRuntime, scene := partyDungeonInstanceFixture(t, 7, 4242)
			instance := service.adoptPartyDungeonInstance(leader, leaderRuntime, scene)
			if service.adoptPartyDungeonInstance(old, oldRuntime, scene) != instance {
				t.Fatal("old member did not join leader")
			}
			replacement := &gameSession{conn: &bufferConn{}, channel: old.channel, selectedCharacterID: old.selectedCharacterID, characterGeneration: old.characterGeneration + 1}
			storeRuntimePartyState(replacement, runtimePartyStateSnapshot(old))
			replacementRuntime := &runtimeDungeonState{
				Dungeon: oldRuntime.Dungeon, MazeIndex: oldRuntime.MazeIndex, Seed: oldRuntime.Seed,
				Room: partyDungeonInstanceRoom(scene, 501, 502), NextObjectKey: 600,
			}
			if service.adoptPartyDungeonInstance(replacement, replacementRuntime, scene) != instance {
				t.Fatal("replacement did not join the existing shared room")
			}
			if publicCleanup {
				old.dungeon.runtime = oldRuntime
				service.bindGameSessionCharacter(replacement, old.selectedCharacterID)
				service.promoteResidentGameSession(replacement, old.selectedCharacterID)
				service.resetGameSessionForCharacterSelect(old)
			} else {
				service.releasePartyDungeonInstance(oldRuntime, old.selectedCharacterID, "return_select_character")
			}
			if oldRuntime.partyInstance != nil {
				t.Fatal("retired runtime retained its instance attachment")
			}
			peers := service.partyDungeonInstancePeers(leaderRuntime, leader.selectedCharacterID)
			if len(peers) != 1 || peers[0].Session != replacement {
				t.Fatalf("late cleanup removed replacement death-notification target: %+v", peers)
			}
			if !dungeonPartyInstanceHasMember(instance, replacement.selectedCharacterID) {
				t.Fatal("late cleanup removed replacement from hit ownership")
			}
			service.releasePartyDungeonInstance(replacementRuntime, replacement.selectedCharacterID, "back_to_village_committed")
			if instance.memberCount() != 1 {
				t.Fatal("current replacement must still be able to leave")
			}
			service.releasePartyDungeonInstance(leaderRuntime, leader.selectedCharacterID, "back_to_village_committed")
			if service.partyDungeonInstanceCount() != 0 {
				t.Fatal("last current member must recycle the instance")
			}
		})
	}
}
