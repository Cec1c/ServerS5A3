package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Rebuild the matching PVF story run with a monster-team follower. The same
// friendly actor key must remain valid after leaving its spawning room.
func TestRealPVFFriendlyMonsterLowellDeathsClearRooms(t *testing.T) {
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
		Name:        "Quest3232",
		Job:         "0",
		Level:       32,
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
			3232: {Status: "active", ProgressValue: 1},
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
	session := &gameSession{conn: conn, connID: "real-pvf-quest3232-owner-sentinel", selectedCharacterID: 99}
	runtime, _, err := service.prepareDungeonRuntime(
		ctx,
		session,
		dungeoncmd.SelectDungeonRequest{DungeonID: 32, Difficulty: 0},
	)
	if err != nil {
		t.Fatalf("prepare real dungeon 32 quest 3232 runtime: %v", err)
	}
	session.dungeon.runtime = runtime
	connection := runtime.Dungeon.Mazes[runtime.MazeIndex].QuestConnection
	if len(connection) < 2 || connection[0] != 0 || connection[1] != 3232 {
		t.Fatalf("real dungeon 32 selected maze=%d quest_connection=%v", runtime.MazeIndex, connection)
	}

	scene, _ := runtime.Session.Scene()
	if runtime.MazeIndex != 2 || scene.Map.Map.ID != 91331 {
		t.Fatalf("wrong scene: maze=%d map=%d", runtime.MazeIndex, scene.Map.Map.ID)
	}
	if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		t.Fatal(err)
	}
	var ally uint32
	for _, monster := range runtime.Room.Snapshot().Monsters {
		if monster.Spawn.MonsterID == 107000152 {
			ally = monster.ObjectKey
		}
	}
	if ally == 0 {
		t.Fatal("matching PVF friendly monster missing")
	}
	origin := runtime.Room
	for roomIndex := 0; roomIndex < 5; roomIndex++ {
		scene, _ = runtime.Session.Scene()
		if scene.Coordinate != (worldmap.RoomCoordinate{X: int64(roomIndex)}) {
			t.Fatalf("wrong room: %s map=%d", scene.Coordinate, scene.Map.Map.ID)
		}
		killed := 0
		for _, monster := range runtime.Room.Snapshot().Monsters {
			if monster.ObjectKey == ally {
				continue
			}
			body := friendlyMonsterDeathBody(t, monster.ObjectKey, ally)
			conn.write.Reset()
			if err := service.handleDungeonMonsterDeath(session, body); err != nil {
				t.Fatal(err)
			}
			if _, ok := runtime.Room.DefeatedDeathTarget(monster.ObjectKey); !ok {
				t.Fatalf("friendly monster kill rejected: map=%d target=%d", scene.Map.Map.ID, monster.ObjectKey)
			}
			packet, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
			if packet.Header.MsgID != 38 || binary.LittleEndian.Uint16(packet.Body) != uint16(monster.ObjectKey) {
				t.Fatal("missing death notification")
			}
			before, _, err := repositories.Character.Load(ctx, "99")
			if err != nil {
				t.Fatal(err)
			}
			conn.write.Reset()
			if err := service.handleDungeonMonsterDeath(session, body); err != nil {
				t.Fatal(err)
			}
			after, _, err := repositories.Character.Load(ctx, "99")
			if err != nil {
				t.Fatal(err)
			}
			packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if packet.Header.MsgID != 38 || len(rest) != 0 || !reflect.DeepEqual(before, after) {
				t.Fatal("duplicate settled progression or rewards")
			}
			killed++
		}
		scene, _ = runtime.Session.Scene()
		if !scene.Cleared {
			t.Fatal("friendly kills left room blocked")
		}
		if _, ok := origin.AnnouncedMonster(ally); !ok {
			t.Fatal("friendly follower retired")
		}
		t.Logf("map=%d allied_key=%d kills=%d cleared=%t", scene.Map.Map.ID, ally, killed, scene.Cleared)
		move := make([]byte, dungeoncmd.MoveMapRequestSize)
		move[0] = byte(roomIndex + 1)
		conn.write.Reset()
		if err := service.handleDungeonMoveMap(session, move); err != nil {
			t.Fatal(err)
		}
		next, _ := runtime.Session.Scene()
		if next.Coordinate != (worldmap.RoomCoordinate{X: int64(roomIndex + 1)}) {
			t.Fatalf("room transition blocked: %+v", next.Coordinate)
		}
	}
}
