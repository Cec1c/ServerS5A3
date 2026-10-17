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
