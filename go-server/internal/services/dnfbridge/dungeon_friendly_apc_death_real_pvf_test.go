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

// Rebuild the reported Aganzo room from the matching real PVF. No production
// map, quest or NPC ID exceptions are needed for the compatibility repair.
func TestRealPVFFriendlyAPCAganzoDeathsClearRoom(t *testing.T) {
	pvfPath := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("matching real PVF required")
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
	repositories := dnfrepomemory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{
		CharacterID: "99",
		AccountID:   "account-1",
		Name:        "Quest3188",
		Job:         "0",
		Level:       25,
		Stats: map[string]int64{
			"fatigue": 156,
			"exp":     0,
		},
	}
	if err := repositories.Character.Save(ctx, character); err != nil {
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
			3188: {Status: "active", ProgressValue: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:                 options{accountID: "account-1", currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		worldMapTable:           table,
		worldMapResolver:        resolver,
		dungeonMonsterTable:     monsters,
		dungeonAICharacterTable: aiCharacters,
		questCatalog:            questCatalog,
		dungeonChoice:           func(int) (int, error) { return 0, nil },
		dungeonSeed:             func() (uint32, error) { return 1, nil },
		repositoryProvider:      func() (dnfrepo.Group, bool) { return repositories, true },
	}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "real-pvf-quest3188-owner-sentinel", selectedCharacterID: 99}
	runtime, _, err := service.prepareDungeonRuntime(
		ctx,
		session,
		dungeoncmd.SelectDungeonRequest{DungeonID: 15, Difficulty: 0},
	)
	if err != nil {
		t.Fatalf("prepare real dungeon 15 quest 3188 runtime: %v", err)
	}
	session.dungeon.runtime = runtime
	connection := runtime.Dungeon.Mazes[runtime.MazeIndex].QuestConnection
	if len(connection) < 2 || connection[0] != 0 || connection[1] != 3188 {
		t.Fatalf("real dungeon 15 selected maze=%d quest_connection=%v", runtime.MazeIndex, connection)
	}

	scene, _ := runtime.Session.Scene()
	if runtime.MazeIndex != 4 || scene.Map.Map.ID != 57953 {
		t.Fatalf("wrong scene: maze=%d map=%d", runtime.MazeIndex, scene.Map.Map.ID)
	}
	if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		t.Fatal(err)
	}
	room := runtime.Room.Snapshot()
	if len(room.Monsters) != 7 || len(room.ExtendedActors) != 1 || (room.ExtendedActors[0].AICharacter == nil || room.ExtendedActors[0].AICharacter.Code != 2013) {
		t.Fatalf("unexpected Aganzo room: %+v", room)
	}
	apc := room.ExtendedActors[0].ObjectKey
	killed := 0
	for _, monster := range room.Monsters {
		if monster.Spawn.MonsterID != 700 {
			continue
		}
		body := friendlyAPCDeathBody(t, monster.ObjectKey, apc, killed < 2)
		conn.write.Reset()
		if err := service.handleDungeonMonsterDeath(session, body); err != nil {
			t.Fatal(err)
		}
		if _, ok := runtime.Room.DefeatedDeathTarget(monster.ObjectKey); !ok {
			t.Fatalf("APC kill %d rejected", monster.ObjectKey)
		}
		packet, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
		if packet.Header.MsgID != 38 || binary.LittleEndian.Uint16(packet.Body) != uint16(monster.ObjectKey) {
			t.Fatal("missing native death acknowledgement")
		}
		killed++
		scene, _ = runtime.Session.Scene()
		if killed < 6 && scene.Cleared {
			t.Fatal("ordinary APC kill prematurely cleared room")
		}
	}
	if killed != 6 || !scene.Cleared {
		t.Fatalf("killed=%d cleared=%t", killed, scene.Cleared)
	}
	if !runtime.Room.ContainsAnnouncedNonHostileAICharacterObjectKey(apc) {
		t.Fatal("Aganzo was retired")
	}
	t.Logf("maze=%d map=%d APC=%d killed=%d cleared=%t", runtime.MazeIndex, scene.Map.Map.ID, apc, killed, scene.Cleared)
	topology, err := worldmap.BuildDungeonLayout(resolver, runtime.Dungeon.ID, runtime.MazeIndex, func(choice worldmap.DungeonMapChoice) (int64, error) { return choice.Candidates[0].ID, nil })
	if err != nil {
		t.Fatal(err)
	}
	var next worldmap.RoomCoordinate
	foundNext := false
	for _, room := range topology.Rooms() {
		if room.Coordinate == scene.Coordinate && len(room.Neighbors) > 0 {
			next, foundNext = room.Neighbors[0].Coordinate, true
		}
	}
	if !foundNext {
		t.Fatal("Aganzo room has no neighboring room")
	}
	body := make([]byte, dungeoncmd.MoveMapRequestSize)
	body[0], body[1] = byte(next.X), byte(next.Y)
	conn.write.Reset()
	if err := service.handleDungeonMoveMap(session, body); err != nil {
		t.Fatal(err)
	}
	after, _ := runtime.Session.Scene()
	if after.Coordinate != next || after.Map.Map.ID == scene.Map.Map.ID {
		t.Fatal("APC deaths left the next room blocked")
	}
	t.Logf("next room=%s map=%d", after.Coordinate, after.Map.Map.ID)
}

// The reported 2026-10-18 23:02 run is dungeon 7118's start map 91206:
// object 403 and 402 were ordinary hostiles, the only extended actor was the
// story APC 55418, and the client wrote owner=0xffff with one contribution.
// This test rebuilds that exact room from the matching PVF and proves the
// tutorial metadata path accepts the APC kill without broadening the sentinel.
func TestRealPVFFriendlyAPC7118TutorialDeathsClearStartRoom(t *testing.T) {
	pvfPath := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("matching real PVF required")
	}
	ctx := context.Background()
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := archive.Snapshot().Checksum; got != "a56cacf96087ba34ca5299e04ab65f8268f694b214b97dec9879fdebf46f5290" {
		t.Fatalf("PVF checksum=%s", got)
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
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "177",
		AccountID:   "account-1",
		Name:        "tutorial-hero",
		Job:         "11",
		Level:       1,
		Stats:       map[string]int64{"fatigue": 156, "exp": 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "177",
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:                 options{accountID: "account-1", currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		worldMapTable:           table,
		worldMapResolver:        resolver,
		dungeonMonsterTable:     monsters,
		dungeonAICharacterTable: aiCharacters,
		dungeonChoice:           func(int) (int, error) { return 0, nil },
		dungeonSeed:             func() (uint32, error) { return 1, nil },
		repositoryProvider:      func() (dnfrepo.Group, bool) { return repositories, true },
	}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "real-pvf-7118-tutorial-apc", selectedCharacterID: 177}
	runtime, _, err := service.prepareDungeonRuntime(
		ctx,
		session,
		dungeoncmd.SelectDungeonRequest{DungeonID: 7118, Difficulty: 0},
	)
	if err != nil {
		t.Fatalf("prepare real tutorial 7118: %v", err)
	}
	session.dungeon.runtime = runtime
	if runtime.Dungeon.ID != 7118 || !isPVFTutorialDungeon(runtime) {
		t.Fatalf("dungeon=%d tutorial=%t", runtime.Dungeon.ID, isPVFTutorialDungeon(runtime))
	}
	scene, _ := runtime.Session.Scene()
	if scene.Map.Map.ID != 91206 {
		t.Fatalf("start map=%d want 91206", scene.Map.Map.ID)
	}
	if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		t.Fatal(err)
	}
	room := runtime.Room.Snapshot()
	if len(room.Monsters) != 2 || len(room.ExtendedActors) != 1 ||
		room.ExtendedActors[0].AICharacter == nil || room.ExtendedActors[0].AICharacter.Code != 55418 {
		t.Fatalf("unexpected 7118 start room: %+v", room)
	}
	apc := room.ExtendedActors[0].ObjectKey
	if apc != 404 || room.Monsters[0].ObjectKey != 402 || room.Monsters[1].ObjectKey != 403 {
		t.Fatalf("object keys apc=%d monsters=%d,%d", apc, room.Monsters[0].ObjectKey, room.Monsters[1].ObjectKey)
	}
	for index, monster := range room.Monsters {
		body := friendlyAPCDeathBody(t, monster.ObjectKey, apc, index == 0)
		conn.write.Reset()
		if err := service.handleDungeonMonsterDeath(session, body); err != nil {
			t.Fatal(err)
		}
		if _, ok := runtime.Room.DefeatedDeathTarget(monster.ObjectKey); !ok {
			t.Fatalf("tutorial APC kill %d rejected", monster.ObjectKey)
		}
		packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if packet.Header.MsgID != 38 || binary.LittleEndian.Uint16(packet.Body) != uint16(monster.ObjectKey) || len(rest) != 0 {
			t.Fatalf("missing tutorial op38 for %d: header=%+v body=%x rest=%x", monster.ObjectKey, packet.Header, packet.Body, rest)
		}
	}
	scene, _ = runtime.Session.Scene()
	if !scene.Cleared {
		t.Fatal("7118 start room did not clear")
	}
	conn.write.Reset()
	if err := service.handleDungeonMonsterDeath(session, friendlyAPCDeathBody(t, room.Monsters[0].ObjectKey, apc, false)); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != 38 || binary.LittleEndian.Uint16(packet.Body) != uint16(room.Monsters[0].ObjectKey) || len(rest) != 0 {
		t.Fatal("tutorial duplicate did not replay the authoritative death")
	}
	if !runtime.Room.ContainsAnnouncedNonHostileAICharacterObjectKey(apc) {
		t.Fatal("tutorial APC was retired by the monster deaths")
	}
	t.Logf("dungeon=%d map=%d apc=%d cleared=%t", runtime.Dungeon.ID, scene.Map.Map.ID, apc, scene.Cleared)
}
