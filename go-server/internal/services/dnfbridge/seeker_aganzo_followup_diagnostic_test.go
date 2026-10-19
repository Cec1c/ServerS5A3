package dnfbridge

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Quest acceptance, room deaths, moves, scripted APC death and hand-in use
// production handlers. The final owner-FFFF death is a compatibility replay;
// triggering the perfume action on the client is outside this server test.
func TestSeekerAganzoFollowupDiagnostic(t *testing.T) {
	path := os.Getenv("SEEKER_AGANZO_SOURCE_JSON")
	if path == "" {
		t.Skip("set SEEKER_AGANZO_SOURCE_JSON")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := seekerAPCFollowupSource{}
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	seekerRunAganzoFollowup(t, source)
}

func TestSeekerAganzoFollowupRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if archive.Snapshot().Checksum != "9dcd31a3e6001fd5563d36dc65cbaa96bed8b782700cba30625197e27cf1e43c" {
		t.Fatal("unexpected PVF checksum")
	}
	seekerRunAganzoFollowup(t, archive)
}

func seekerRunAganzoFollowup(t *testing.T, source dnfpvf.Source) {
	ctx := context.Background()
	table, err := worldmap.LoadSource(ctx, source, worldmap.Options{SkipAreas: true})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	ai, err := newPVFDungeonAICharacterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Build(ctx, source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := progression.Load(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	repos, account, character := seekerQST012AcceptQuest(t, ctx, catalog, seekerQST012QuestCase{questID: 3213, level: 35, prerequisite: 3212})
	conn := &bufferConn{}
	logSink, readLogs := supplement45LogSink(t)
	svc := &Service{options: options{accountID: account, currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain}, worldMapTable: table, worldMapResolver: resolver, dungeonMonsterTable: monsters, dungeonAICharacterTable: ai, questCatalog: catalog, dungeonChoice: func(int) (int, error) { return 0, nil }, dungeonSeed: func() (uint32, error) { return 1, nil }, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	svc.packetLog, svc.logger = logSink.packetLog, logSink.logger
	svc.questFinishResourceLoader = func(context.Context) (*progression.Tables, *pvfDungeonDropCatalog, error) {
		return progress, items, nil
	}
	session := &gameSession{conn: conn, connID: "aganzo-followup", selectedCharacterID: 19, channel: channelcatalog.Channel{ID: 19}}
	rt, start, err := svc.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: 26})
	if err != nil {
		t.Fatal(err)
	}
	session.dungeon.runtime = rt
	layout, err := worldmap.BuildDungeonLayout(resolver, 26, rt.MazeIndex, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
	if err != nil {
		t.Fatal(err)
	}
	var target worldmap.RoomCoordinate
	found := false
	for _, r := range layout.Rooms() {
		if r.Map != nil && r.Map.Map.ID == 53529 {
			target = r.Coordinate
			found = true
		}
	}
	if !found {
		t.Fatal("final map missing")
	}
	route := realDungeonPathToRoom(t, layout, start.Coordinate, target)
	deaths := 0
	for n, coord := range route {
		if n > 0 {
			body := make([]byte, dungeoncmd.MoveMapRequestSize)
			body[0], body[1] = byte(coord.X), byte(coord.Y)
			if err = svc.handleDungeonMoveMap(session, body); err != nil {
				t.Fatal(err)
			}
		}
		scene, _ := rt.Session.Scene()
		if scene.Coordinate != coord {
			t.Fatalf("move=%v want=%v logs=%s", scene.Coordinate, coord, readLogs())
		}
		seekerQST012EnsureAnnounced(t, rt, "aganzo-route")
		for i := 0; i < 100; i++ {
			mob, ok := seekerQST012LiveHostile(rt)
			if !ok {
				break
			}
			packets, accepted := seekerQST012SendDeath(t, svc, session, conn, mob.ObjectKey, 19, 0x111)
			if !accepted || !seekerQST012HasOp38(packets, mob.ObjectKey) {
				t.Fatalf("map=%d monster=%d rejected", scene.Map.Map.ID, mob.Spawn.MonsterID)
			}
			deaths++
		}
	}
	var key uint32
	for _, a := range rt.Room.Snapshot().ExtendedActors {
		if a.AICharacter != nil && a.AICharacter.Code == 2004 {
			key = a.ObjectKey
		}
	}
	if key == 0 {
		t.Fatal("Aganzo absent")
	}
	conn.write.Reset()
	if err = svc.handleDungeonMonsterDeath(session, storyProcDeathBody(key)); err != nil {
		t.Fatal(err)
	}
	if _, ok := rt.Room.DefeatedDeathTarget(key); !ok {
		t.Fatalf("script death rejected logs=%s", readLogs())
	}
	if !rt.settlementEntrySent || rt.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
		t.Fatal("script death did not settle")
	}
	qr, _, err := repos.Quest.Load(ctx, character)
	if err != nil || qr.States[3213].ProgressValue != 0 {
		t.Fatalf("quest=%+v err=%v", qr, err)
	}
	packets := qst006CallFinish(t, svc, session, conn, 3213)
	qst006AssertFinishSuccess(t, packets, 3213)
	qst006AssertState(t, ctx, repos, map[int64]dnfrepo.QuestState{3213: {Status: "completed"}})
	t.Logf("rooms=%d normal-deaths=%d Aganzo=%d settled-and-handed-in", len(route), deaths, key)
}
