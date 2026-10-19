package worldmap

import "testing"

func TestProjectTournamentRoomActorsClonesOnlyTheStartRoom(t *testing.T) {
	start := RoomCoordinate{X: 0, Y: 0}
	boss := RoomCoordinate{X: 1, Y: 0}
	startParams := []int64{7, 8}
	bossActor := AICharacter{
		Code: 9002, Position: Point{X: 90, Y: 80}, Faction: "[monster]",
		AIType: "[normal]", Params: []int64{1, 2},
	}
	startMap := &ResolvedMap{Map: Map{
		ID: 100,
		Monsters: []MonsterSpawn{{
			MonsterID: 3001, Position: Point3{X: 10, Y: 20},
		}},
		AICharacters: []AICharacter{{
			Code: 4001, Position: Point{X: 30, Y: 40}, Faction: "[character]",
			AIType: "[normal]", Params: startParams,
		}},
		MonsterTeam: []int64{0},
	}}
	bossMap := &ResolvedMap{Map: Map{
		ID:           101,
		AICharacters: []AICharacter{bossActor},
	}}
	topology := &DungeonTopology{
		DungeonID: 700,
		Start:     &start,
		rooms: map[coordinateKey]DungeonRoom{
			{x: start.X, y: start.Y}: {Coordinate: start, Map: startMap, Start: true},
			{x: boss.X, y: boss.Y}:   {Coordinate: boss, Map: bossMap, Boss: true},
		},
		resolutionErrors: map[coordinateKey]error{},
	}
	projected := TournamentRoomActors{
		Monsters: []MonsterSpawn{{
			MonsterID: 5001, Level: 75, Position: Point3{X: 70, Y: 60}, Rank: "[boss]",
		}},
		AICharacters: []AICharacter{{
			Code: 6001, Level: 76, Position: Point{X: 70, Y: 60}, Faction: "[monster]",
			AIType: "[boss]", Params: []int64{3, 4}, Blocking: true,
		}},
	}
	if err := topology.ProjectTournamentRoomActors(start, projected); err != nil {
		t.Fatal(err)
	}

	room, ok := topology.Room(start)
	if !ok || room.Map == nil {
		t.Fatal("projected start room is missing")
	}
	if len(room.Map.Map.Monsters) != 1 || room.Map.Map.Monsters[0].MonsterID != 5001 ||
		len(room.Map.Map.AICharacters) != 1 || room.Map.Map.AICharacters[0].Code != 6001 ||
		!room.Map.Map.AICharacters[0].Blocking || room.Map.Map.AICharacters[0].Level != 76 {
		t.Fatalf("projected actors=%+v/%+v", room.Map.Map.Monsters, room.Map.Map.AICharacters)
	}
	if len(room.Map.Map.MonsterTeam) != 0 {
		t.Fatalf("projected monster team=%v want empty", room.Map.Map.MonsterTeam)
	}

	// Mutating the projected room must not leak into the resolver's original map,
	// its slice backing arrays, or the unrelated boss room.
	room.Map.Map.AICharacters[0].Params[0] = 99
	room.Map.Map.Monsters[0].Level = 99
	if startMap.Map.AICharacters[0].Params[0] != 7 ||
		startMap.Map.Monsters[0].Level != 0 ||
		startMap.Map.Monsters[0].MonsterID != 3001 ||
		startMap.Map.AICharacters[0].Code != 4001 {
		t.Fatalf("projection mutated the resolver-owned start map: %+v", startMap.Map)
	}
	bossRoom, ok := topology.Room(boss)
	if !ok || bossRoom.Map == nil || len(bossRoom.Map.Map.AICharacters) != 1 ||
		bossRoom.Map.Map.AICharacters[0].Code != 9002 ||
		len(bossRoom.Map.Map.AICharacters[0].Params) != 2 ||
		bossRoom.Map.Map.AICharacters[0].Params[1] != 2 {
		t.Fatalf("projection leaked into boss room: %+v", bossRoom.Map)
	}
}
