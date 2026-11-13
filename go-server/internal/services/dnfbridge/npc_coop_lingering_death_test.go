package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

func retireCoopNPCThroughNativeDeath(t *testing.T, s *Service, session *gameSession, conn *bufferConn, key uint32) {
	t.Helper()
	// Keep the captured current-client zero-contribution layout and its tail.
	// Only remove its single 14-byte contribution and bind the actor key.
	body := friendlyAPCDeathBody(t, key, key, false)
	body = append(append([]byte(nil), body[:31]...), body[45:]...)
	body[30] = 0
	if err := s.handleDungeonMonsterDeath(session, body); err != nil {
		t.Fatalf("retire friendly actor: %v", err)
	}
	if _, ok := session.dungeon.runtime.Room.DeathResponseBody(key); !ok {
		t.Fatal("native retirement did not record its authoritative notification")
	}
	conn.write.Reset()
}

func TestCoopNPCLingeringContributionAfterConfirmedRetirementCompletesDeath(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "npc_only", true: "npc_and_player"}[mixed], func(t *testing.T) {
			s, session, conn, target, npc := prepareFriendlyAPCDeathTest(t)
			retireCoopNPCThroughNativeDeath(t, s, session, conn, npc)
			body := friendlyAPCDeathBody(t, target, npc, mixed)
			for repeat := 0; repeat < 2; repeat++ {
				if err := s.handleDungeonMonsterDeath(session, body); err != nil {
					t.Fatal(err)
				}
				packets := splitAllGameServerUpperPackets(t, conn.write.Bytes())
				if !seekerQST012HasOp38(packets, target) {
					t.Fatal("confirmed retired NPC contribution left monster waiting for death notification")
				}
				scene, _ := session.dungeon.runtime.Session.Scene()
				if !scene.Cleared {
					t.Fatal("lingering NPC kill did not complete the last hostile")
				}
				conn.write.Reset()
			}
		})
	}
}

func TestCoopNPCLingeringRetirementStillRequiresCurrentRoomAndOwnedSource(t *testing.T) {
	for _, name := range []string{"no_receipt", "wrong_receipt", "planned", "hostile", "unknown_key", "zero_contribution", "different_room", "tutorial", "other_visit"} {
		t.Run(name, func(t *testing.T) {
			s, session, conn, target, npc := prepareFriendlyAPCDeathTest(t)
			rt := session.dungeon.runtime
			if name == "no_receipt" || name == "wrong_receipt" {
				rt.Room.extendedActors[0].State = runtimeDungeonMonsterDefeated
				if name == "wrong_receipt" {
					rt.Room.RecordDeathResponseBody(npc+1, []byte{1})
				}
			} else {
				retireCoopNPCThroughNativeDeath(t, s, session, conn, npc)
			}
			body := friendlyAPCDeathBody(t, target, npc, false)
			switch name {
			case "planned":
				rt.Room.extendedActors[0].State = runtimeDungeonMonsterPlanned
			case "hostile":
				rt.Room.extendedActors[0].HostileReference = &worldmap.HostileReference{Kind: worldmap.HostileAICharacter}
			case "unknown_key":
				binary.LittleEndian.PutUint16(body[33:], 60000)
			case "zero_contribution":
				binary.LittleEndian.PutUint64(body[35:], 0)
			case "different_room":
				rt.Room.mapID++
			case "tutorial":
				rt.Dungeon.Path = "dungeon/tutorial/test.dgn"
			case "other_visit":
				old := dungeonDeathOwnerTestRoom(nil, []runtimeDungeonExtendedActor{rt.Room.extendedActors[0]})
				old.RecordDeathResponseBody(npc, []byte{1})
				rt.Rooms[runtimeDungeonRoomKey{X: 1, MapID: 101}] = &runtimeDungeonRoomVisit{Room: old}
				rt.Room.extendedActors = nil
				delete(rt.Room.extendedByObjectKey, npc)
			}
			if err := s.handleDungeonMonsterDeath(session, body); err != nil {
				t.Fatal(err)
			}
			if conn.write.Len() != 0 || rt.Room.Snapshot().Monsters[0].State != runtimeDungeonMonsterAnnounced {
				t.Fatal("unproven lingering source settled a death")
			}
		})
	}
}

func TestCoopNPCLingeringConfirmedDeathAwardsProgressionOnlyOnce(t *testing.T) {
	s, rt, repos := prepareCurrentDungeonMonsterExperienceTest(t)
	s.options.currentClientVersion = "official-24.9"
	npc := uint32(1000)
	rt.Room.extendedActors = append(rt.Room.extendedActors, runtimeDungeonExtendedActor{Kind: runtimeDungeonActorAICharacter, ObjectKey: npc, State: runtimeDungeonMonsterAnnounced})
	if rt.Room.extendedByObjectKey == nil {
		rt.Room.extendedByObjectKey = make(map[uint32]int)
	}
	rt.Room.extendedByObjectKey[npc] = len(rt.Room.extendedActors) - 1
	conn := &bufferConn{}
	session := &gameSession{conn: conn, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: rt}}
	retireCoopNPCThroughNativeDeath(t, s, session, conn, npc)
	target := rt.Room.Snapshot().Monsters[0].ObjectKey
	body := friendlyAPCDeathBody(t, target, npc, true)
	if err := s.handleDungeonMonsterDeath(session, body); err != nil {
		t.Fatal(err)
	}
	character, found, err := repos.Character.Load(context.Background(), "99")
	if err != nil || !found || character.Stats["exp"] != 200 {
		t.Fatalf("lingering NPC kill failed its one progression grant: exp=%d err=%v", character.Stats["exp"], err)
	}
	for repeat := 0; repeat < 2; repeat++ {
		if err := s.handleDungeonMonsterDeath(session, body); err != nil {
			t.Fatal(err)
		}
		after, _, err := repos.Character.Load(context.Background(), "99")
		if err != nil || !reflect.DeepEqual(character, after) {
			t.Fatal("lingering death replay changed durable progression")
		}
	}
}
