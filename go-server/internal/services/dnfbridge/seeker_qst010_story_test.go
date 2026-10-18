package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const seekerQST010PVFChecksum = "fef4f46f6c3096d10cd88c84579dea8bd4fffa18e5a75b4684a2e920864f5525"

type seekerQST010PVF struct {
	source   dnfpvf.Source
	table    *worldmap.Table
	resolver *worldmap.Resolver
	quests   *dnfquest.Catalog
	monsters *pvfDungeonMonsterCatalog
	ai       *pvfDungeonAICharacterCatalog
}

func loadSeekerQST010PVF(t *testing.T) seekerQST010PVF {
	t.Helper()
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the matching Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := archive.Snapshot().Checksum; got != seekerQST010PVFChecksum {
		t.Fatalf("PVF checksum=%s", got)
	}
	ctx := context.Background()
	table, err := worldmap.LoadSource(ctx, archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Build(ctx, archive, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	quests, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	ai, err := newPVFDungeonAICharacterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	return seekerQST010PVF{
		source: archive, table: table, resolver: resolver, quests: quests, monsters: monsters, ai: ai,
	}
}

func TestSeekerQST010RealPVFIdentity(t *testing.T) {
	pvf := loadSeekerQST010PVF(t)

	south, ok := pvf.quests.Find(3525)
	if !ok || south.Name != "奇怪的兵器" || south.Type != "[quest clear]" ||
		!reflect.DeepEqual(south.IntData, []int64{3558, 3559, 3560}) {
		t.Fatalf("south parent quest=%+v found=%t", south, ok)
	}
	southClear, ok := pvf.quests.Find(3560)
	if !ok || southClear.Name != "奇怪的兵器" || southClear.Type != "[clear map]" ||
		!reflect.DeepEqual(southClear.IntData, []int64{92247}) || southClear.MainQuestID != 3525 {
		t.Fatalf("south clear quest=%+v found=%t", southClear, ok)
	}
	defense, ok := pvf.quests.Find(3538)
	if !ok || defense.Name != "夜战司令官巴比伦" || defense.Type != "[clear map]" ||
		!reflect.DeepEqual(defense.IntData, []int64{92342}) {
		t.Fatalf("defense quest=%+v found=%t", defense, ok)
	}
	pursuit, ok := pvf.quests.Find(3553)
	if !ok || pursuit.Name != "大决战" || pursuit.Type != "[condition under clear]" ||
		!reflect.DeepEqual(pursuit.IntData, []int64{85, -1, -1}) {
		t.Fatalf("pursuit quest=%+v found=%t", pursuit, ok)
	}

	for _, identity := range []struct {
		dungeon int64
		maze    int
		quest   int64
		name    string
	}{
		{82, 2, 3560, "根特南门"},
		{89, 3, 3538, "根特防御战"},
		{85, 5, 3553, "追击歼灭战"},
	} {
		dungeon, ok := pvf.table.FindDungeon(identity.dungeon)
		if !ok || dungeon.Metadata.Name != identity.name || identity.maze >= len(dungeon.Mazes) ||
			!realMazeConnectsQuest(dungeon.Mazes[identity.maze], identity.quest) {
			t.Fatalf("dungeon identity=%+v found=%t dungeon=%+v", identity, ok, dungeon)
		}
	}

	southMap, ok := pvf.table.FindMap(92247)
	if !ok || len(southMap.Monsters) != 6 || len(southMap.AICharacters) != 1 ||
		southMap.AICharacters[0].Code != 10448 ||
		normalizeDungeonPVFSymbol(southMap.AICharacters[0].Faction) != "character" {
		t.Fatalf("south map=%+v found=%t", southMap, ok)
	}
	southAI, found, err := pvf.ai.Find(10448)
	if err != nil || !found || southAI.Name != "皇都军队的机关师" {
		t.Fatalf("south APC=%+v found=%t err=%v", southAI, found, err)
	}

	defenseMap, ok := pvf.table.FindMap(92342)
	if !ok || len(defenseMap.AICharacters) != 3 {
		t.Fatalf("defense map=%+v found=%t", defenseMap, ok)
	}
	hostileBabylon := 0
	for _, actor := range defenseMap.AICharacters {
		if actor.Code == 10409 && normalizeDungeonPVFSymbol(actor.Faction) == "monster" {
			hostileBabylon++
		}
	}
	if hostileBabylon != 1 {
		t.Fatalf("defense hostile Babylon count=%d actors=%+v", hostileBabylon, defenseMap.AICharacters)
	}
	defenseEventMap, ok := pvf.table.FindMap(92334)
	if !ok {
		t.Fatal("defense cinematic map 92334 missing")
	}
	defenseHostile, defenseAllies := 0, 0
	for _, actor := range defenseEventMap.AICharacters {
		switch {
		case actor.Code == 10409 && normalizeDungeonPVFSymbol(actor.Faction) == "monster":
			defenseHostile++
		case normalizeDungeonPVFSymbol(actor.Faction) == "character":
			defenseAllies++
		}
	}
	if defenseHostile != 1 || defenseAllies != 2 {
		t.Fatalf("defense map=92334 hostile=%d allies=%d actors=%+v",
			defenseHostile, defenseAllies, defenseEventMap.AICharacters)
	}

	pursuitMap, ok := pvf.table.FindMap(92472)
	if !ok || len(pursuitMap.Monsters) != 2 || len(pursuitMap.AICharacters) != 2 {
		t.Fatalf("pursuit map=%+v found=%t", pursuitMap, ok)
	}
	if pursuitMap.Monsters[0].MonsterID != 65502 ||
		normalizeDungeonPVFSymbol(pursuitMap.Monsters[0].Rank) != "boss" {
		t.Fatalf("pursuit boss=%+v", pursuitMap.Monsters[0])
	}
	dummy := pursuitMap.Monsters[1]
	if dummy.MonsterID != 63821 ||
		normalizeDungeonPVFSymbol(dummy.Rank) != "dummy" ||
		normalizeDungeonPVFSymbol(dummy.SuffixMarker) != "boss" {
		t.Fatalf("pursuit dummy=%+v", dummy)
	}
	var niver bool
	for _, actor := range pursuitMap.AICharacters {
		if actor.Code != 10449 {
			continue
		}
		niver = normalizeDungeonPVFSymbol(actor.Faction) == "character"
	}
	niverDefinition, found, err := pvf.ai.Find(10449)
	if err != nil || !found || niverDefinition.Name != "尼贝尔" || !niver {
		t.Fatalf("Niver=%+v found=%t friendly=%t err=%v", niverDefinition, found, niver, err)
	}

	// South Gate's three story mazes contain no randomized-object script. The
	// North Gate uses the already-handled AT-5T MOB 61235 in its random-object
	// scripts, so the two reported locations are not the same vehicle owner.
	southDungeon, _ := pvf.table.FindDungeon(82)
	for _, maze := range southDungeon.Mazes {
		if len(maze.RandomizedObjects) != 0 {
			t.Fatalf("South Gate maze=%d unexpectedly has randomized objects=%+v", maze.Index, maze.RandomizedObjects)
		}
	}
	northDungeon, _ := pvf.table.FindDungeon(88)
	northVehicleGroups := 0
	for _, maze := range northDungeon.Mazes {
		for _, group := range maze.RandomizedObjects {
			for _, object := range group.Objects {
				if object.ObjectIndex.Set && object.ObjectIndex.Value == 61235 {
					northVehicleGroups++
				}
			}
		}
	}
	if northVehicleGroups == 0 {
		t.Fatal("North Gate AT-5T 61235 randomized-object declaration is missing")
	}

	t.Logf(
		"south quest=3525/%d map=92247 APC=10448; defense quest=3538 target=92342 cinematic=92334 hostileBabylon=%d/friendly=2; pursuit quest=3553 map=92472 Niver=10449 boss=65502 dummy=63821; NorthGate 61235 groups=%d",
		southClear.ID, hostileBabylon, northVehicleGroups,
	)
}

func TestSeekerQST010RealPVFSceneClearGates(t *testing.T) {
	pvf := loadSeekerQST010PVF(t)
	for _, test := range []struct {
		name        string
		parentQuest int64
		prereq      int64
		mazeQuest   int64
		dungeonID   uint32
		mazeIndex   int
		targetMap   int64
		level       int
		allyCode    int64
		expectMobs  map[int64]int
		passGate    bool
	}{
		{
			name:        "south-gate-story",
			parentQuest: 3525, prereq: 3524, mazeQuest: 3560,
			dungeonID: 82, mazeIndex: 2, targetMap: 92247, level: 65,
			allyCode: 10448, expectMobs: map[int64]int{61801: 6}, passGate: true,
		},
		{
			name:        "pursuit-final-niver",
			parentQuest: 3553, prereq: 3552, mazeQuest: 3553,
			dungeonID: 85, mazeIndex: 5, targetMap: 92472, level: 70,
			allyCode: 10449, expectMobs: map[int64]int{65502: 1, 63821: 1},
		},
	} {
		for _, report := range []string{"scripted-empty", "friendly-key", "friendly-contribution"} {
			t.Run(test.name+"/"+report, func(t *testing.T) {
				ctx := context.Background()
				repositories := dnfrepomemory.NewMemoryGroup()
				if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
					CharacterID: "19", AccountID: "dnf:1", Job: "2", Level: test.level,
					Stats: map[string]int64{"fatigue": 156},
				}); err != nil {
					t.Fatal(err)
				}
				if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
					CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{},
				}); err != nil {
					t.Fatal(err)
				}
				if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
					CharacterID: "19",
					States:      map[int64]dnfrepo.QuestState{test.prereq: {Status: "completed"}},
				}); err != nil {
					t.Fatal(err)
				}
				owner, err := dnfquest.NewOwner(repositories)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := owner.ApplyAccept(
					ctx,
					pvf.quests,
					dnfquest.CharacterEligibility{Level: test.level, Job: 2},
					dnfquest.NewQuestIDCommand(
						alignedcmd.Request{AccountID: "dnf:1", SelectedCharacterID: 19},
						"accept_quest",
						dnfquest.QuestIDRequest{QuestID: uint16(test.parentQuest)},
					),
				); err != nil {
					t.Fatalf("accept quest %d: %v", test.parentQuest, err)
				}
				accepted, found, err := repositories.Quest.Load(ctx, "19")
				if err != nil || !found || !seekerQST010QuestActive(accepted.States[test.mazeQuest]) {
					t.Fatalf("accepted maze quest=%d state=%+v found=%t err=%v", test.mazeQuest, accepted.States[test.mazeQuest], found, err)
				}

				service := &Service{
					options: options{
						accountID: "dnf:1", currentClientVersion: "official-24.9",
						gameUpperBodyCodec: gameUpperBodyCodecPlain,
					},
					worldMapTable:           pvf.table,
					worldMapResolver:        pvf.resolver,
					dungeonMonsterTable:     pvf.monsters,
					dungeonAICharacterTable: pvf.ai,
					questCatalog:            pvf.quests,
					dungeonChoice:           func(int) (int, error) { return 0, nil },
					dungeonSeed:             func() (uint32, error) { return 1, nil },
					repositoryProvider:      func() (dnfrepo.Group, bool) { return repositories, true },
				}
				conn := &bufferConn{}
				session := &gameSession{conn: conn, connID: "qst010-" + test.name, selectedCharacterID: 19}
				runtime, start, err := service.prepareDungeonRuntime(
					ctx,
					session,
					dungeoncmd.SelectDungeonRequest{DungeonID: test.dungeonID, Difficulty: 0},
				)
				if err != nil {
					t.Fatalf("prepare dungeon=%d: %v", test.dungeonID, err)
				}
				session.dungeon.runtime = runtime
				if runtime.MazeIndex != test.mazeIndex ||
					!realMazeConnectsQuest(runtime.Dungeon.Mazes[runtime.MazeIndex], test.mazeQuest) {
					t.Fatalf("maze=%d quest=%d connection=%v", runtime.MazeIndex, test.mazeQuest,
						runtime.Dungeon.Mazes[runtime.MazeIndex].QuestConnection)
				}

				layout, err := worldmap.BuildDungeonLayout(
					pvf.resolver,
					int64(test.dungeonID),
					runtime.MazeIndex,
					func(choice worldmap.DungeonMapChoice) (int64, error) { return choice.Candidates[0].ID, nil },
				)
				if err != nil {
					t.Fatal(err)
				}
				targetCoordinate, found := seekerQST010FindMap(layout, test.targetMap)
				if !found {
					t.Fatalf("target map=%d is absent from dungeon=%d maze=%d", test.targetMap, test.dungeonID, runtime.MazeIndex)
				}
				for _, coordinate := range realDungeonPathToRoom(t, layout, start.Coordinate, targetCoordinate)[1:] {
					clearCurrentRealPVFSmokeRoom(t, runtime)
					body := make([]byte, dungeoncmd.MoveMapRequestSize)
					body[0], body[1] = byte(coordinate.X), byte(coordinate.Y)
					if err := service.handleDungeonMoveMap(session, body); err != nil {
						t.Fatalf("move to %s: %v", coordinate, err)
					}
				}
				seekerQST010EnsureAnnounced(t, runtime)
				scene, ok := runtime.Session.Scene()
				if !ok || scene.Map.Map.ID != test.targetMap ||
					scene.Coordinate != targetCoordinate || len(scene.BlockingHostiles) == 0 {
					t.Fatalf("target scene=%+v ok=%t coordinate=%s blocking=%v",
						scene, ok, targetCoordinate, scene.BlockingHostiles)
				}

				var ally runtimeDungeonExtendedActor
				for _, actor := range runtime.Room.Snapshot().ExtendedActors {
					if actor.AICharacter != nil && actor.AICharacter.Code == test.allyCode {
						ally = actor
						break
					}
				}
				if ally.ObjectKey == 0 || ally.State != runtimeDungeonMonsterAnnounced ||
					ally.HostileReference != nil || ally.AICharacterMetadata == nil {
					t.Fatalf("friendly APC code=%d actor=%+v", test.allyCode, ally)
				}

				actualMobs := map[int64]int{}
				for _, monster := range runtime.Room.Snapshot().Monsters {
					actualMobs[monster.Spawn.MonsterID]++
				}
				if !reflect.DeepEqual(actualMobs, test.expectMobs) {
					t.Fatalf("runtime monsters=%v want=%v", actualMobs, test.expectMobs)
				}

				orderedKeys := seekerQST010BlockingKeys(t, runtime, scene)
				for _, objectKey := range orderedKeys {
					conn.write.Reset()
					body := seekerQST010DeathBody(objectKey)
					// The dummy is script-owned. Exercise the recorded APC combat
					// shape only for ordinary hostiles, including the actual boss.
					monster, ordinary := runtime.Room.AnnouncedMonster(objectKey)
					if report != "scripted-empty" && ordinary && normalizeDungeonPVFSymbol(monster.Spawn.Rank) != "dummy" {
						body = friendlyAPCDeathBody(t, objectKey, ally.ObjectKey, false)
						if report == "friendly-key" {
							binary.LittleEndian.PutUint16(body[4:], uint16(ally.ObjectKey))
						}
					}
					if err := service.handleDungeonMonsterDeath(session, body); err != nil {
						t.Fatalf("death object=%d: %v", objectKey, err)
					}
					if _, defeated := runtime.Room.DefeatedDeathTarget(objectKey); !defeated {
						t.Fatalf("death object=%d was not authoritative", objectKey)
					}
					if !seekerQST010HasOp38(t, conn.write.Bytes(), objectKey) {
						t.Fatalf("death object=%d produced no op38", objectKey)
					}
				}
				scene, _ = runtime.Session.Scene()
				if !scene.Cleared {
					t.Fatalf("target room not cleared: blocking=%v defeated=%v",
						scene.BlockingHostiles, scene.DefeatedObjects)
				}
				for _, objectKey := range orderedKeys {
					if !dungeonSceneObjectDefeated(scene.DefeatedObjects, objectKey) {
						t.Fatalf("cleared target room lost authoritative death object=%d defeated=%v",
							objectKey, scene.DefeatedObjects)
					}
				}

				if test.passGate {
					room, ok := layout.Room(scene.Coordinate)
					if !ok || len(room.Neighbors) == 0 {
						t.Fatalf("target room=%s has no gate neighbors", scene.Coordinate)
					}
					next := room.Neighbors[0].Coordinate
					body := make([]byte, dungeoncmd.MoveMapRequestSize)
					body[0], body[1] = byte(next.X), byte(next.Y)
					if err := service.handleDungeonMoveMap(session, body); err != nil {
						t.Fatalf("post-clear move from %s to %s: %v", scene.Coordinate, next, err)
					}
					after, _ := runtime.Session.Scene()
					if after.Coordinate != next {
						t.Fatalf("post-clear move remained=%s want=%s", after.Coordinate, next)
					}
					t.Logf("quest=%d target=%d ally=%d blocked=%d gate=%s->%s map=%d",
						test.parentQuest, test.targetMap, ally.ObjectKey, len(orderedKeys), scene.Coordinate, next, after.Map.Map.ID)
				} else {
					var dummy runtimeDungeonMonster
					for _, monster := range runtime.Room.Snapshot().Monsters {
						if monster.Spawn.MonsterID == 63821 {
							dummy = monster
							break
						}
					}
					if dummy.ObjectKey == 0 ||
						normalizeDungeonPVFSymbol(dummy.Spawn.Rank) != "dummy" ||
						normalizeDungeonPVFSymbol(dummy.Spawn.SuffixMarker) != "boss" ||
						dummy.State != runtimeDungeonMonsterDefeated {
						t.Fatalf("Niver-room dummy=%+v", dummy)
					}
					dummyType, err := currentDungeonMonsterActorType(dummy.Spawn)
					if err != nil {
						t.Fatal(err)
					}
					t.Logf("quest=%d target=%d Niver=%d dummy=%d type=%d blockers=%v cleared=%t",
						test.parentQuest, test.targetMap, ally.ObjectKey, dummy.ObjectKey,
						dummyType, orderedKeys, scene.Cleared)
				}

				record, found, err := repositories.Quest.Load(ctx, "19")
				if err != nil || !found {
					t.Fatalf("load final quest found=%t err=%v", found, err)
				}
				parent := record.States[test.parentQuest]
				if parent.Status != "active" || (test.parentQuest == 3525 && parent.ProgressValue != 2) || (test.parentQuest == 3553 && parent.ProgressValue != 1) {
					t.Fatalf("unexpected parent after clear: %+v", parent)
				}
				if test.parentQuest == 3525 && record.States[3560].Status != "completed" {
					t.Fatalf("south clear child=%+v", record.States[3560])
				}
				t.Logf("quest states after clear parent=%+v maze=%+v", parent, record.States[test.mazeQuest])
			})
		}
	}
}

func seekerQST010FindMap(layout *worldmap.DungeonTopology, mapID int64) (worldmap.RoomCoordinate, bool) {
	for _, room := range layout.Rooms() {
		if room.Map != nil && room.Map.Map.ID == mapID {
			return room.Coordinate, true
		}
	}
	return worldmap.RoomCoordinate{}, false
}

func seekerQST010EnsureAnnounced(t *testing.T, runtime *runtimeDungeonState) {
	t.Helper()
	room := runtime.Room.Snapshot()
	needsAnnouncement := false
	for _, monster := range room.Monsters {
		needsAnnouncement = needsAnnouncement || monster.State == runtimeDungeonMonsterPlanned
	}
	for _, actor := range room.ExtendedActors {
		needsAnnouncement = needsAnnouncement || actor.State == runtimeDungeonMonsterPlanned
	}
	if needsAnnouncement {
		if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
			t.Fatalf("announce map=%d: %v", room.MapID, err)
		}
	}
}

func seekerQST010BlockingKeys(t *testing.T, runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene) []uint32 {
	t.Helper()
	keys := make([]uint32, 0, len(scene.BlockingHostiles))
	for _, reference := range scene.BlockingHostiles {
		var objectKey uint32
		for candidate, bound := range scene.RuntimeObjects {
			if bound == reference {
				objectKey = candidate
				break
			}
		}
		if objectKey == 0 {
			t.Fatalf("blocking reference=%+v has no object key", reference)
		}
		keys = append(keys, objectKey)
	}
	// Exercise each blocker before the boss clear transition. This is a
	// controlled report order; native fight/render ordering is tested separately.
	if len(keys) > 1 {
		for index, key := range keys {
			for _, monster := range runtime.Room.Snapshot().Monsters {
				if monster.ObjectKey == key && monster.Spawn.MonsterID == 65502 {
					copy(keys[index:], keys[index+1:])
					keys[len(keys)-1] = key
					return keys
				}
			}
		}
	}
	return keys
}

func seekerQST010DeathBody(objectKey uint32) []byte {
	body := make([]byte, dungeoncmd.Official249DieMonsterBaseSize)
	binary.LittleEndian.PutUint32(body, objectKey)
	binary.LittleEndian.PutUint16(body[4:], ^uint16(0))
	body[68] = 7
	return body
}

func seekerQST010HasOp38(t *testing.T, raw []byte, objectKey uint32) bool {
	t.Helper()
	for _, packet := range splitAllFinishBridgePackets(t, raw) {
		if packet.Header.MsgID == 38 && len(packet.Body) >= 2 &&
			binary.LittleEndian.Uint16(packet.Body[:2]) == uint16(objectKey) {
			return true
		}
	}
	return false
}

func seekerQST010QuestActive(state dnfrepo.QuestState) bool {
	return state.Status == "active" || state.Status == "in_progress"
}
