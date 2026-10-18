package dnfbridge

import (
	"context"
	"reflect"
	"testing"
)

// Exercise the production route arm, typed transition and stream callbacks.
// The old lifetime generation guard silently suppresses every later selection
// after this same connection has ever completed a town transition.
func TestLoginActorGenerationReselectAfterTownHistory(t *testing.T) {
	t.Setenv(loginActorGenerationRebuildEnv, "")
	t.Setenv(loginEquipmentSceneReadyReplayEnv, loginEquipmentSceneReadyReplayBeforeTransitionOnly)
	t.Setenv(skillSwitchInventoryModeEnv, skillSwitchInventoryBeforeTransitionTier)
	service, session, connection, _ := newSceneReadyReplayFixture(t)
	repos, _ := service.repositoryGroup()
	before, found, err := repos.Equipment.Load(context.Background(), "6")
	if err != nil || !found {
		t.Fatalf("equipment fixture found=%v err=%v", found, err)
	}
	// A real production town-transition arm leaves its monotonic generation
	// behind even after a new selector route resets the pending owner.
	service.armCurrentTownPostTransitionPlayerState(session, "prior_town_movement")
	history := session.townPostTransition.generation
	if history == 0 {
		t.Fatal("prior movement did not allocate a generation")
	}
	for entry := 0; entry < 2; entry++ {
		service.armCurrentInitialTownRoute(session, session.selectedCharacterID)
		armLoginRouteUpToOp24(session)
		connection.write.Reset()
		body, err := buildCurrentSceneTransitionBody(31, 2, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.sendCurrentTownTransitionLocked(session, 6, 31, 2, body); err != nil {
			t.Fatal(err)
		}
		if err := service.handleGameStreamPacket(session, loginActorGenerationLegacyStream(buildLegacyTownSceneReadyAckPacket())); err != nil {
			t.Fatal(err)
		}
		if got := len(loginActorGenerationOp2Frames(t, connection.write.Bytes())); got != 0 {
			t.Fatalf("scene-ready callback rebuilt actor early: %d", got)
		}
		connection.write.Reset()
		if err := service.handleGameStreamPacket(session, loginActorGenerationLegacyStream(loginActorGenerationClientPacket())); err != nil {
			t.Fatal(err)
		}
		frames := loginActorGenerationOp2Frames(t, connection.write.Bytes())
		if len(frames) != 2 || frames[0].mode != 0 || frames[1].mode != 1 {
			t.Fatalf("selection %d after town history emitted %d actor frames, want mode0 and equipment mode1", entry, len(frames))
		}
		count, _, _, _, weapon, ok := loginActorGenerationMode1WireRowAudit(frames[1].body)
		wantWeapon := []any{"actor_weapon", "present", "actor_weapon_slot", "12", "actor_weapon_item_id", "101040493", "actor_weapon_durability_sent", "48"}
		if !ok || count != 31 || !reflect.DeepEqual(weapon, wantWeapon) {
			t.Fatalf("selection %d lost authoritative worn weapon: rows=%d audit=%v weapon=%+v", entry, count, ok, weapon)
		}
		written := connection.write.Len()
		if err := service.handleGameStreamPacket(session, loginActorGenerationLegacyStream(loginActorGenerationClientPacket())); err != nil {
			t.Fatal(err)
		}
		if connection.write.Len() != written {
			t.Fatal("second client packet rebuilt the same actor again")
		}
		if session.townPostTransition.generation != history {
			t.Fatal("selection reset or reused the monotonic town generation")
		}
	}
	after, found, err := repos.Equipment.Load(context.Background(), "6")
	if err != nil || !found || !reflect.DeepEqual(before, after) {
		t.Fatal("projection repair changed stored equipment")
	}
}

func TestLoginActorGenerationReselectStillDefersToNewTownTransition(t *testing.T) {
	for _, resetPending := range []bool{false, true} {
		t.Run(map[bool]string{false: "active_town_owner", true: "retired_new_town_owner"}[resetPending], func(t *testing.T) {
			t.Setenv(loginActorGenerationRebuildEnv, "")
			service, session, connection, _ := newSceneReadyReplayFixture(t)
			service.armCurrentTownPostTransitionPlayerState(session, "previous_selection")
			service.armCurrentInitialTownRoute(session, session.selectedCharacterID)
			armLoginRouteUpToOp24(session)
			session.initialTownRouteStage = currentInitialTownRouteTransitionSent
			session.initialTownLegacySceneReadyAccepted = true
			service.armLoginActorGenerationRebuild(session)
			service.armCurrentTownPostTransitionPlayerState(session, "new_transition_after_selection")
			if resetPending {
				resetCurrentTownPostTransitionPlayerState(session)
			}
			connection.write.Reset()
			if err := service.handleGameStreamPacket(session, loginActorGenerationLegacyStream(loginActorGenerationClientPacket())); err != nil {
				t.Fatal(err)
			}
			if len(loginActorGenerationOp2Frames(t, connection.write.Bytes())) != 0 {
				t.Fatal("login rebuilt an actor owned by a newer town transition")
			}
		})
	}
}
