package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// The owner-ffff death is an explicit simulation of the real cinematic's
// APC index 1 DESTROY. It is not a captured player packet or a rendered movie.
func TestSeekerQST007RealPVFCinematicHostileAPCRetirement(t *testing.T) {
	source, table, resolver := loadRealStoryPVF(t)
	ctx := context.Background()
	idx, err := dnfpvf.Build(ctx, source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	quests, err := dnfquest.Load(ctx, idx)
	if err != nil {
		t.Fatal(err)
	}
	q, ok := quests.Find(3538)
	if !ok || q.Name != "夜战司令官巴比伦" {
		t.Fatalf("quest=%+v", q)
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
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "dnf:1", Job: "2", Level: 67, Stats: map[string]int64{"fatigue": 156}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{3537: {Status: "completed"}}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	owner, err := dnfquest.NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ApplyAccept(ctx, quests, dnfquest.CharacterEligibility{Level: 67, Job: 2}, dnfquest.NewQuestIDCommand(alignedcmd.Request{AccountID: "dnf:1", SelectedCharacterID: 19}, "accept_quest", dnfquest.QuestIDRequest{QuestID: 3538})); err != nil {
		t.Fatal(err)
	}
	svc := &Service{options: options{accountID: "dnf:1", currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain}, worldMapTable: table, worldMapResolver: resolver, dungeonMonsterTable: monsters, dungeonAICharacterTable: ai, questCatalog: quests, dungeonChoice: func(int) (int, error) { return 0, nil }, dungeonSeed: func() (uint32, error) { return 1, nil }, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "qst007", selectedCharacterID: 19}
	rt, start, err := svc.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: 89, Difficulty: 0})
	if err != nil {
		t.Fatal(err)
	}
	session.dungeon.runtime = rt
	if !realMazeConnectsQuest(rt.Dungeon.Mazes[rt.MazeIndex], 3538) {
		t.Fatalf("maze=%d", rt.MazeIndex)
	}
	if start.Coordinate != (worldmap.RoomCoordinate{X: 5, Y: 1}) {
		t.Fatalf("start=%+v", start.Coordinate)
	}
	killMonsters := func() {
		for _, m := range rt.Room.Snapshot().Monsters {
			if m.State == runtimeDungeonMonsterPlanned {
				if _, err := rt.Room.AnnounceAllActors(rt.Session); err != nil {
					t.Fatal(err)
				}
				break
			}
		}
		for _, m := range rt.Room.Snapshot().Monsters {
			b := seekerQST007NativeDeathBody(m.ObjectKey)
			binary.LittleEndian.PutUint16(b[4:], uint16(currentSceneActorObjectKey(19)))
			if err := svc.handleDungeonMonsterDeath(session, b); err != nil {
				t.Fatal(err)
			}
			if _, ok := rt.Room.DefeatedDeathTarget(m.ObjectKey); !ok {
				t.Fatalf("combat death denied key=%d", m.ObjectKey)
			}
		}
	}
	for x := 4; x >= 2; x-- {
		killMonsters()
		b := make([]byte, dungeoncmd.MoveMapRequestSize)
		b[0], b[1] = byte(x), 1
		if err := svc.handleDungeonMoveMap(session, b); err != nil {
			t.Fatal(err)
		}
		scene, _ := rt.Session.Scene()
		if scene.Coordinate != (worldmap.RoomCoordinate{X: int64(x), Y: 1}) {
			t.Fatalf("failed move to %d/1", x)
		}
	}
	scene, _ := rt.Session.Scene()
	if scene.Map.Map.ID != 92334 {
		t.Fatalf("fourth room=%d", scene.Map.Map.ID)
	}
	var target, friendly runtimeDungeonExtendedActor
	for _, a := range rt.Room.Snapshot().ExtendedActors {
		t.Logf("actor key=%d code=%d state=%s hostile=%+v", a.ObjectKey, a.AICharacter.Code, a.State, a.HostileReference)
		if a.AICharacter != nil && a.AICharacter.Code == 10409 {
			target = a
		} else if a.AICharacter != nil && a.AICharacter.Code == 10448 {
			friendly = a
		}
	}
	if target.ObjectKey == 0 || target.HostileReference == nil || target.HostileReference.Index != 1 {
		t.Fatalf("cinematic target=%+v", target)
	}
	if friendly.ObjectKey == 0 {
		t.Fatalf("friendly APC10448 missing from fourth room: %+v", rt.Room.Snapshot().ExtendedActors)
	}
	conn.write.Reset()
	if err := svc.handleDungeonMonsterDeath(session, seekerQST007NativeDeathBody(target.ObjectKey)); err != nil {
		t.Fatal(err)
	}
	if keys := storyAIBossDeathNotificationKeys(t, conn.write.Bytes()); !slices.Equal(keys, []uint32{target.ObjectKey}) {
		t.Fatalf("cinematic destroy notifications=%v want only target=%d", keys, target.ObjectKey)
	}
	killMonsters()
	scene, _ = rt.Session.Scene()
	_, retired := rt.Room.DefeatedDeathTarget(target.ObjectKey)
	qr, _, err := repos.Quest.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("maze=%d map=%d target=%d retired=%t cleared=%t remaining=%+v quest=%s/%d", rt.MazeIndex, scene.Map.Map.ID, target.ObjectKey, retired, scene.Cleared, scene.ExpectedHostiles, qr.States[3538].Status, qr.States[3538].ProgressValue)
	if !retired || !scene.Cleared {
		t.Errorf("real cinematic APC destroy proof failed: target_retired=%t room_cleared=%t", retired, scene.Cleared)
	}
	friendlyState := runtimeDungeonMonsterState("")
	for _, actor := range rt.Room.Snapshot().ExtendedActors {
		if actor.ObjectKey == friendly.ObjectKey {
			friendlyState = actor.State
			break
		}
	}
	if friendlyState != runtimeDungeonMonsterAnnounced {
		t.Errorf("friendly APC10448 state=%q after hostile cinematic destroy", friendlyState)
	}
	if qr.States[3538].Status != "active" || qr.States[3538].ProgressValue != 1 {
		t.Fatal("middle room incorrectly completed boss quest")
	}
	b := make([]byte, dungeoncmd.MoveMapRequestSize)
	b[0], b[1] = 1, 1
	if err := svc.handleDungeonMoveMap(session, b); err != nil {
		t.Fatal(err)
	}
	after, _ := rt.Session.Scene()
	if after.Coordinate != (worldmap.RoomCoordinate{X: 1, Y: 1}) {
		t.Errorf("east door move from the fourth room did not commit: coordinate=%+v", after.Coordinate)
	}
}

const seekerQST007OnStartActionFixture = `[TRIGGER]
[ON START MAP]
[LIMIT]
1
[WHICH]
[CHARACTER]
[DO BEHAVIOR]
[ME]
0
[/TRIGGER]
[BEHAVIOR]
[CINEMATIC]
2216
[/BEHAVIOR]
`

func TestSeekerQST007OnStartActionRequiresOneShotSelfLink(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{
			name: "one shot self behavior",
			text: seekerQST007OnStartActionFixture,
			want: true,
		},
		{
			name: "wrong behavior index",
			text: strings.Replace(seekerQST007OnStartActionFixture, "[ME]\n0\n[/TRIGGER]", "[ME]\n9\n[/TRIGGER]", 1),
		},
		{
			name: "not on start",
			text: strings.Replace(seekerQST007OnStartActionFixture, "[ON START MAP]", "[ON SUCCESS TUTORIAL KEY]", 1),
		},
		{
			name: "unlimited or repeating limit",
			text: strings.Replace(seekerQST007OnStartActionFixture, "[LIMIT]\n1", "[LIMIT]\n0", 1),
		},
		{
			name: "another trigger target",
			text: strings.Replace(seekerQST007OnStartActionFixture, "[ME]\n0\n[/TRIGGER]", "[CHECKUP OBJECT]\n0\n[/TRIGGER]", 1),
		},
		{
			name: "extra trigger condition",
			text: strings.Replace(seekerQST007OnStartActionFixture, "[DO BEHAVIOR]", "[MONSTER]\n[DO BEHAVIOR]", 1),
		},
		{
			name: "behavior is not one cinematic",
			text: strings.Replace(seekerQST007OnStartActionFixture, "[CINEMATIC]\n2216\n[/BEHAVIOR]", "[CINEMATIC]\n2216\n[RANDOM]\n1\n[/BEHAVIOR]", 1),
		},
		{
			name: "unclosed behavior",
			text: strings.TrimSuffix(seekerQST007OnStartActionFixture, "[/BEHAVIOR]\n"),
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			document, err := worldmap.ParseDocument("map/dungeon/test/action/qst007.act", test.text)
			if err != nil {
				t.Fatal(err)
			}
			got := dungeonOnStartCinematics(document)
			if test.want {
				if !slices.Equal(got, []int64{2216}) {
					t.Fatalf("cinematics=%v want [2216]", got)
				}
				return
			}
			if len(got) != 0 {
				t.Fatalf("cinematics=%v; malformed ON START link must not authorize a CMT", got)
			}
		})
	}
}

const seekerQST007APCDestroyFixture = `[MAP]
100
[SCENE]
[BEHAVIOR]
[ACTOR]
[TYPE]
[APC]
[INDEX]
1
[POS]
-41 264
[/ACTOR]
[DESTROY]
[IS SHOW DIE]
0
[IS REWARD]
1
[/DESTROY]
[/BEHAVIOR]
[/SCENE]
`

func TestSeekerQST007StrictCMTRequiresExactClosedAPCSubjectDestroy(t *testing.T) {
	moveTarget := `[MAP]
100
[SCENE]
[BEHAVIOR]
[MOVE]
[ACTOR]
[TYPE]
[APC]
[INDEX]
1
[/ACTOR]
[/MOVE]
[DESTROY]
[/DESTROY]
[/BEHAVIOR]
[/SCENE]
`
	cases := []struct {
		name string
		text string
		want []int
	}{
		{
			name: "exact APC destroy subject",
			text: seekerQST007APCDestroyFixture,
			want: []int{1},
		},
		{
			name: "monster shares APC index",
			text: strings.Replace(seekerQST007APCDestroyFixture, "[APC]", "[MONSTER]", 1),
		},
		{
			name: "NPC shares APC index",
			text: strings.Replace(seekerQST007APCDestroyFixture, "[APC]", "[NPC]", 1),
		},
		{
			name: "move target APC is not destroy subject",
			text: moveTarget,
		},
		{
			name: "no destroy",
			text: strings.Replace(seekerQST007APCDestroyFixture, "[DESTROY]\n[IS SHOW DIE]\n0\n[IS REWARD]\n1\n[/DESTROY]\n", "", 1),
		},
		{
			name: "unclosed actor",
			text: strings.Replace(seekerQST007APCDestroyFixture, "[/ACTOR]\n", "", 1),
		},
		{
			name: "unclosed destroy",
			text: strings.Replace(seekerQST007APCDestroyFixture, "[/DESTROY]\n", "", 1),
		},
		{
			name: "unclosed scene",
			text: strings.TrimSuffix(seekerQST007APCDestroyFixture, "[/SCENE]\n"),
		},
		{
			name: "duplicate type",
			text: strings.Replace(seekerQST007APCDestroyFixture, "[TYPE]\n[APC]", "[TYPE]\n[APC]\n[TYPE]\n[APC]", 1),
		},
		{
			name: "duplicate index",
			text: strings.Replace(seekerQST007APCDestroyFixture, "[INDEX]\n1\n[POS]", "[INDEX]\n1\n[INDEX]\n1\n[POS]", 1),
		},
		{
			name: "negative index",
			text: strings.Replace(seekerQST007APCDestroyFixture, "[INDEX]\n1\n[POS]", "[INDEX]\n-1\n[POS]", 1),
		},
		{
			name: "non integer index",
			text: strings.Replace(seekerQST007APCDestroyFixture, "[INDEX]\n1\n[POS]", "[INDEX]\n`1`\n[POS]", 1),
		},
		{
			name: "wrong map",
			text: strings.Replace(seekerQST007APCDestroyFixture, "[MAP]\n100", "[MAP]\n101", 1),
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			document, err := worldmap.ParseDocument("cinematic/dungeon/test/qst007.cmt", test.text)
			if err != nil {
				t.Fatal(err)
			}
			usage, ok := parseDungeonCinematicAPCDestroyUsage(document, 100)
			if test.want == nil {
				if ok || len(usage.DestroyAPCIndexes) != 0 {
					t.Fatalf("usage=%+v valid=%t; malformed or non-APC subject must not authorize", usage, ok)
				}
				return
			}
			if !ok || usage.MapID != 100 || !slices.Equal(usage.DestroyAPCIndexes, test.want) {
				t.Fatalf("usage=%+v valid=%t want map=100 indexes=%v", usage, ok, test.want)
			}
		})
	}
}

type seekerQST007CinematicFixture struct {
	service  *Service
	session  *gameSession
	conn     *bufferConn
	repos    dnfrepo.Group
	source   bridgePVFSource
	target   runtimeDungeonExtendedActor
	friendly runtimeDungeonExtendedActor
}

func prepareSeekerQST007CinematicFixture(t *testing.T) seekerQST007CinematicFixture {
	t.Helper()
	ctx := context.Background()
	source := bridgeDungeonPVF(false)
	source[worldmap.DefaultMapList] = "100 `dungeon/test/start.map`\n101 `dungeon/test/boss.map`\n"
	source["map/dungeon/test/start.map"] = `[map name]
` + "`QST007 intermediate`" + `
[dungeon]
700
[type]
` + "`[normal]`" + `
[ai character]
4001 30 20 0 ` + "`[monster]`" + ` ` + "`[normal]`" + ` 0 0 4002 40 20 0 ` + "`[monster]`" + ` ` + "`[normal]`" + ` 1 0
[basic action]
` + "`Action/qst007.act`" + `
`
	source["map/dungeon/test/boss.map"] = `[map name]
` + "`QST007 boss`" + `
[dungeon]
700
[type]
` + "`[boss]`" + `
`
	source[worldmap.DefaultDungeonList] = "700 `test.dgn`\n"
	source["dungeon/test.dgn"] = `[name]
` + "`Synthetic QST007`" + `
[minimum required level]
1
[basis level]
20
[limit party count]
1
[maze info]
[size]
2 1
[greed]
` + "`AB`" + `
[map specification]
` + "`map`" + ` 0 0 100 ` + "`boss`" + ` 1 0 101
[start map]
0 0
[/start map]
[boss map]
1 0
[/boss map]
`
	source[defaultDungeonAICharacterList] = "4001 `Test/Other.aic`\n4002 `Test/Target.aic`\n"
	source["AICharacter/Test/Other.aic"] = "[minimum info]\n`QST007 Other APC` 1 2 3 4 25\n"
	// Deliberately no PROC: the cinematic link, not a PROC action, authorizes
	// this synthetic owner-ffff/no-combat retirement.
	source["AICharacter/Test/Target.aic"] = "[minimum info]\n`QST007 Target APC` 1 2 3 4 25\n"
	source["map/dungeon/test/action/qst007.act"] = seekerQST007OnStartActionFixture
	source[defaultDungeonCinematicList] = "2216 `Dungeon/QST007/entry.cmt`\n9999 `Dungeon/QST007/same-map.cmt`\n"
	source["cinematic/Dungeon/QST007/entry.cmt"] = seekerQST007APCDestroyFixture
	source["cinematic/Dungeon/QST007/same-map.cmt"] = seekerQST007APCDestroyFixture

	ai, err := newPVFDungeonAICharacterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	runtime := prepareSyntheticDungeonRuntimeForEntryTest(t, source, ai)
	runtime.Dungeon.Mazes[runtime.MazeIndex].QuestConnection = []int64{0, 9001, -1}
	if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		t.Fatal(err)
	}
	questSource := questListTestSource{
		dnfquest.DefaultList: "9001 `qst007.qst`\n",
		"n_quest/qst007.qst": "[type]\n`[clear map]`\n[int data]\n101\n",
	}
	index, err := dnfpvf.Build(ctx, questSource, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	quests, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, runtime.Character); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "99", States: map[int64]dnfrepo.QuestState{9001: {Status: "active", ProgressValue: 1}}}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:                 options{accountID: "account-1", currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		dungeonAICharacterTable: ai,
		questCatalog:            quests,
		repositoryProvider:      func() (dnfrepo.Group, bool) { return repos, true },
	}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "qst007-synthetic", selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
	var target, friendly runtimeDungeonExtendedActor
	for _, actor := range runtime.Room.Snapshot().ExtendedActors {
		if actor.AICharacter == nil {
			continue
		}
		switch actor.AICharacter.Code {
		case 4001:
			friendly = actor
		case 4002:
			target = actor
		}
	}
	if target.ObjectKey == 0 || target.HostileReference == nil || target.HostileReference.Index != 1 || friendly.ObjectKey == 0 {
		t.Fatalf("synthetic actors=%+v target=%+v friendly=%+v", runtime.Room.Snapshot().ExtendedActors, target, friendly)
	}
	return seekerQST007CinematicFixture{
		service:  service,
		session:  session,
		conn:     conn,
		repos:    repos,
		source:   source,
		target:   target,
		friendly: friendly,
	}
}

func TestSeekerQST007SyntheticProofUsesLinkedCMTAndSharedGuards(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*seekerQST007CinematicFixture, *dungeoncmd.DieMonsterRequest, *worldmap.DungeonRoomScene) error
		want   bool
	}{
		{name: "valid linked exact APC without proc", want: true},
		{
			name: "wrong owner",
			mutate: func(_ *seekerQST007CinematicFixture, request *dungeoncmd.DieMonsterRequest, _ *worldmap.DungeonRoomScene) error {
				request.OwnerObjectKey = 1
				return nil
			},
		},
		{
			name: "combat contribution tail",
			mutate: func(_ *seekerQST007CinematicFixture, request *dungeoncmd.DieMonsterRequest, _ *worldmap.DungeonRoomScene) error {
				request.CombatEntryCount = 1
				return nil
			},
		},
		{
			name: "wrong map",
			mutate: func(_ *seekerQST007CinematicFixture, _ *dungeoncmd.DieMonsterRequest, scene *worldmap.DungeonRoomScene) error {
				scene.Map.Map.ID++
				return nil
			},
		},
		{
			name: "wrong room",
			mutate: func(_ *seekerQST007CinematicFixture, _ *dungeoncmd.DieMonsterRequest, scene *worldmap.DungeonRoomScene) error {
				scene.Coordinate.X++
				return nil
			},
		},
		{
			name: "wrong character",
			mutate: func(fixture *seekerQST007CinematicFixture, _ *dungeoncmd.DieMonsterRequest, _ *worldmap.DungeonRoomScene) error {
				fixture.session.selectedCharacterID = 98
				return nil
			},
		},
		{
			name: "inactive task",
			mutate: func(fixture *seekerQST007CinematicFixture, _ *dungeoncmd.DieMonsterRequest, _ *worldmap.DungeonRoomScene) error {
				return fixture.repos.Quest.Save(context.Background(), dnfrepo.QuestRecord{
					CharacterID: "99",
					States:      map[int64]dnfrepo.QuestState{9001: {Status: "completed"}},
				})
			},
		},
		{
			name: "same map unrelated CMT",
			mutate: func(fixture *seekerQST007CinematicFixture, _ *dungeoncmd.DieMonsterRequest, _ *worldmap.DungeonRoomScene) error {
				fixture.source["cinematic/Dungeon/QST007/entry.cmt"] = strings.Replace(
					seekerQST007APCDestroyFixture,
					"[INDEX]\n1",
					"[INDEX]\n0",
					1,
				)
				return nil
			},
		},
		{
			name: "friendly actor",
			mutate: func(fixture *seekerQST007CinematicFixture, _ *dungeoncmd.DieMonsterRequest, scene *worldmap.DungeonRoomScene) error {
				index := fixture.target.HostileReference.Index
				scene.AICharacters[index].Faction = "character"
				scene.Map.Map.AICharacters[index].Faction = "character"
				for actorIndex := range fixture.session.dungeon.runtime.Room.extendedActors {
					actor := &fixture.session.dungeon.runtime.Room.extendedActors[actorIndex]
					if actor.ObjectKey == fixture.target.ObjectKey && actor.AICharacter != nil {
						actor.AICharacter.Faction = "character"
					}
				}
				return nil
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := prepareSeekerQST007CinematicFixture(t)
			scene, ok := fixture.session.dungeon.runtime.Session.Scene()
			if !ok {
				t.Fatal("synthetic scene missing")
			}
			request, err := fixture.service.decodeCurrentDungeonMonsterDeath(seekerQST007NativeDeathBody(fixture.target.ObjectKey))
			if err != nil {
				t.Fatal(err)
			}
			if test.mutate != nil {
				if err := test.mutate(&fixture, &request, &scene); err != nil {
					t.Fatal(err)
				}
			}
			evidence, proved := fixture.service.currentDungeonStoryProcDeath(
				fixture.session,
				fixture.session.dungeon.runtime,
				scene,
				request,
			)
			if proved != test.want {
				t.Fatalf("evidence=%+v proved=%t want=%t", evidence, proved, test.want)
			}
			if proved && (evidence.CinematicID != 2216 || evidence.AICharacterID != 4002 ||
				evidence.BehaviorIndex != 1 || evidence.PassiveObjectID != 0) {
				t.Fatalf("cinematic evidence=%+v", evidence)
			}
		})
	}
}

func TestSeekerQST007SyntheticDeathNotifiesIdempotentlyWithoutAssets(t *testing.T) {
	assertUnchanged := func(t *testing.T, fixture seekerQST007CinematicFixture, beforeCharacter dnfrepo.CharacterRecord, beforeInventory dnfrepo.InventoryRecord) {
		t.Helper()
		afterCharacter, _, err := fixture.repos.Character.Load(context.Background(), "99")
		if err != nil {
			t.Fatal(err)
		}
		afterInventory, _, err := fixture.repos.Inventory.Load(context.Background(), "99")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(beforeCharacter, afterCharacter) || !reflect.DeepEqual(beforeInventory, afterInventory) {
			t.Fatal("cinematic APC death changed player assets")
		}
		quests, _, err := fixture.repos.Quest.Load(context.Background(), "99")
		if err != nil {
			t.Fatal(err)
		}
		if state := quests.States[9001]; state.Status != "active" || state.ProgressValue != 1 {
			t.Fatalf("intermediate task changed prematurely: %+v", state)
		}
	}
	assertTargetOnly := func(t *testing.T, fixture seekerQST007CinematicFixture) {
		t.Helper()
		if keys := storyAIBossDeathNotificationKeys(t, fixture.conn.write.Bytes()); !slices.Equal(keys, []uint32{fixture.target.ObjectKey}) {
			t.Fatalf("death notifications=%v want only target=%d", keys, fixture.target.ObjectKey)
		}
		var targetState, friendlyState runtimeDungeonMonsterState
		for _, actor := range fixture.session.dungeon.runtime.Room.Snapshot().ExtendedActors {
			switch actor.ObjectKey {
			case fixture.target.ObjectKey:
				targetState = actor.State
			case fixture.friendly.ObjectKey:
				friendlyState = actor.State
			}
		}
		if targetState != runtimeDungeonMonsterDefeated || friendlyState != runtimeDungeonMonsterAnnounced {
			t.Fatalf("target state=%q other APC state=%q", targetState, friendlyState)
		}
	}

	t.Run("success and duplicate", func(t *testing.T) {
		fixture := prepareSeekerQST007CinematicFixture(t)
		beforeCharacter, _, err := fixture.repos.Character.Load(context.Background(), "99")
		if err != nil {
			t.Fatal(err)
		}
		beforeInventory, _, err := fixture.repos.Inventory.Load(context.Background(), "99")
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.service.handleDungeonMonsterDeath(fixture.session, seekerQST007NativeDeathBody(fixture.target.ObjectKey)); err != nil {
			t.Fatal(err)
		}
		assertTargetOnly(t, fixture)
		assertUnchanged(t, fixture, beforeCharacter, beforeInventory)

		fixture.conn.write.Reset()
		if err := fixture.service.handleDungeonMonsterDeath(fixture.session, seekerQST007NativeDeathBody(fixture.target.ObjectKey)); err != nil {
			t.Fatal(err)
		}
		assertTargetOnly(t, fixture)
		assertUnchanged(t, fixture, beforeCharacter, beforeInventory)
	})

	t.Run("notification write failure then retry", func(t *testing.T) {
		fixture := prepareSeekerQST007CinematicFixture(t)
		beforeCharacter, _, err := fixture.repos.Character.Load(context.Background(), "99")
		if err != nil {
			t.Fatal(err)
		}
		beforeInventory, _, err := fixture.repos.Inventory.Load(context.Background(), "99")
		if err != nil {
			t.Fatal(err)
		}
		injected := errors.New("qst007 notification write failure")
		fixture.session.conn = &failNthDungeonWriteConn{failAt: 1, err: injected}
		err = fixture.service.handleDungeonMonsterDeath(fixture.session, seekerQST007NativeDeathBody(fixture.target.ObjectKey))
		if !errors.Is(err, injected) {
			t.Fatalf("write error=%v want injected", err)
		}
		fixture.session.conn = fixture.conn
		fixture.conn.write.Reset()
		if err := fixture.service.handleDungeonMonsterDeath(fixture.session, seekerQST007NativeDeathBody(fixture.target.ObjectKey)); err != nil {
			t.Fatal(err)
		}
		assertTargetOnly(t, fixture)
		assertUnchanged(t, fixture, beforeCharacter, beforeInventory)
	})
}

// The matched show=0 APC writer emits reason 7 at offset 68 for count=0.
// Network checksum remains the existing decoder fixture's zero value.
func seekerQST007NativeDeathBody(objectKey uint32) []byte {
	body := storyProcDeathBody(objectKey)
	body[68] = 7
	return body
}

func TestSeekerQST007NativeDestroyFlags(t *testing.T) {
	cases := []struct {
		name, old, replacement string
		want                   bool
	}{
		{"native defaults", "[IS SHOW DIE]\n0\n[IS REWARD]\n1\n", "", true},
		{"reward disabled still sends", "[IS REWARD]\n1", "[IS REWARD]\n0", true},
		{"show HP path excluded", "[IS SHOW DIE]\n0", "[IS SHOW DIE]\n1", false},
		{"duplicate show", "[IS SHOW DIE]\n0", "[IS SHOW DIE]\n0\n[IS SHOW DIE]\n0", false},
		{"duplicate reward", "[IS REWARD]\n1", "[IS REWARD]\n1\n[IS REWARD]\n0", false},
		{"unknown show", "[IS SHOW DIE]\n0", "[IS SHOW DIE]\n2", false},
		{"unknown reward", "[IS REWARD]\n1", "[IS REWARD]\n-1", false},
		{"wrong type", "[IS REWARD]\n1", "[IS REWARD]\n`1`", false},
		{"extra value", "[IS REWARD]\n1", "[IS REWARD]\n1 0", false},
		{"unknown field", "[IS REWARD]\n1", "[UNKNOWN]\n1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := worldmap.ParseDocument("cinematic/qst007.cmt", strings.Replace(seekerQST007APCDestroyFixture, tc.old, tc.replacement, 1))
			if err != nil {
				t.Fatal(err)
			}
			_, got := parseDungeonCinematicAPCDestroyUsage(doc, 100)
			if got != tc.want {
				t.Fatalf("accepted=%t want=%t", got, tc.want)
			}
		})
	}
}
