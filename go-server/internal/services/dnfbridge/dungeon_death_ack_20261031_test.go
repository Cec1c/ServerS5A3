package dnfbridge

import (
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

func TestDungeonDeathAckSpecialMonsterSentinelCombatEntry(t *testing.T) {
	fixture := newHitOwnerFixture(t)
	runtime := fixture.leader.dungeon.runtime
	const specialObjectKey uint32 = 9001
	plan := runtimeDungeonExtendedActorPlan{
		Actors: []runtimeDungeonExtendedActor{{
			Kind:      runtimeDungeonActorSpecialMonster,
			ObjectKey: specialObjectKey,
			Packet: currentDungeonStartMapActor{
				ObjectKey: uint16(specialObjectKey),
				Code:      3001,
				Level:     20,
				Type:      0,
				Blocking:  0,
			},
			State: runtimeDungeonMonsterAnnounced,
		}},
		NextObjectKey:      specialObjectKey + 1,
		SpecialObjectCount: 1,
	}
	if err := runtime.Room.AttachExtendedActors(plan); err != nil {
		t.Fatal(err)
	}
	fixture.leaderConn.write.Reset()

	if err := fixture.service.handleDungeonMonsterDeath(
		fixture.leader,
		twoAttackerDieMonsterBody(specialObjectKey, ^uint16(0), 1),
	); err != nil {
		t.Fatal(err)
	}
	text := fixture.readLog(t)
	if got := countNotifyDieMonsterFrames(t, fixture.leaderConn); got == 0 {
		t.Fatalf("special monster owner-sentinel death did not receive op38\n%s", text)
	}
	snapshot := runtime.Room.Snapshot()
	if len(snapshot.ExtendedActors) != 1 ||
		snapshot.ExtendedActors[0].State != runtimeDungeonMonsterDefeated {
		t.Fatalf("special monster was not authoritatively retired: %+v", snapshot.ExtendedActors)
	}
	if strings.Contains(text, "reason=non_hostile_ai_request_shape_unproven") ||
		strings.Contains(text, "reason=room_related_actor_object_key_mismatch") {
		t.Fatalf("special monster still hit a generic owner gate\n%s", text)
	}
	if !strings.Contains(text, "kind=game-dungeon-special-monster-retired") {
		t.Fatalf("special monster retirement event missing\n%s", text)
	}
}

func TestDungeonDeathAckRecentlyDepartedPartyOwner(t *testing.T) {
	fixture := newHitOwnerFixture(t)
	state := runtimePartyStateSnapshot(fixture.leader)
	state.Members = []alignedcmd.PartyMemberState{state.Members[0]}
	storeRuntimePartyState(fixture.leader, state)
	fixture.service.releasePartyDungeonInstance(
		fixture.member.dungeon.runtime,
		fixture.member.selectedCharacterID,
		"back_to_village_committed",
	)
	fixture.leaderConn.write.Reset()

	if err := fixture.service.handleDungeonMonsterDeath(
		fixture.leader,
		twoAttackerDieMonsterBody(fixture.monsterKeys[0], fixture.member.selectedCharacterID, 0),
	); err != nil {
		t.Fatal(err)
	}
	if got := countNotifyDieMonsterFrames(t, fixture.leaderConn); got == 0 {
		t.Fatal("recently departed same-instance owner did not receive op38")
	}
	text := fixture.readLog(t)
	for _, want := range []string{
		"kind=game-dungeon-party-hit-accepted",
		"attacker_char_id=100",
		"attacker_is_party_member=false",
		"attacker_recently_departed=true",
		"gate=recently_departed_party_peer_same_instance",
		"reason=recently_departed_party_peer_in_same_dungeon_instance",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("accepted departed-owner event is missing %q\n%s", want, text)
		}
	}
}

func TestDungeonDeathAckDepartedPartyOwnerExpires(t *testing.T) {
	fixture := newHitOwnerFixture(t)
	state := runtimePartyStateSnapshot(fixture.leader)
	state.Members = []alignedcmd.PartyMemberState{state.Members[0]}
	storeRuntimePartyState(fixture.leader, state)
	fixture.service.releasePartyDungeonInstance(
		fixture.member.dungeon.runtime,
		fixture.member.selectedCharacterID,
		"back_to_village_committed",
	)
	instance := fixture.leader.dungeon.runtime.partyInstance
	if instance == nil {
		t.Fatal("leader lost the shared instance")
	}
	instance.mu.Lock()
	instance.departedOwners[fixture.member.selectedCharacterID] = time.Now().Add(-time.Second)
	instance.mu.Unlock()
	fixture.leaderConn.write.Reset()

	if err := fixture.service.handleDungeonMonsterDeath(
		fixture.leader,
		twoAttackerDieMonsterBody(fixture.monsterKeys[0], fixture.member.selectedCharacterID, 0),
	); err != nil {
		t.Fatal(err)
	}
	if got := countNotifyDieMonsterFrames(t, fixture.leaderConn); got != 0 {
		t.Fatalf("expired departed-owner witness sent %d op38 frames, want 0", got)
	}
	text := fixture.readLog(t)
	if !strings.Contains(text, "reason=room_related_actor_object_key_mismatch") {
		t.Fatalf("expired witness did not fall back to the owner gate\n%s", text)
	}
}
