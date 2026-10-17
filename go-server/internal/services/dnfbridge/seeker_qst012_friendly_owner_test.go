package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestSeekerQST012FriendlyOwnerProof(t *testing.T) {
	tests := []struct {
		name              string
		cross             bool
		mutate            func(*runtimeDungeonState, *worldmap.DungeonRoomScene, *runtimeDungeonRoom, *worldmap.DungeonRoomScene, *dungeoncmd.DieMonsterRequest)
		want              bool
		wantExistingOwner bool
	}{
		{name: "current team 0 monster", want: true},
		{name: "current team 0 monster without follow", want: true},
		{name: "current hostile target as owner", mutate: func(rt *runtimeDungeonState, _ *worldmap.DungeonRoomScene, _ *runtimeDungeonRoom, _ *worldmap.DungeonRoomScene, request *dungeoncmd.DieMonsterRequest) {
			request.OwnerObjectKey = uint16(rt.Room.monsters[0].ObjectKey)
		}, wantExistingOwner: true},
		{name: "current planned monster", mutate: func(rt *runtimeDungeonState, _ *worldmap.DungeonRoomScene, _ *runtimeDungeonRoom, _ *worldmap.DungeonRoomScene, _ *dungeoncmd.DieMonsterRequest) {
			rt.Room.monsters[1].State = runtimeDungeonMonsterPlanned
		}},
		{name: "unknown owner", mutate: func(_ *runtimeDungeonState, _ *worldmap.DungeonRoomScene, _ *runtimeDungeonRoom, _ *worldmap.DungeonRoomScene, request *dungeoncmd.DieMonsterRequest) {
			request.OwnerObjectKey = 60000
		}},
		{name: "cross-room followed team 0 monster", cross: true, mutate: func(_ *runtimeDungeonState, _ *worldmap.DungeonRoomScene, _ *runtimeDungeonRoom, oldScene *worldmap.DungeonRoomScene, _ *dungeoncmd.DieMonsterRequest) {
			oldScene.Map.Map.MonsterFollowType[1] = "follow"
		}, want: true},
		{name: "cross-room team 0 monster without follow", cross: true},
		{name: "cross-room follow at wrong monster index", cross: true, mutate: func(_ *runtimeDungeonState, _ *worldmap.DungeonRoomScene, _ *runtimeDungeonRoom, oldScene *worldmap.DungeonRoomScene, _ *dungeoncmd.DieMonsterRequest) {
			oldScene.Map.Map.MonsterFollowType = []string{"follow", "none"}
		}},
		{name: "cross-room hostile monster with follow", cross: true, mutate: func(_ *runtimeDungeonState, _ *worldmap.DungeonRoomScene, _ *runtimeDungeonRoom, oldScene *worldmap.DungeonRoomScene, _ *dungeoncmd.DieMonsterRequest) {
			oldScene.Map.Map.MonsterTeam[1] = 100
			oldScene.Map.Map.MonsterFollowType[1] = "follow"
		}},
		{name: "cross-room planned follower", cross: true, mutate: func(_ *runtimeDungeonState, _ *worldmap.DungeonRoomScene, old *runtimeDungeonRoom, oldScene *worldmap.DungeonRoomScene, _ *dungeoncmd.DieMonsterRequest) {
			oldScene.Map.Map.MonsterFollowType[1] = "follow"
			old.monsters[0].State = runtimeDungeonMonsterPlanned
		}},
		{name: "cross-room target", cross: true, mutate: func(_ *runtimeDungeonState, _ *worldmap.DungeonRoomScene, _ *runtimeDungeonRoom, oldScene *worldmap.DungeonRoomScene, request *dungeoncmd.DieMonsterRequest) {
			oldScene.Map.Map.MonsterFollowType[1] = "follow"
			request.RuntimeObjectKey = uint32(request.OwnerObjectKey)
		}},
		{name: "cross-room unbound follower", cross: true, mutate: func(_ *runtimeDungeonState, _ *worldmap.DungeonRoomScene, _ *runtimeDungeonRoom, oldScene *worldmap.DungeonRoomScene, request *dungeoncmd.DieMonsterRequest) {
			oldScene.Map.Map.MonsterFollowType[1] = "follow"
			delete(oldScene.RuntimeObjects, uint32(request.OwnerObjectKey))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, session, conn, _, _ := prepareFriendlyMonsterDeathTest(t)
			rt := session.dungeon.runtime
			scene, _ := rt.Session.Scene()
			target := rt.Room.monsters[0].ObjectKey
			owner := rt.Room.monsters[1].ObjectKey
			request, err := s.decodeCurrentDungeonMonsterDeath(seekerQST012DeathBody(t, target, owner, 0x211))
			if err != nil {
				t.Fatal(err)
			}
			var old *runtimeDungeonRoom
			var oldScene worldmap.DungeonRoomScene
			if test.cross {
				friendly := rt.Room.monsters[1]
				rt.Room.monsters = rt.Room.monsters[:1]
				delete(rt.Room.byObjectKey, owner)
				old = dungeonDeathOwnerTestRoom([]runtimeDungeonMonster{friendly}, nil)
				old.coordinate = worldmap.RoomCoordinate{X: 1}
				old.mapID = 101
				oldScene = scene
				oldScene.Coordinate = old.coordinate
				oldScene.Map.Map.ID = old.mapID
				oldScene.Map.Map.MonsterTeam = append([]int64(nil), scene.Map.Map.MonsterTeam...)
				oldScene.Map.Map.MonsterFollowType = []string{"none", "none"}
				rt.Rooms = map[runtimeDungeonRoomKey]*runtimeDungeonRoomVisit{
					{X: 1, MapID: 101}: {Room: old, Scene: oldScene},
				}
			} else {
				scene.Map.Map.MonsterFollowType = nil
			}
			if test.mutate != nil {
				test.mutate(rt, &scene, old, &oldScene, &request)
			}
			got := s.currentDungeonFriendlyOwnerDeath(rt, scene, request)
			if got != test.want {
				t.Fatalf("friendly owner proof=%t want=%t request=%+v", got, test.want, request)
			}
			conn.write.Reset()
			body := seekerQST012DeathBody(t, request.RuntimeObjectKey, uint32(request.OwnerObjectKey), 0x211)
			if err := s.handleDungeonMonsterDeath(session, body); err != nil {
				t.Fatalf("handle unproven/proven owner: %v", err)
			}
			if test.want || test.wantExistingOwner {
				if _, defeated := rt.Room.DefeatedDeathTarget(request.RuntimeObjectKey); !defeated || conn.write.Len() == 0 {
					t.Fatalf("proven owner death defeated=%t response_bytes=%d", defeated, conn.write.Len())
				}
				return
			}
			targetRoom := rt.Room
			if test.cross && request.RuntimeObjectKey == uint32(request.OwnerObjectKey) {
				targetRoom = old
			}
			if state := targetRoom.Snapshot().Monsters[0].State; state != runtimeDungeonMonsterAnnounced {
				t.Fatalf("unproven owner changed target state=%s", state)
			}
			if conn.write.Len() != 0 {
				t.Fatalf("unproven owner produced response bytes=%d", conn.write.Len())
			}
		})
	}
}
