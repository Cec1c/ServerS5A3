package dnfbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
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

type seekerAPCFollowupSource map[string]string

func (s seekerAPCFollowupSource) ReadText(path string) (string, error) {
	v, ok := s[strings.ToLower(strings.ReplaceAll(path, "\\", "/"))]
	if !ok {
		return "", fmt.Errorf("missing extracted file %s", path)
	}
	return v, nil
}

// Document texts are unmodified PVF extracts; list rows select the three story
// mazes. Prerequisites are setup. Acceptance, all room deaths and moves use
// production handlers; no room-clear callback is injected.
func TestSeekerAPCFollowupDiagnostic(t *testing.T) {
	path := os.Getenv("SEEKER_APC_SOURCE_JSON")
	if path == "" {
		t.Skip("set SEEKER_APC_SOURCE_JSON")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := seekerAPCFollowupSource{}
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	seekerRunAPCFollowups(t, source)
}

func TestSeekerAPCFollowupRealPVF(t *testing.T) {
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
	seekerRunAPCFollowups(t, archive)
}

func seekerRunAPCFollowups(t *testing.T, source dnfpvf.Source) {
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
	cases := []struct {
		q                    int64
		level                int
		dungeon              uint32
		start, target, owner int64
		rooms                int
	}{
		{3732, 81, 101, 76490, 76499, 11525, 10},
		{3752, 83, 104, 76630, 76637, 11525, 8},
		{3976, 89, 312, 76920, 76928, 17002, 9},
	}
	for _, tc := range cases {
		for _, bossLast := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/boss-last-%t", tc.q, bossLast), func(t *testing.T) {
				def, ok := catalog.Find(tc.q)
				if !ok {
					t.Fatal("quest missing")
				}
				repos, account, character := seekerQST012AcceptQuest(t, ctx, catalog, seekerQST012QuestCase{questID: tc.q, level: tc.level, prerequisite: tc.q - 1})
				conn := &bufferConn{}
				logSink, readLogs := supplement45LogSink(t)
				svc := &Service{options: options{accountID: account, currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain}, worldMapTable: table, worldMapResolver: resolver, dungeonMonsterTable: monsters, dungeonAICharacterTable: ai, questCatalog: catalog, dungeonChoice: func(int) (int, error) { return 0, nil }, dungeonSeed: func() (uint32, error) { return 1, nil }, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
				svc.packetLog, svc.logger = logSink.packetLog, logSink.logger
				svc.questFinishResourceLoader = func(context.Context) (*progression.Tables, *pvfDungeonDropCatalog, error) {
					return progress, items, nil
				}
				session := &gameSession{conn: conn, connID: fmt.Sprintf("apc-followup-%d", tc.q), selectedCharacterID: 19, channel: channelcatalog.Channel{ID: 19}}
				rt, start, err := svc.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: tc.dungeon})
				if err != nil {
					t.Fatal(err)
				}
				session.dungeon.runtime = rt
				if start.Map.Map.ID != tc.start {
					t.Fatalf("start=%d", start.Map.Map.ID)
				}
				seekerQST012EnsureAnnounced(t, rt, "start")
				owner := seekerQST012FindFriendlyAIByCode(t, rt, tc.owner)
				layout, err := worldmap.BuildDungeonLayout(resolver, int64(tc.dungeon), rt.MazeIndex, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
				if err != nil {
					t.Fatal(err)
				}
				var target worldmap.RoomCoordinate
				found := false
				for _, room := range layout.Rooms() {
					if room.Map != nil && room.Map.Map.ID == tc.target {
						target = room.Coordinate
						found = true
					}
				}
				if !found {
					t.Fatal("target absent")
				}
				route := realDungeonPathToRoom(t, layout, start.Coordinate, target)
				if len(route) != tc.rooms {
					t.Fatalf("rooms=%d want=%d", len(route), tc.rooms)
				}
				deaths := 0
				for n, coord := range route {
					if n > 0 {
						body := make([]byte, dungeoncmd.MoveMapRequestSize)
						body[0], body[1] = byte(coord.X), byte(coord.Y)
						if err := svc.handleDungeonMoveMap(session, body); err != nil {
							t.Fatal(err)
						}
					}
					scene, _ := rt.Session.Scene()
					if scene.Coordinate != coord {
						for _, line := range strings.Split(readLogs(), "\n") {
							if strings.Contains(line, "blocked") {
								t.Log(line)
							}
						}
						_, reason := svc.planCurrentDungeonMove(rt, dungeoncmd.MoveMapRequest{NextX: byte(coord.X), NextY: byte(coord.Y)})
						t.Fatalf("move reached=%v want=%v reason=%v", scene.Coordinate, coord, reason)
					}
					seekerQST012EnsureAnnounced(t, rt, "route")
					if bossLast && tc.q == 3976 && scene.Map.Map.ID == tc.target {
						owner = seekerQST012FindFriendlyAIByCode(t, rt, 56500)
					}
					roomDeaths := 0
					for j := 0; j < 100; j++ {
						mob, ok := seekerQST012LiveHostile(rt)
						if !ok {
							break
						}
						if bossLast {
							for _, candidate := range rt.Room.Snapshot().Monsters {
								if candidate.State != runtimeDungeonMonsterAnnounced || normalizeDungeonPVFSymbol(candidate.Spawn.Rank) == "boss" {
									continue
								}
								for _, hostile := range scene.ExpectedHostiles {
									if candidate.Reference == hostile {
										mob = candidate
										break
									}
								}
							}
						}
						packets, accepted := seekerQST012SendDeath(t, svc, session, conn, mob.ObjectKey, owner, 0x111)
						if !accepted || !seekerQST012HasOp38(packets, mob.ObjectKey) {
							t.Fatalf("map=%d APC=%d monster=%d/key%d accepted=%t", scene.Map.Map.ID, owner, mob.Spawn.MonsterID, mob.ObjectKey, accepted)
						}
						roomDeaths++
						deaths++
					}
					scene, _ = rt.Session.Scene()
					if !scene.Cleared {
						t.Fatalf("map=%d remains uncleared expected=%v defeated=%v", scene.Map.Map.ID, scene.ExpectedHostiles, scene.DefeatedObjects)
					}
					t.Logf("quest=%d map=%d APC=%d deaths=%d cleared=true", tc.q, scene.Map.Map.ID, owner, roomDeaths)
				}
				record, found, err := repos.Quest.Load(ctx, character)
				if err != nil || !found {
					t.Fatal(err)
				}
				state := record.States[tc.q]
				if state.Status != "active" || state.ProgressValue != 0 {
					t.Fatalf("final quest=%+v", state)
				}
				if rt.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
					t.Fatalf("run=%s", rt.Session.Snapshot().Run.Status)
				}
				t.Logf("quest=%d %s type=%s rooms=%d deaths=%d pending reward state=%+v", tc.q, def.Name, def.Type, len(route), deaths, state)
				qst006AssertFinishSuccess(t, qst006CallFinish(t, svc, session, conn, uint16(tc.q)), uint16(tc.q))
				record, _, err = repos.Quest.Load(ctx, character)
				if err != nil {
					t.Fatal(err)
				}
				if record.States[tc.q].Status != "completed" {
					t.Fatalf("finish state=%+v", record.States[tc.q])
				}
				t.Logf("quest=%d normal op34 completed", tc.q)
			})
		}
	}
}
