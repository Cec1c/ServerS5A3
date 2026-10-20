package dnfbridge

import (
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

func TestOfficial249DeathUsesNativeLayoutAndPreservesRoomOwnership(t *testing.T) {
	for _, kind := range []string{"valid", "unknown-target", "unknown-owner", "extension", "legacy-profile"} {
		t.Run(kind, func(t *testing.T) {
			service, runtime := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{job: "12", dungeonReference: tutorialScopeKnightFDungeonReference, mapDirectory: tutorialScopeKnightFMapDirectory, singleMonster: true})
			service.options.currentClientVersion = "official-24.9"
			if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
				t.Fatal(err)
			}
			target := runtime.Room.Snapshot().Monsters[0].ObjectKey
			conn := &bufferConn{}
			session := &gameSession{conn: conn, connID: "official249-real-owner-boundary-test", selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
			body := make([]byte, 88)
			binary.LittleEndian.PutUint32(body[0:4], target)
			binary.LittleEndian.PutUint16(body[4:6], 99)
			body[30] = 1
			switch kind {
			case "unknown-target":
				binary.LittleEndian.PutUint32(body[0:4], target+5000)
			case "unknown-owner":
				binary.LittleEndian.PutUint16(body[4:6], 60000)
			case "extension":
				body = append(body, 0)
			case "legacy-profile":
				service.options.currentClientVersion = "90cn-decode-bypass-v1"
			}
			if err := service.handleDungeonMonsterDeath(session, body); err != nil {
				t.Fatal(err)
			}
			state := runtime.Room.Snapshot().Monsters[0].State
			if kind == "valid" {
				if state != runtimeDungeonMonsterDefeated || conn.write.Len() == 0 {
					t.Fatalf("native death was not committed/notified: state=%v wrote=%d", state, conn.write.Len())
				}
				before := conn.write.Len()
				if err := service.handleDungeonMonsterDeath(session, body); err != nil {
					t.Fatal(err)
				}
				if runtime.Room.Snapshot().Monsters[0].State != runtimeDungeonMonsterDefeated {
					t.Fatal("replay changed defeated state")
				}
				if conn.write.Len() < before {
					t.Fatal("replay invalidated notification")
				}
			} else if state != runtimeDungeonMonsterAnnounced || conn.write.Len() != 0 {
				t.Fatalf("invalid death changed state: state=%v wrote=%d", state, conn.write.Len())
			}
		})
	}
	service := &Service{}
	legacy := make([]byte, dungeoncmd.DieMonsterRequestSize)
	got, err := service.decodeCurrentDungeonMonsterDeath(legacy)
	if err != nil || got.Layout != dungeoncmd.DieMonsterRequestLayoutFixed62 {
		t.Fatal("default legacy decoder changed")
	}
}
