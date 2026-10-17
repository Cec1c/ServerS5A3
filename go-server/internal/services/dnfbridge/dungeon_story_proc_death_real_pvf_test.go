package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// The op39 is an explicit compatibility replay, not a captured player packet.
// The quest, maze, actors and proc action all come from the matching real PVF.
func TestRealPVFQuest3213ScriptedHostileAPCDeath(t *testing.T) {
	source, table, resolver := loadRealStoryPVF(t)
	ctx := context.Background()
	index, err := dnfpvf.Build(ctx, source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	quests, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	quest, ok := quests.Find(3213)
	if !ok || normalizeDungeonPVFSymbol(quest.Type) != "clear map" || len(quest.IntData) != 1 || quest.IntData[0] != 53529 {
		t.Fatalf("quest3213=%+v found=%t", quest, ok)
	}
	monsters, err := newPVFDungeonMonsterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	ai, err := newPVFDungeonAICharacterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "99", AccountID: "account-1", Name: "Quest3213", Job: "0", Level: 35, Stats: map[string]int64{"fatigue": 156, "exp": 0}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "99", States: map[int64]dnfrepo.QuestState{3213: {Status: "active", ProgressValue: 1}}}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{options: options{accountID: "account-1", currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain}, worldMapTable: table, worldMapResolver: resolver, dungeonMonsterTable: monsters, dungeonAICharacterTable: ai, questCatalog: quests, dungeonChoice: func(int) (int, error) { return 0, nil }, dungeonSeed: func() (uint32, error) { return 1, nil }, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "real-pvf-quest3213-proc-death", selectedCharacterID: 99}
	rt, start, err := svc.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: 26, Difficulty: 0})
	if err != nil {
		t.Fatal(err)
	}
	session.dungeon.runtime = rt
	if !realMazeConnectsQuest(rt.Dungeon.Mazes[rt.MazeIndex], 3213) {
		t.Fatalf("wrong maze=%d connection=%v", rt.MazeIndex, rt.Dungeon.Mazes[rt.MazeIndex].QuestConnection)
	}
	layout, err := worldmap.BuildDungeonLayout(resolver, 26, rt.MazeIndex, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
	if err != nil {
		t.Fatal(err)
	}
	var target worldmap.RoomCoordinate
	found := false
	for _, r := range layout.Rooms() {
		if r.Map != nil && r.Map.Map.ID == 53529 {
			target, found = r.Coordinate, true
		}
	}
	if !found {
		t.Fatal("map53529 missing from selected layout")
	}
	for _, coordinate := range realDungeonPathToRoom(t, layout, start.Coordinate, target)[1:] {
		clearCurrentRealPVFSmokeRoom(t, rt)
		body := make([]byte, dungeoncmd.MoveMapRequestSize)
		body[0], body[1] = byte(coordinate.X), byte(coordinate.Y)
		if err := svc.handleDungeonMoveMap(session, body); err != nil {
			t.Fatal(err)
		}
	}
	scene, _ := rt.Session.Scene()
	if scene.Map.Map.ID != 53529 {
		t.Fatalf("map=%d", scene.Map.Map.ID)
	}
	planned := false
	for _, a := range rt.Room.Snapshot().ExtendedActors {
		planned = planned || a.State == runtimeDungeonMonsterPlanned
	}
	if planned {
		if _, err := rt.Room.AnnounceAllActors(rt.Session); err != nil {
			t.Fatal(err)
		}
	}
	var aganzo runtimeDungeonExtendedActor
	for _, a := range rt.Room.Snapshot().ExtendedActors {
		if a.AICharacter != nil && a.AICharacter.Code == 2004 {
			aganzo = a
		}
	}
	if aganzo.ObjectKey == 0 || aganzo.HostileReference == nil || len(rt.Room.Snapshot().ExtendedActors) != 1 {
		t.Fatalf("real actors=%+v", rt.Room.Snapshot().ExtendedActors)
	}
	conn.write.Reset()
	currentScene, _ := rt.Session.Scene()
	request, decodeErr := svc.decodeCurrentDungeonMonsterDeath(storyProcDeathBody(aganzo.ObjectKey))
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	definition, _, _ := ai.Find(2004)
	actionPath := dungeonAIProcActionPath(definition)
	actionText, readErr := source.ReadText(actionPath)
	doc, parseErr := worldmap.ParseDocument(actionPath, actionText)
	parsed, parsedOK := parseDungeonStoryProcDeath(doc)
	proof, proofOK := svc.currentDungeonStoryProcDeath(session, rt, currentScene, request)
	t.Logf("runtime status=%s scene cleared=%t boss=%t blockers=%v expected=%v path=%q read=%v parse=%v parsed=%+v/%t proof=%+v/%t decode=%v", rt.Session.Snapshot().Run.Status, currentScene.Cleared, currentScene.Boss, currentScene.BlockingHostiles, currentScene.ExpectedHostiles, actionPath, readErr, parseErr, parsed, parsedOK, proof, proofOK, decodeErr)
	if err := svc.handleDungeonMonsterDeath(session, storyProcDeathBody(aganzo.ObjectKey)); err != nil {
		t.Fatal(err)
	}
	if _, ok := rt.Room.DefeatedDeathTarget(aganzo.ObjectKey); !ok {
		t.Fatalf("scripted hostile APC2004 owner-ffff death rejected; remaining actor=%+v", aganzo)
	}
	keys := storyAIBossDeathNotificationKeys(t, conn.write.Bytes())
	if len(keys) != 1 || keys[0] != aganzo.ObjectKey {
		t.Fatalf("death keys=%v want only Aganzo=%d", keys, aganzo.ObjectKey)
	}
	scene, _ = rt.Session.Scene()
	if !scene.Cleared || !rt.bossDieCheckAccepted || !rt.settlementEntrySent {
		t.Fatalf("final state cleared=%t accepted=%t entry=%t", scene.Cleared, rt.bossDieCheckAccepted, rt.settlementEntrySent)
	}
	qr, _, err := repos.Quest.Load(ctx, "99")
	if err != nil {
		t.Fatal(err)
	}
	if qr.States[3213].ProgressValue != 0 {
		t.Fatalf("quest3213 not persisted: %+v", qr)
	}
	t.Logf("quest3213 maze=%d map=%d Aganzo=%d clear_map_trigger=%d", rt.MazeIndex, scene.Map.Map.ID, aganzo.ObjectKey, qr.States[3213].ProgressValue)
}

func storyProcDeathBody(objectKey uint32) []byte {
	body := make([]byte, dungeoncmd.Official249DieMonsterBaseSize)
	binary.LittleEndian.PutUint32(body, objectKey)
	binary.LittleEndian.PutUint16(body[4:], ^uint16(0))
	return body
}
