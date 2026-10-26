package dnfbridge

import (
	"fmt"
	"reflect"
	"testing"
)

func TestTournamentDeathHandlerRejectsFutureRoundBeforeMutation(t *testing.T) {
	for _, index := range []int{1, 3} {
		t.Run(fmt.Sprintf("round-%d", index+1), func(t *testing.T) {
			t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
			runtime, _ := tournamentStartMapBlockingFixture(t)
			if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
				t.Fatal(err)
			}
			connection := &bufferConn{}
			session := &gameSession{conn: connection, selectedCharacterID: 99}
			session.dungeon.runtime = runtime
			service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
			before := runtime.Room.Snapshot()
			sceneBefore, _ := runtime.Session.Scene()
			key := before.Monsters[index].ObjectKey
			if err := service.handleDungeonMonsterDeath(session, tutorialScopeVariableZeroCombatDeathBody(key)); err != nil {
				t.Fatal(err)
			}
			after := runtime.Room.Snapshot()
			sceneAfter, _ := runtime.Session.Scene()
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(sceneBefore, sceneAfter) {
				t.Fatalf("future round %d mutated room or scene before admission", index+1)
			}
			if connection.write.Len() != 0 || runtime.Tournament.currentRoundNumber() != 1 || runtime.tournamentReward != nil {
				t.Fatal("future round death emitted a response, advanced the round, or created rewards")
			}
			// The refused report must not poison a later valid current-round report.
			if err := service.handleDungeonMonsterDeath(session, tutorialScopeVariableZeroCombatDeathBody(before.Monsters[0].ObjectKey)); err != nil {
				t.Fatal(err)
			}
			if runtime.Room.Snapshot().Monsters[0].State != runtimeDungeonMonsterDefeated || runtime.Tournament.currentRoundNumber() != 2 {
				t.Fatal("current round death no longer advances normally")
			}
			if runtime.Room.Snapshot().Monsters[index].State != runtimeDungeonMonsterAnnounced {
				t.Fatal("future actor was silently retired after the valid death")
			}
		})
	}
}

func TestTournamentDeathAdmissionPreservesOtherRoomAndOrdinaryDeaths(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(fmt.Sprintf("foreign-%t", foreign), func(t *testing.T) {
			t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
			runtime, _ := tournamentStartMapBlockingFixture(t)
			if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
				t.Fatal(err)
			}
			if foreign {
				definition := *runtime.Tournament.Definition
				definition.MapID++
				runtime.Tournament.Definition = &definition
			} else {
				runtime.Tournament = nil
			}
			session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 99}
			session.dungeon.runtime = runtime
			service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
			key := runtime.Room.Snapshot().Monsters[1].ObjectKey
			if err := service.handleDungeonMonsterDeath(session, tutorialScopeVariableZeroCombatDeathBody(key)); err != nil {
				t.Fatal(err)
			}
			if runtime.Room.Snapshot().Monsters[1].State != runtimeDungeonMonsterDefeated {
				t.Fatal("tournament admission intercepted an ordinary or other-room death")
			}
		})
	}
}
