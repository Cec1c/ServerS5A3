package dnfbridge

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

const storyProcDeathFixture = `[TRIGGER]
[WHICH]
[ME]
[DO PROC BEHAVIOR]
[ME]
0
[/TRIGGER]
[BEHAVIOR]
[TRIGGER]
1
[ON]
[/BEHAVIOR]
[TRIGGER]
[ENABLE]
[OFF]
[LIMIT]
1
[WHICH]
[PASSIVE]
[CHECKUP]
[IS INDEX]
55
[/IS INDEX]
[DISTANCE]
[<=]
400
[/CHECKUP]
[CHECKED NO]
[>=]
1
[DO PROC BEHAVIOR]
[ME]
1
[/TRIGGER]
[BEHAVIOR]
[SET HP]
0
[/BEHAVIOR]
`

func TestStoryProcDeathParsingRequiresExplicitSelfKill(t *testing.T) {
	cases := []struct {
		name, text string
		want       bool
	}{
		{"explicit proc with trigger-on command", storyProcDeathFixture, true},
		{"another target", strings.Replace(storyProcDeathFixture, "[ME]\n1\n[/TRIGGER]", "[CHECKUP OBJECT]\n1\n[/TRIGGER]", 1), false},
		{"not a passive selector", strings.Replace(storyProcDeathFixture, "[PASSIVE]", "[MONSTER]", 1), false},
		{"HP comparison is not a kill", strings.Replace(storyProcDeathFixture, "[SET HP]\n0", "[HP]\n[<=]\n0", 1), false},
		{"nonzero HP", strings.Replace(storyProcDeathFixture, "[SET HP]\n0", "[SET HP]\n1", 1), false},
		{"numeric text", strings.Replace(storyProcDeathFixture, "[SET HP]\n0", "[SET HP]\n`0`", 1), false},
		{"unknown extra behavior", strings.Replace(storyProcDeathFixture, "[SET HP]\n0", "[SET HP]\n0\n[RANDOM]\n1", 1), false},
		{"wrong behavior index", strings.Replace(storyProcDeathFixture, "[ME]\n1\n[/TRIGGER]", "[ME]\n9\n[/TRIGGER]", 1), false},
		{"zero passive ID", strings.Replace(storyProcDeathFixture, "55\n", "0\n", 1), false},
		{"incomplete block", strings.TrimSuffix(storyProcDeathFixture, "[/BEHAVIOR]\n"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := worldmap.ParseDocument("AICharacter/Test/proc.act", tc.text)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := parseDungeonStoryProcDeath(doc)
			if ok != tc.want {
				t.Fatalf("evidence=%+v valid=%t want=%t", got, ok, tc.want)
			}
			if ok && (got.PassiveObjectID != 55 || got.BehaviorIndex != 1) {
				t.Fatalf("evidence=%+v", got)
			}
		})
	}
}

func prepareStoryProcDeathTest(t *testing.T) (*Service, *gameSession, *bufferConn, dnfrepo.Group, bridgePVFSource, uint32) {
	t.Helper()
	ctx := context.Background()
	source := bridgeDungeonPVF(false)
	source["map/dungeon/test/start.map"] = "[dungeon]\n700\n[type]\n`[boss]`\n[ai character]\n4002 30 20 0 `[monster]` `[boss]` 0 0\n"
	source[defaultDungeonAICharacterList] = "4002 `Test/Script.aic`\n"
	source["AICharacter/Test/Script.aic"] = "[minimum info]\n`Script APC` 1 2 3 4 25\n[proc action]\n`proc.act`\n"
	source["AICharacter/Test/proc.act"] = storyProcDeathFixture
	catalog, err := newPVFDungeonAICharacterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	rt := prepareSyntheticDungeonRuntimeForEntryTest(t, source, catalog)
	rt.Dungeon.Mazes[rt.MazeIndex].QuestConnection = []int64{0, 9001, -1}
	if _, err := rt.Room.AnnounceAllActors(rt.Session); err != nil {
		t.Fatal(err)
	}
	qs := questListTestSource{dnfquest.DefaultList: "9001 `script.qst`\n", "n_quest/script.qst": "[type]\n`[clear map]`\n[int data]\n100\n"}
	index, err := dnfpvf.Build(ctx, qs, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	quests, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, rt.Character); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "99", States: map[int64]dnfrepo.QuestState{9001: {Status: "active", ProgressValue: 1}}}); err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{accountID: "account-1", currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain}, dungeonAICharacterTable: catalog, questCatalog: quests, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: rt}}
	return s, session, conn, repos, source, rt.Room.Snapshot().ExtendedActors[0].ObjectKey
}

func TestStoryProcDeathStrictRuntimeGuards(t *testing.T) {
	for _, name := range []string{"valid", "other client", "other character", "other map", "other room", "unbound", "wrong reference", "planned", "ordinary kind", "friendly faction", "wrong AI metadata", "wrong packet code", "no active quest", "wrong quest map", "no proc", "missing proc", "nonzero proc", "fixed layout", "combat contribution", "opaque tail", "zero key", "unrecorded defeated"} {
		t.Run(name, func(t *testing.T) {
			s, session, _, repos, source, key := prepareStoryProcDeathTest(t)
			rt := session.dungeon.runtime
			scene, _ := rt.Session.Scene()
			req, err := s.decodeCurrentDungeonMonsterDeath(storyProcDeathBody(key))
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "other client":
				s.options.currentClientVersion = "90cn-decode-bypass-v1"
			case "other character":
				session.selectedCharacterID = 98
			case "other map":
				scene.Map.Map.ID++
			case "other room":
				scene.Coordinate.X++
			case "unbound":
				delete(scene.RuntimeObjects, key)
			case "wrong reference":
				scene.RuntimeObjects[key] = worldmap.HostileReference{Kind: worldmap.HostileMonster, Index: 0}
			case "planned":
				rt.Room.extendedActors[0].State = runtimeDungeonMonsterPlanned
			case "ordinary kind":
				rt.Room.extendedActors[0].Kind = runtimeDungeonActorSpecialMonster
			case "friendly faction":
				rt.Room.extendedActors[0].AICharacter.Faction = "character"
			case "wrong AI metadata":
				rt.Room.extendedActors[0].AICharacterMetadata.ID++
			case "wrong packet code":
				rt.Room.extendedActors[0].Packet.Code++
			case "no active quest":
				if err := repos.Quest.Save(context.Background(), dnfrepo.QuestRecord{CharacterID: "99", States: map[int64]dnfrepo.QuestState{}}); err != nil {
					t.Fatal(err)
				}
			case "wrong quest map":
				rt.Dungeon.Mazes[rt.MazeIndex].QuestConnection = []int64{0, 9002, -1}
			case "no proc":
				s.dungeonAICharacterTable.cache[4002] = pvfDungeonAICharacterDefinition{ID: 4002, Path: "AICharacter/Test/Script.aic"}
			case "missing proc":
				delete(source, "AICharacter/Test/proc.act")
			case "nonzero proc":
				source["AICharacter/Test/proc.act"] = strings.Replace(storyProcDeathFixture, "[SET HP]\n0", "[SET HP]\n1", 1)
			case "fixed layout":
				req.Layout = dungeoncmd.DieMonsterRequestLayoutFixed62
			case "combat contribution":
				req.CombatEntryCount = 1
			case "opaque tail":
				req.OpaqueTail = []byte{1}
			case "zero key":
				req.RuntimeObjectKey = 0
			case "unrecorded defeated":
				if _, _, err := rt.Room.CommitActorDeathReport(key, rt.Session); err != nil {
					t.Fatal(err)
				}
				scene, _ = rt.Session.Scene()
			}
			evidence, ok := s.currentDungeonStoryProcDeath(session, rt, scene, req)
			if ok != (name == "valid") {
				t.Fatalf("evidence=%+v valid=%t", evidence, ok)
			}
		})
	}
}

func TestStoryProcDeathWriteRetryAndDuplicateDoNotGrantAssets(t *testing.T) {
	for _, failAt := range []int{0, 1, 3, 4} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			s, session, conn, repos, _, key := prepareStoryProcDeathTest(t)
			rt := session.dungeon.runtime
			before, _, err := repos.Character.Load(context.Background(), "99")
			if err != nil {
				t.Fatal(err)
			}
			beforeInventory, _, err := repos.Inventory.Load(context.Background(), "99")
			if err != nil {
				t.Fatal(err)
			}
			injected := errors.New("script death write failure")
			if failAt > 0 {
				session.conn = &failNthDungeonWriteConn{failAt: failAt, err: injected}
			}
			err = s.handleDungeonMonsterDeath(session, storyProcDeathBody(key))
			if failAt == 0 && err != nil {
				t.Fatal(err)
			}
			if failAt > 0 && !errors.Is(err, injected) {
				t.Fatalf("write error=%v want injected", err)
			}
			session.conn = conn
			for attempt := 0; attempt < 2; attempt++ {
				conn.write.Reset()
				if err := s.handleDungeonMonsterDeath(session, storyProcDeathBody(key)); err != nil {
					t.Fatal(err)
				}
				keys := storyAIBossDeathNotificationKeys(t, conn.write.Bytes())
				if len(keys) != 1 || keys[0] != key {
					t.Fatalf("retry death keys=%v", keys)
				}
				if !rt.bossDieCheckAccepted || !rt.settlementEntrySent || !rt.clearMapCompletionPhaseAPersisted {
					t.Fatalf("incomplete retry accepted=%t entry=%t phaseA=%t", rt.bossDieCheckAccepted, rt.settlementEntrySent, rt.clearMapCompletionPhaseAPersisted)
				}
			}
			after, _, err := repos.Character.Load(context.Background(), "99")
			if err != nil {
				t.Fatal(err)
			}
			inv, _, err := repos.Inventory.Load(context.Background(), "99")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeInventory, inv) {
				t.Fatal("script death/replay changed player assets")
			}
			qr, _, err := repos.Quest.Load(context.Background(), "99")
			if err != nil {
				t.Fatal(err)
			}
			if qr.States[9001].ProgressValue != 0 {
				t.Fatalf("quest trigger=%+v", qr)
			}
		})
	}
}
