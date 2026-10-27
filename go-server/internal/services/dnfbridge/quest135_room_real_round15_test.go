package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 段135 运行时拓扑探针（第十五批）：在固定 v106 Script.pvf 上真实装载
// dungeon 26 / map 53529 的房间，量出它是否满足 dummy+AI 双 boss gate，
// 以及 AI/APC actor 在权威运行时里到底是不是 blocker。
//
// 边界：不造 dummy、不启 170、不假死亡、不改通关规则；只复用既有
// realPVF scene/runtime fixture 并运行既有生产判定。
// 真实 3213_53529.map 只有 [ai character] 2004 monster/boss，没有 [monster] 段。
const (
	quest135RoomRealPVFDungeonID   = 26
	quest135RoomRealPVFQuestID     = 3213
	quest135RoomRealPVFMapID       = 53529
	quest135RoomRealPVFAIBossCode  = 2004
	quest135RoomRealPVFCharacterID = 99
)

func TestRealScriptPVFDungeon26Quest3213Map53529StoryAIBossGateAndAPCBlocker(t *testing.T) {
	pvfPath := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNF_WORLDMAP_REAL_PVF_SMOKE to load the real map 53529 story-room topology")
	}
	ctx := context.Background()
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(ctx, archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	aiCharacters, err := newPVFDungeonAICharacterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	questIndex, err := dnfpvf.Build(ctx, archive, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	questCatalog, err := dnfquest.Load(ctx, questIndex)
	if err != nil {
		t.Fatal(err)
	}

	// 任务侧锚点：3213 的 [clear map] 必须就是 53529，否则探针对象不对。
	quest, ok := questCatalog.Find(quest135RoomRealPVFQuestID)
	if !ok || normalizeDungeonPVFSymbol(quest.Type) != "clear map" ||
		len(quest.IntData) != 1 || quest.IntData[0] != quest135RoomRealPVFMapID {
		t.Fatalf("real quest %d definition=%+v found=%t want clear-map %d",
			quest135RoomRealPVFQuestID, quest, ok, quest135RoomRealPVFMapID)
	}

	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "99",
		AccountID:   "account-1",
		Name:        "Quest135Room",
		Job:         "0",
		Level:       90,
		Stats:       map[string]int64{"fatigue": 200, "exp": 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "99",
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: "99",
		States: map[int64]dnfrepo.QuestState{
			quest135RoomRealPVFQuestID: {Status: "active"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:                 options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		worldMapTable:           table,
		worldMapResolver:        resolver,
		dungeonMonsterTable:     monsters,
		dungeonAICharacterTable: aiCharacters,
		questCatalog:            questCatalog,
		dungeonChoice:           func(int) (int, error) { return 0, nil },
		dungeonSeed:             func() (uint32, error) { return 1, nil },
		repositoryProvider:      func() (dnfrepo.Group, bool) { return repositories, true },
	}
	connection := &bufferConn{}
	session := &gameSession{
		conn:                connection,
		connID:              "real-pvf-dungeon26-quest3213-map53529",
		selectedCharacterID: quest135RoomRealPVFCharacterID,
	}
	runtime, startScene, err := service.prepareDungeonRuntime(
		ctx,
		session,
		dungeoncmd.SelectDungeonRequest{DungeonID: quest135RoomRealPVFDungeonID, Difficulty: 0},
	)
	if err != nil {
		t.Fatalf("prepare real dungeon %d quest %d runtime: %v", quest135RoomRealPVFDungeonID, quest135RoomRealPVFQuestID, err)
	}
	session.dungeon.runtime = runtime

	maze := runtime.Dungeon.Mazes[runtime.MazeIndex]
	if len(maze.QuestConnection) < 2 || maze.QuestConnection[0] != 0 || maze.QuestConnection[1] != quest135RoomRealPVFQuestID {
		t.Fatalf("real dungeon %d selected maze=%d quest_connection=%v want the active quest %d connection",
			runtime.Dungeon.ID, runtime.MazeIndex, maze.QuestConnection, quest135RoomRealPVFQuestID)
	}

	topology, err := worldmap.BuildDungeonLayout(
		resolver,
		runtime.Dungeon.ID,
		runtime.MazeIndex,
		func(choice worldmap.DungeonMapChoice) (int64, error) { return choice.Candidates[0].ID, nil },
	)
	if err != nil {
		t.Fatalf("build real dungeon %d maze %d topology: %v", runtime.Dungeon.ID, runtime.MazeIndex, err)
	}
	targetRoom := worldmap.DungeonRoom{}
	targetFound := false
	for _, room := range topology.Rooms() {
		if room.Map != nil && room.Map.Map.ID == quest135RoomRealPVFMapID {
			targetRoom, targetFound = room, true
			break
		}
	}
	if !targetFound {
		t.Fatalf("real dungeon %d maze %d has no room for map %d", runtime.Dungeon.ID, runtime.MazeIndex, quest135RoomRealPVFMapID)
	}

	path := realDungeonPathToRoom(t, topology, startScene.Coordinate, targetRoom.Coordinate)
	for _, coordinate := range path[1:] {
		_ = clearCurrentRealPVFSmokeRoom(t, runtime)
		moveBody := make([]byte, dungeoncmd.MoveMapRequestSize)
		moveBody[0] = byte(coordinate.X)
		moveBody[1] = byte(coordinate.Y)
		if err := service.handleDungeonMoveMap(session, moveBody); err != nil {
			t.Fatalf("move real quest %d runtime to %s: %v", quest135RoomRealPVFQuestID, coordinate, err)
		}
	}
	scene := runtime.Session.Snapshot().Scene
	if scene.Map.Map.ID != quest135RoomRealPVFMapID || scene.Coordinate != targetRoom.Coordinate {
		t.Fatalf("real map %d target scene=%+v want room=%s", quest135RoomRealPVFMapID, scene, targetRoom.Coordinate)
	}

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
			t.Fatalf("announce real map %d actors: %v", quest135RoomRealPVFMapID, err)
		}
	}
	room = runtime.Room.Snapshot()
	scene = runtime.Session.Snapshot().Scene

	// 真实 53529 没有 [monster] 段：场景里不能出现任何普通怪行。
	if len(scene.Monsters) != 0 || len(room.Monsters) != 0 {
		t.Fatalf("real map %d unexpectedly carries [monster] rows: scene=%+v room=%+v",
			quest135RoomRealPVFMapID, scene.Monsters, room.Monsters)
	}
	// AI actor 2004 必须是已公告的 hostile AI boss（type 8 / monster / boss）。
	aiBoss := runtimeDungeonExtendedActor{}
	aiFound := false
	for _, actor := range room.ExtendedActors {
		if actor.Kind != runtimeDungeonActorAICharacter || actor.AICharacter == nil ||
			actor.AICharacter.Code != quest135RoomRealPVFAIBossCode {
			continue
		}
		aiBoss, aiFound = actor, true
		break
	}
	if !aiFound {
		t.Fatalf("real map %d has no AI actor code %d: %+v", quest135RoomRealPVFMapID, quest135RoomRealPVFAIBossCode, room.ExtendedActors)
	}
	if aiBoss.State != runtimeDungeonMonsterAnnounced || aiBoss.HostileReference == nil ||
		aiBoss.HostileReference.Kind != worldmap.HostileAICharacter || aiBoss.Packet.Type != 8 ||
		normalizeDungeonPVFSymbol(aiBoss.AICharacter.Faction) != "monster" ||
		normalizeDungeonPVFSymbol(aiBoss.AICharacter.AIType) != "boss" {
		t.Fatalf("real map %d AI actor=%+v is not the announced hostile monster/boss row", quest135RoomRealPVFMapID, aiBoss)
	}
	aiReference := *aiBoss.HostileReference
	if bound, ok := scene.RuntimeObjects[aiBoss.ObjectKey]; !ok || bound != aiReference {
		t.Fatalf("real map %d AI actor key %d binding=%v ok=%t want %+v",
			quest135RoomRealPVFMapID, aiBoss.ObjectKey, bound, ok, aiReference)
	}
	if !quest135RoomRealPVFContainsReference(scene.ExpectedHostiles, aiReference) {
		t.Fatalf("real map %d AI actor %+v missing from ExpectedHostiles=%v", quest135RoomRealPVFMapID, aiReference, scene.ExpectedHostiles)
	}
	// AI/APC 行（type 5..8）永远不进 room-door blocker 集：
	// 真实 53529 的 BlockingHostiles 必须为空，房间进入即 Cleared。
	if len(scene.BlockingHostiles) != 0 || !scene.Cleared {
		t.Fatalf("real map %d blocking=%v cleared=%t: the hostile AI/APC row must not be a room blocker",
			quest135RoomRealPVFMapID, scene.BlockingHostiles, scene.Cleared)
	}
	if quest135RoomRealPVFContainsReference(scene.BlockingHostiles, aiReference) {
		t.Fatalf("real map %d AI actor %+v leaked into BlockingHostiles", quest135RoomRealPVFMapID, aiReference)
	}

	gate, gateActive := currentDungeonStoryAIBossDeathGate(runtime, scene)
	if gateActive || len(gate.DummyBossObjectKeys) != 0 || len(gate.AIBossObjectKeys) != 0 {
		t.Fatalf("real map %d must not satisfy the dummy+AI dual-boss gate: active=%t gate=%+v",
			quest135RoomRealPVFMapID, gateActive, gate)
	}
	t.Logf("real map %d room topology: monsters=%d blocking=%d expected=%v cleared=%t AI_boss_key=%d AI_boss_ref=%+v gate_active=%t dummy_keys=%v",
		quest135RoomRealPVFMapID, len(scene.Monsters), len(scene.BlockingHostiles), scene.ExpectedHostiles,
		scene.Cleared, aiBoss.ObjectKey, aiReference, gateActive, gate.DummyBossObjectKeys)

	// 真实 boss die decision 最小分支：先由真实 op39 handler 提交 AI boss 的
	// 权威死亡，再走真实 op117 handler。裁判只认同房间权威死亡，不替任何 actor 补死。
	deathBody := make([]byte, dungeoncmd.DieMonsterRequestSize)
	binary.LittleEndian.PutUint32(deathBody[0:4], aiBoss.ObjectKey)
	binary.LittleEndian.PutUint16(deathBody[4:6], currentSceneActorObjectKey(quest135RoomRealPVFCharacterID))
	connection.write.Reset()
	if err := service.handleDungeonMonsterDeath(session, deathBody); err != nil {
		t.Fatalf("commit real map %d AI boss %d authoritative op39: %v", quest135RoomRealPVFMapID, aiBoss.ObjectKey, err)
	}
	afterDeath := runtime.Session.Snapshot()
	op39Bytes := connection.write.Len()
	op39Notify := storyAIBossDeathNotificationKeys(t, connection.write.Bytes())
	if afterDeath.Run.Status != worldmap.DungeonRunCompleted || !runtime.ordinaryFinalRoomClearAccepted ||
		!runtime.bossDieCheckAccepted || !runtime.settlementEntrySent {
		t.Fatalf("reported AI boss death did not complete the authoritative run: status=%v clear=%t boss=%t settlement=%t",
			afterDeath.Run.Status, runtime.ordinaryFinalRoomClearAccepted, runtime.bossDieCheckAccepted, runtime.settlementEntrySent)
	}
	if len(op39Notify) != 1 || op39Notify[0] != aiBoss.ObjectKey {
		t.Fatalf("AI boss death notification keys=%v want only %d", op39Notify, aiBoss.ObjectKey)
	}
	t.Logf("real map %d op39 decision: run_status=%v cleared=%t ordinary_final_clear=%t boss_check_accepted=%t settlement_entry=%t",
		quest135RoomRealPVFMapID, afterDeath.Run.Status, afterDeath.Scene.Cleared,
		runtime.ordinaryFinalRoomClearAccepted, runtime.bossDieCheckAccepted, runtime.settlementEntrySent)
	if len(afterDeath.Scene.DefeatedObjects) != 1 || !dungeonSceneObjectDefeated(afterDeath.Scene.DefeatedObjects, aiBoss.ObjectKey) {
		t.Fatalf("real map %d AI boss op39 must be the only committed death: defeated=%v",
			quest135RoomRealPVFMapID, afterDeath.Scene.DefeatedObjects)
	}
	if gate, active := currentDungeonStoryAIBossDeathGate(runtime, afterDeath.Scene); active {
		t.Fatalf("real map %d gate became active after the AI boss death: %+v", quest135RoomRealPVFMapID, gate)
	}

	requestBody := bossDieCheckRequestBody(currentSceneActorObjectKey(quest135RoomRealPVFCharacterID), uint16(aiBoss.ObjectKey))
	connection.write.Reset()
	if err := service.handleDungeonBossDieCheck(session, requestBody); err != nil {
		t.Fatalf("run real map %d op117 boss-die decision: %v", quest135RoomRealPVFMapID, err)
	}
	decided := runtime.Session.Snapshot()
	op117Bytes := connection.write.Len()
	if decided.Run.Status != worldmap.DungeonRunCompleted || !runtime.bossDieCheckAccepted || op117Bytes != 0 {
		t.Fatalf("post-clear boss check changed replay state: status=%v accepted=%t bytes=%d", decided.Run.Status, runtime.bossDieCheckAccepted, op117Bytes)
	}
	// 不假死亡：整条决策链只能认可我们提交过的那个 object key。
	if len(decided.Scene.DefeatedObjects) != 1 || !dungeonSceneObjectDefeated(decided.Scene.DefeatedObjects, aiBoss.ObjectKey) {
		t.Fatalf("real map %d decision fabricated extra deaths: defeated=%v", quest135RoomRealPVFMapID, decided.Scene.DefeatedObjects)
	}
	t.Logf("real map %d op117 decision: run_status=%v cleared=%t ordinary_final_clear=%t boss_check_accepted=%t settlement_entry=%t sent_bytes=%d",
		quest135RoomRealPVFMapID, decided.Run.Status, decided.Scene.Cleared,
		runtime.ordinaryFinalRoomClearAccepted, runtime.bossDieCheckAccepted, runtime.settlementEntrySent, op117Bytes)
	t.Logf("real map %d wire: op39_bytes=%d op39_die_notify_keys=%v op117_bytes=%d",
		quest135RoomRealPVFMapID, op39Bytes, op39Notify, op117Bytes)
}

func quest135RoomRealPVFContainsReference(values []worldmap.HostileReference, want worldmap.HostileReference) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
