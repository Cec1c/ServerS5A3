package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type seekerQST012QuestCase struct {
	questID           int64
	name              string
	questLevelMin     int
	level             int
	prerequisite      int64
	dungeonID         uint32
	mazeIndex         int
	startMapID        int64
	targetMapID       int64
	startOwnerCode    int64
	startOwnerKind    uint16
	fallbackOwnerCode int64
	targetOwnerCode   int64
}

var seekerQST012QuestCases = []seekerQST012QuestCase{
	{
		questID: 3188, name: "天空之城的邪恶魔力", questLevelMin: 22, level: 23, prerequisite: 3187,
		dungeonID: 15, mazeIndex: 4, startMapID: 57953, targetMapID: 57962,
		startOwnerCode: 65488, startOwnerKind: 0x211, fallbackOwnerCode: 2013,
	},
	{
		questID: 3190, name: "光之城主", questLevelMin: 23, level: 23, prerequisite: 3189,
		dungeonID: 15, mazeIndex: 5, startMapID: 57971, targetMapID: 57979,
		startOwnerCode: 2012, startOwnerKind: 0x111,
		targetOwnerCode: 65488,
	},
	{
		questID: 3197, name: "GBL教的神殿", questLevelMin: 24, level: 24, prerequisite: 3196,
		dungeonID: 21, mazeIndex: 1, startMapID: 53351, targetMapID: 53357,
		startOwnerCode: 2001, startOwnerKind: 0x111,
	},
}

// The feedback names three clear-map quests. Accept and maze selection are real;
// smoke clearing previous rooms only reaches the target. Every target death in
// this test is submitted through the actual op39 handler with a friendly object
// key that was announced by the real MAP/AIC declaration.
func TestSeekerQST012FriendlyAPCDeathsRealPVF(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the QST-012 Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := archive.Snapshot()
	if snapshot.Checksum != "fef4f46f6c3096d10cd88c84579dea8bd4fffa18e5a75b4684a2e920864f5525" {
		t.Fatalf("PVF checksum=%s", snapshot.Checksum)
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

	for _, test := range seekerQST012QuestCases {
		test := test
		t.Run(strconv.FormatInt(test.questID, 10)+"-"+test.name, func(t *testing.T) {
			seekerQST012RunQuestCase(t, ctx, archive, table, resolver, monsters, aiCharacters, questCatalog, test)
		})
	}
}

func seekerQST012RunQuestCase(
	t *testing.T,
	ctx context.Context,
	archive *platformpvf.Archive,
	table *worldmap.Table,
	resolver *worldmap.Resolver,
	monsters *pvfDungeonMonsterCatalog,
	aiCharacters *pvfDungeonAICharacterCatalog,
	questCatalog *dnfquest.Catalog,
	test seekerQST012QuestCase,
) {
	t.Helper()
	definition, ok := questCatalog.Find(test.questID)
	if !ok {
		t.Fatalf("quest %d is missing", test.questID)
	}
	if definition.Name != test.name || definition.Type != "[clear map]" ||
		len(definition.IntData) != 1 || definition.IntData[0] != test.targetMapID ||
		definition.LevelMin != test.questLevelMin ||
		len(definition.PreRequiredGroups) != 1 ||
		len(definition.PreRequiredGroups[0]) != 1 ||
		definition.PreRequiredGroups[0][0] != test.prerequisite {
		t.Fatalf("quest identity=%+v", definition)
	}
	dungeon, ok := table.FindDungeon(int64(test.dungeonID))
	if !ok {
		t.Fatalf("dungeon %d is missing", test.dungeonID)
	}
	if test.mazeIndex < 0 || test.mazeIndex >= len(dungeon.Mazes) ||
		!realMazeConnectsQuest(dungeon.Mazes[test.mazeIndex], test.questID) {
		t.Fatalf("dungeon %d maze %d does not connect quest %d", test.dungeonID, test.mazeIndex, test.questID)
	}

	repositories, accountID, characterID := seekerQST012AcceptQuest(t, ctx, questCatalog, test)
	conn := &bufferConn{}
	service := &Service{
		options: options{
			accountID:            accountID,
			currentClientVersion: "official-24.9",
			gameUpperBodyCodec:   gameUpperBodyCodecPlain,
		},
		worldMapTable:           table,
		worldMapResolver:        resolver,
		dungeonMonsterTable:     monsters,
		dungeonAICharacterTable: aiCharacters,
		questCatalog:            questCatalog,
		dungeonChoice:           func(int) (int, error) { return 0, nil },
		dungeonSeed:             func() (uint32, error) { return 1, nil },
		repositoryProvider:      func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{
		conn:                conn,
		connID:              fmt.Sprintf("qst012-real-pvf-%d", test.questID),
		selectedCharacterID: 19,
		channel:             channelcatalog.Channel{ID: 19},
	}
	runtime, _, err := service.prepareDungeonRuntime(
		ctx,
		session,
		dungeoncmd.SelectDungeonRequest{DungeonID: test.dungeonID, Difficulty: 0},
	)
	if err != nil {
		t.Fatalf("prepare real dungeon %d quest %d: %v", test.dungeonID, test.questID, err)
	}
	session.dungeon.runtime = runtime
	if runtime.MazeIndex != test.mazeIndex {
		t.Fatalf("selected maze=%d want=%d connection=%v", runtime.MazeIndex, test.mazeIndex, dungeon.Mazes[runtime.MazeIndex].QuestConnection)
	}
	startScene, _ := runtime.Session.Scene()
	if startScene.Map.Map.ID != test.startMapID {
		t.Fatalf("start map=%d want=%d", startScene.Map.Map.ID, test.startMapID)
	}
	seekerQST012EnsureAnnounced(t, runtime, "start")

	startOwner, ownerSource := seekerQST012FindStartOwner(t, archive, runtime, test)
	fallbackOwner := uint32(0)
	if test.fallbackOwnerCode != 0 {
		fallbackOwner = seekerQST012FindFriendlyAIByCode(t, runtime, test.fallbackOwnerCode)
	}
	t.Logf("quest=%d accept=active/1 start=%d maze=%d owner=%d kind=%#x source=%s",
		test.questID, test.startMapID, runtime.MazeIndex, startOwner, test.startOwnerKind, ownerSource)

	seekerQST012ExerciseSameRoomAndControls(t, service, session, conn, repositories, characterID, test, startOwner)
	seekerQST012ReachTarget(t, service, session, runtime, resolver, test)

	targetScene, _ := runtime.Session.Scene()
	if targetScene.Map.Map.ID != test.targetMapID {
		t.Fatalf("target map=%d want=%d", targetScene.Map.Map.ID, test.targetMapID)
	}
	seekerQST012EnsureAnnounced(t, runtime, "target")
	seekerQST012AssertTargetFriendlyFacts(t, archive, runtime, test)
	t.Logf("actual room transition reached coordinate=%s map=%d", targetScene.Coordinate, targetScene.Map.Map.ID)

	workingOwner := startOwner
	targetFriendly := uint32(0)
	if test.targetOwnerCode != 0 {
		targetFriendly = seekerQST012FindFriendlyMonster(t, archive, runtime, test.targetOwnerCode, true)
		workingOwner = targetFriendly
		t.Logf("target-map friendly monster=%d code=%d follow=true", targetFriendly, test.targetOwnerCode)
	}

	firstTarget, ok := seekerQST012LiveHostile(runtime)
	if !ok {
		t.Fatal("target room has no live hostile")
	}
	crossOwner, crossKind := startOwner, test.startOwnerKind
	crossPackets, crossAccepted := seekerQST012SendDeath(t, service, session, conn, firstTarget.ObjectKey, crossOwner, crossKind)
	if !crossAccepted {
		t.Errorf("cross-room friendly owner %d did not kill target %d on map %d; quest remains untested at its real target",
			startOwner, firstTarget.ObjectKey, test.targetMapID)
		if fallbackOwner == 0 {
			record, found, err := repositories.Quest.Load(ctx, characterID)
			if err != nil || !found {
				t.Fatalf("load rejected-target quest found=%t err=%v", found, err)
			}
			state := record.States[test.questID]
			t.Logf("quest=%d target map=%d rejected state=%s/%d", test.questID, test.targetMapID, state.Status, state.ProgressValue)
			return
		}
		crossPackets, crossAccepted = seekerQST012SendDeath(t, service, session, conn, firstTarget.ObjectKey, fallbackOwner, 0x111)
		if !crossAccepted {
			t.Errorf("fallback real friendly AI owner %d did not kill target %d on map %d", fallbackOwner, firstTarget.ObjectKey, test.targetMapID)
			return
		}
		workingOwner = fallbackOwner
		crossOwner, crossKind = fallbackOwner, 0x111
		t.Logf("cross-room fallback friendly AI owner=%d accepted target=%d; monster-owner gap remains", fallbackOwner, firstTarget.ObjectKey)
	} else {
		t.Logf("cross-room friendly owner=%d accepted target=%d", startOwner, firstTarget.ObjectKey)
	}
	if !seekerQST012HasOp38(crossPackets, firstTarget.ObjectKey) {
		t.Errorf("cross-room friendly target %d death produced no op38", firstTarget.ObjectKey)
	}

	replayPackets, _ := seekerQST012SendDeath(t, service, session, conn, firstTarget.ObjectKey, crossOwner, crossKind)
	if !seekerQST012HasOp38(replayPackets, firstTarget.ObjectKey) {
		t.Errorf("duplicate target death did not replay the authoritative op38 for object=%d", firstTarget.ObjectKey)
	}

	if targetFriendly != 0 {
		target, ok := seekerQST012LiveHostile(runtime)
		if !ok {
			t.Fatal("target room has no second hostile for same-room friendly proof")
		}
		packets, accepted := seekerQST012SendDeath(t, service, session, conn, target.ObjectKey, targetFriendly, 0x211)
		if !accepted {
			t.Errorf("same-room target friendly %d did not kill %d", targetFriendly, target.ObjectKey)
		} else if !seekerQST012HasOp38(packets, target.ObjectKey) {
			t.Errorf("same-room target friendly kill produced no op38 for object=%d", target.ObjectKey)
		} else {
			t.Logf("same-room target friendly owner=%d accepted target=%d", targetFriendly, target.ObjectKey)
		}
	}

	for attempts := 0; attempts < 32; attempts++ {
		target, ok := seekerQST012LiveHostile(runtime)
		if !ok {
			break
		}
		packets, accepted := seekerQST012SendDeath(t, service, session, conn, target.ObjectKey, workingOwner, test.startOwnerKind)
		if !accepted {
			t.Errorf("target room left live hostile object=%d after friendly owner=%d was already accepted", target.ObjectKey, workingOwner)
			break
		}
		if !seekerQST012HasOp38(packets, target.ObjectKey) {
			t.Errorf("friendly target death produced no op38 for object=%d", target.ObjectKey)
		}
	}

	finalScene, _ := runtime.Session.Scene()
	runSnapshot := runtime.Session.Snapshot()
	if !finalScene.Cleared || runSnapshot.Run.Status != worldmap.DungeonRunCompleted {
		t.Errorf("target room map=%d cleared=%t run=%s after all live hostiles", finalScene.Map.Map.ID, finalScene.Cleared, runSnapshot.Run.Status)
	}
	record, found, err := repositories.Quest.Load(ctx, characterID)
	if err != nil || !found {
		t.Fatalf("load quest state found=%t err=%v", found, err)
	}
	state := record.States[test.questID]
	if state.Status != "active" || state.ProgressValue != 0 ||
		state.Extra["completion_map_id"] != strconv.FormatInt(test.targetMapID, 10) ||
		state.Extra["completion_key"] == "" {
		t.Errorf("quest %d target state=%+v; want active/0 with completion map=%d", test.questID, state, test.targetMapID)
	}
	t.Logf("quest=%d target map=%d state=%s/%d completion_map=%s completion_key=%s",
		test.questID, test.targetMapID, state.Status, state.ProgressValue,
		state.Extra["completion_map_id"], state.Extra["completion_key"])
}

func seekerQST012AcceptQuest(
	t *testing.T,
	ctx context.Context,
	catalog *dnfquest.Catalog,
	test seekerQST012QuestCase,
) (dnfrepo.Group, string, string) {
	t.Helper()
	const accountID = defaultAccountPrefix + "1"
	const characterID = "19"
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: characterID, AccountID: accountID, Name: "QST012", Job: "0",
		Level: test.level,
		Stats: map[string]int64{"grow_type": 0, "exp": 0, "fatigue": 156},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Skill.Save(ctx, dnfrepo.SkillRecord{
		CharacterID: characterID,
		Points: dnfrepo.SkillPointState{
			SyncedLevel: test.level, TotalSP: 100, RemainingSP: 100, TotalTP: 20, RemainingTP: 20,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: characterID, Slots: map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: characterID,
		States: map[int64]dnfrepo.QuestState{
			test.prerequisite: {Status: "completed", ProgressValue: 0},
		},
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := dnfquest.NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := owner.ApplyAccept(
		ctx,
		catalog,
		dnfquest.CharacterEligibility{Level: test.level, Job: 0, GrowType: 0},
		dnfquest.NewQuestIDCommand(
			alignedcmd.Request{AccountID: accountID, SelectedCharacterID: 19},
			"accept_quest",
			dnfquest.QuestIDRequest{QuestID: uint16(test.questID)},
		),
	)
	if err != nil {
		t.Fatalf("real ApplyAccept quest=%d level=%d: %v", test.questID, test.level, err)
	}
	if int64(accepted.QuestID) != test.questID || accepted.Idempotent {
		t.Fatalf("accept result=%+v", accepted)
	}
	record, found, err := repositories.Quest.Load(ctx, characterID)
	if err != nil || !found {
		t.Fatalf("load accepted quest found=%t err=%v", found, err)
	}
	state := record.States[test.questID]
	if state.Status != "active" || state.ProgressValue != 1 {
		t.Fatalf("accepted quest state=%+v want active/1", state)
	}
	return repositories, accountID, characterID
}

func seekerQST012FindStartOwner(
	t *testing.T,
	archive *platformpvf.Archive,
	runtime *runtimeDungeonState,
	test seekerQST012QuestCase,
) (uint32, string) {
	t.Helper()
	if test.startOwnerKind == 0x211 {
		key, index := seekerQST012FindFriendlyMonsterWithIndex(t, archive, runtime, test.startOwnerCode, true)
		scene, _ := runtime.Session.Scene()
		if index >= len(scene.Map.Map.MonsterFollowType) || scene.Map.Map.MonsterFollowType[index] != "follow" {
			t.Fatalf("start map friendly monster index=%d follow values=%v", index, scene.Map.Map.MonsterFollowType)
		}
		return key, "MAP [monster team]=0 and parsed [monster follow type]=follow"
	}
	for _, actor := range runtime.Room.Snapshot().ExtendedActors {
		if actor.AICharacter != nil && actor.AICharacter.Code == test.startOwnerCode &&
			actor.HostileReference == nil && actor.State == runtimeDungeonMonsterAnnounced {
			if actor.AICharacterMetadata == nil || actor.AICharacterMetadata.Name == "" {
				t.Fatalf("AI character code=%d lacks real metadata", test.startOwnerCode)
			}
			return actor.ObjectKey, fmt.Sprintf("MAP [ai character] code=%d faction=%s AIC=%s name=%s",
				actor.AICharacter.Code, actor.AICharacter.Faction, actor.AICharacterMetadata.Path, actor.AICharacterMetadata.Name)
		}
	}
	t.Fatalf("start map has no announced non-hostile AI character code=%d", test.startOwnerCode)
	return 0, ""
}

func seekerQST012FindFriendlyAIByCode(
	t *testing.T,
	runtime *runtimeDungeonState,
	code int64,
) uint32 {
	t.Helper()
	for _, actor := range runtime.Room.Snapshot().ExtendedActors {
		if actor.AICharacter != nil && actor.AICharacter.Code == code &&
			actor.HostileReference == nil && actor.State == runtimeDungeonMonsterAnnounced {
			return actor.ObjectKey
		}
	}
	t.Fatalf("start map has no announced non-hostile AI character code=%d", code)
	return 0
}

func seekerQST012FindFriendlyMonster(
	t *testing.T,
	archive *platformpvf.Archive,
	runtime *runtimeDungeonState,
	code int64,
	requireFollow bool,
) uint32 {
	t.Helper()
	key, _ := seekerQST012FindFriendlyMonsterWithIndex(t, archive, runtime, code, requireFollow)
	return key
}

func seekerQST012FindFriendlyMonsterWithIndex(
	t *testing.T,
	archive *platformpvf.Archive,
	runtime *runtimeDungeonState,
	code int64,
	requireFollow bool,
) (uint32, int) {
	t.Helper()
	room := runtime.Room.Snapshot()
	scene, _ := runtime.Session.Scene()
	answer := uint32(0)
	answerIndex := -1
	for index, monster := range room.Monsters {
		if monster.Spawn.MonsterID != code {
			continue
		}
		if index >= len(scene.Map.Map.MonsterTeam) || scene.Map.Map.MonsterTeam[index] != 0 {
			t.Fatalf("friendly monster code=%d index=%d is not [monster team]=0: teams=%v", code, index, scene.Map.Map.MonsterTeam)
		}
		if requireFollow {
			if index >= len(scene.Map.Map.MonsterFollowType) || scene.Map.Map.MonsterFollowType[index] != "follow" {
				t.Fatalf("friendly monster code=%d index=%d is not follow: values=%v", code, index, scene.Map.Map.MonsterFollowType)
			}
		}
		if monster.State != runtimeDungeonMonsterAnnounced {
			t.Fatalf("friendly monster code=%d state=%s", code, monster.State)
		}
		if answer != 0 {
			t.Fatalf("duplicate friendly monster code=%d", code)
		}
		answer, answerIndex = monster.ObjectKey, index
	}
	if answer == 0 {
		t.Fatalf("map %d has no announced friendly monster code=%d", scene.Map.Map.ID, code)
	}
	return answer, answerIndex
}

func seekerQST012AssertTargetFriendlyFacts(
	t *testing.T,
	archive *platformpvf.Archive,
	runtime *runtimeDungeonState,
	test seekerQST012QuestCase,
) {
	t.Helper()
	scene, _ := runtime.Session.Scene()
	if test.questID == 3188 || test.questID == 3197 {
		room := runtime.Room.Snapshot()
		if len(room.ExtendedActors) != 0 {
			for _, actor := range room.ExtendedActors {
				if actor.HostileReference == nil && actor.AICharacter != nil {
					t.Fatalf("target map %d unexpectedly gained a fabricated friendly AI %+v", scene.Map.Map.ID, actor.AICharacter)
				}
			}
		}
		for index, monster := range room.Monsters {
			if index < len(scene.Map.Map.MonsterTeam) && scene.Map.Map.MonsterTeam[index] == 0 {
				t.Fatalf("target map %d unexpectedly declares friendly monster index=%d id=%d", scene.Map.Map.ID, index, monster.Spawn.MonsterID)
			}
		}
		return
	}
	if test.questID == 3190 {
		key, index := seekerQST012FindFriendlyMonsterWithIndex(t, archive, runtime, test.targetOwnerCode, true)
		if index >= len(scene.Map.Map.MonsterFollowType) || scene.Map.Map.MonsterFollowType[index] != "follow" {
			t.Fatalf("target map follow attribute code=%d index=%d values=%v", test.targetOwnerCode, index, scene.Map.Map.MonsterFollowType)
		}
		var ai *runtimeDungeonExtendedActor
		for i := range runtime.Room.Snapshot().ExtendedActors {
			actor := runtime.Room.Snapshot().ExtendedActors[i]
			if actor.AICharacter != nil && actor.AICharacter.Code == 2014 && actor.HostileReference == nil {
				actor := actor
				ai = &actor
				break
			}
		}
		if ai == nil || ai.AICharacterMetadata == nil || ai.AICharacterMetadata.Name != "G.S.D" {
			t.Fatalf("target map has no announced real G.S.D AI actor")
		}
		t.Logf("target friendly monster=%d code=%d team=0 follow=true; AI=%d G.S.D", key, test.targetOwnerCode, ai.ObjectKey)
	}
}

func seekerQST012ExerciseSameRoomAndControls(
	t *testing.T,
	service *Service,
	session *gameSession,
	conn *bufferConn,
	repositories dnfrepo.Group,
	characterID string,
	test seekerQST012QuestCase,
	friendlyOwner uint32,
) {
	t.Helper()
	first, ok := seekerQST012LiveHostile(session.dungeon.runtime)
	if !ok {
		t.Fatal("start room has no hostile")
	}
	packets, accepted := seekerQST012SendDeath(t, service, session, conn, first.ObjectKey, friendlyOwner, test.startOwnerKind)
	if !accepted {
		t.Fatalf("same-room friendly owner=%d did not kill start-room target=%d", friendlyOwner, first.ObjectKey)
	}
	if !seekerQST012HasOp38(packets, first.ObjectKey) {
		t.Fatalf("same-room friendly death produced no op38 for object=%d", first.ObjectKey)
	}
	characterBefore, characterFound, err := repositories.Character.Load(context.Background(), characterID)
	if err != nil || !characterFound {
		t.Fatalf("load character after first friendly death found=%t err=%v", characterFound, err)
	}
	inventoryBefore, inventoryFound, err := repositories.Inventory.Load(context.Background(), characterID)
	if err != nil || !inventoryFound {
		t.Fatalf("load inventory after first friendly death found=%t err=%v", inventoryFound, err)
	}
	questBefore, questFound, err := repositories.Quest.Load(context.Background(), characterID)
	if err != nil || !questFound {
		t.Fatalf("load quest after first friendly death found=%t err=%v", questFound, err)
	}
	assertFriendlyDeathReplayUnchanged := func(stage string) {
		t.Helper()
		characterAfter, found, err := repositories.Character.Load(context.Background(), characterID)
		if err != nil || !found || !reflect.DeepEqual(characterBefore, characterAfter) {
			t.Fatalf("%s changed character found=%t err=%v before=%+v after=%+v", stage, found, err, characterBefore, characterAfter)
		}
		inventoryAfter, found, err := repositories.Inventory.Load(context.Background(), characterID)
		if err != nil || !found || !reflect.DeepEqual(inventoryBefore, inventoryAfter) {
			t.Fatalf("%s changed inventory found=%t err=%v before=%+v after=%+v", stage, found, err, inventoryBefore, inventoryAfter)
		}
		questAfter, found, err := repositories.Quest.Load(context.Background(), characterID)
		if err != nil || !found || !reflect.DeepEqual(questBefore, questAfter) {
			t.Fatalf("%s changed quest found=%t err=%v before=%+v after=%+v", stage, found, err, questBefore, questAfter)
		}
	}

	replayPackets, _ := seekerQST012SendDeath(t, service, session, conn, first.ObjectKey, friendlyOwner, test.startOwnerKind)
	if !seekerQST012HasOp38(replayPackets, first.ObjectKey) {
		t.Errorf("duplicate same-room friendly death did not replay op38 for object=%d", first.ObjectKey)
	}
	assertFriendlyDeathReplayUnchanged("duplicate same-room friendly death")

	retryTarget, ok := seekerQST012LiveHostile(session.dungeon.runtime)
	if !ok {
		t.Fatal("start room has no second hostile for notification retry")
	}
	retryBody := seekerQST012DeathBody(t, retryTarget.ObjectKey, friendlyOwner, test.startOwnerKind)
	session.conn = &failNthDungeonWriteConn{failAt: 1, err: errors.New("qst012 injected op38 failure")}
	err = service.handleDungeonMonsterDeath(session, retryBody)
	if err == nil {
		t.Error("injected op38 failure returned nil")
	}
	if _, defeated := session.dungeon.runtime.Room.DefeatedDeathTarget(retryTarget.ObjectKey); !defeated {
		t.Fatalf("notification failure did not commit target=%d", retryTarget.ObjectKey)
	}
	// The failed notification is the first authoritative death of this second
	// target, so its assets/task transition belong before the retry boundary.
	// Refresh the durable baseline here; retrying the op38 must not settle it again.
	characterBefore, characterFound, err = repositories.Character.Load(context.Background(), characterID)
	if err != nil || !characterFound {
		t.Fatalf("load character after failed op38 found=%t err=%v", characterFound, err)
	}
	inventoryBefore, inventoryFound, err = repositories.Inventory.Load(context.Background(), characterID)
	if err != nil || !inventoryFound {
		t.Fatalf("load inventory after failed op38 found=%t err=%v", inventoryFound, err)
	}
	questBefore, questFound, err = repositories.Quest.Load(context.Background(), characterID)
	if err != nil || !questFound {
		t.Fatalf("load quest after failed op38 found=%t err=%v", questFound, err)
	}
	session.conn = conn
	retryPackets, _ := seekerQST012SendDeath(t, service, session, conn, retryTarget.ObjectKey, friendlyOwner, test.startOwnerKind)
	if !seekerQST012HasOp38(retryPackets, retryTarget.ObjectKey) {
		t.Errorf("retry after op38 failure did not replay notification for friendly owner=%d target=%d", friendlyOwner, retryTarget.ObjectKey)
	}
	assertFriendlyDeathReplayUnchanged("retry after op38 failure")

	ordinaryTarget, ok := seekerQST012LiveHostile(session.dungeon.runtime)
	if !ok {
		t.Fatal("start room has no hostile for ordinary-owner control")
	}
	playerOwner := uint32(currentSceneActorObjectKey(session.selectedCharacterID))
	ordinaryPackets, ordinaryAccepted := seekerQST012SendDeath(t, service, session, conn, ordinaryTarget.ObjectKey, playerOwner, 0x111)
	if !ordinaryAccepted || !seekerQST012HasOp38(ordinaryPackets, ordinaryTarget.ObjectKey) {
		t.Errorf("ordinary player-owner control was rejected or missed op38 for target=%d", ordinaryTarget.ObjectKey)
	} else {
		t.Logf("ordinary player-owner control accepted target=%d", ordinaryTarget.ObjectKey)
	}

	illegalTarget, ok := seekerQST012LiveHostile(session.dungeon.runtime)
	if !ok {
		t.Fatal("start room has no hostile for illegal-owner control")
	}
	illegalPackets, illegalAccepted := seekerQST012SendDeath(t, service, session, conn, illegalTarget.ObjectKey, 64000, 0x111)
	if illegalAccepted || seekerQST012HasOp38(illegalPackets, illegalTarget.ObjectKey) {
		t.Error("illegal owner 64000 changed authoritative state or emitted op38")
	} else {
		t.Logf("illegal owner control rejected target=%d", illegalTarget.ObjectKey)
	}

	record, found, err := repositories.Quest.Load(context.Background(), characterID)
	if err != nil || !found {
		t.Fatalf("load pre-target quest found=%t err=%v", found, err)
	}
	state := record.States[test.questID]
	if state.Status != "active" || state.ProgressValue != 1 {
		t.Errorf("non-final friendly/control deaths changed quest %d to %s/%d", test.questID, state.Status, state.ProgressValue)
	}
}

func seekerQST012ReachTarget(
	t *testing.T,
	service *Service,
	session *gameSession,
	runtime *runtimeDungeonState,
	resolver *worldmap.Resolver,
	test seekerQST012QuestCase,
) {
	t.Helper()
	topology, err := worldmap.BuildDungeonLayout(
		resolver,
		runtime.Dungeon.ID,
		runtime.MazeIndex,
		func(choice worldmap.DungeonMapChoice) (int64, error) { return choice.Candidates[0].ID, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	start, _ := runtime.Session.Scene()
	targetCoordinate := worldmap.RoomCoordinate{}
	found := false
	for _, room := range topology.Rooms() {
		if room.Map != nil && room.Map.Map.ID == test.targetMapID {
			targetCoordinate, found = room.Coordinate, true
			break
		}
	}
	if !found {
		t.Fatalf("topology has no map %d", test.targetMapID)
	}
	path := realDungeonPathToRoom(t, topology, start.Coordinate, targetCoordinate)
	for _, coordinate := range path[1:] {
		clearCurrentRealPVFSmokeRoom(t, runtime)
		body := make([]byte, dungeoncmd.MoveMapRequestSize)
		body[0], body[1] = byte(coordinate.X), byte(coordinate.Y)
		if err := service.handleDungeonMoveMap(session, body); err != nil {
			t.Fatalf("move from smoke-cleared room to %s: %v", coordinate, err)
		}
		scene, _ := runtime.Session.Scene()
		if scene.Coordinate != coordinate {
			t.Fatalf("move landed at %s want=%s", scene.Coordinate, coordinate)
		}
		t.Logf("smoke-located passage %s -> %s map=%d", path[0], coordinate, scene.Map.Map.ID)
	}
}

func seekerQST012EnsureAnnounced(t *testing.T, runtime *runtimeDungeonState, label string) {
	t.Helper()
	room := runtime.Room.Snapshot()
	needsAnnouncement := false
	for _, monster := range room.Monsters {
		needsAnnouncement = needsAnnouncement || monster.State == runtimeDungeonMonsterPlanned
	}
	for _, actor := range room.ExtendedActors {
		needsAnnouncement = needsAnnouncement || actor.State == runtimeDungeonMonsterPlanned
	}
	if !needsAnnouncement {
		return
	}
	if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		t.Fatalf("announce %s map %d: %v", label, room.MapID, err)
	}
}

func seekerQST012LiveHostile(runtime *runtimeDungeonState) (runtimeDungeonMonster, bool) {
	if runtime == nil || runtime.Room == nil || runtime.Session == nil {
		return runtimeDungeonMonster{}, false
	}
	scene, ok := runtime.Session.Scene()
	if !ok {
		return runtimeDungeonMonster{}, false
	}
	expected := make(map[worldmap.HostileReference]struct{}, len(scene.ExpectedHostiles))
	for _, reference := range scene.ExpectedHostiles {
		expected[reference] = struct{}{}
	}
	for _, monster := range runtime.Room.Snapshot().Monsters {
		if monster.Reference.Kind != worldmap.HostileMonster || monster.State != runtimeDungeonMonsterAnnounced {
			continue
		}
		if _, hostile := expected[monster.Reference]; !hostile {
			continue
		}
		if dungeonSceneObjectDefeated(scene.DefeatedObjects, monster.ObjectKey) {
			continue
		}
		return monster, true
	}
	return runtimeDungeonMonster{}, false
}

func seekerQST012SendDeath(
	t *testing.T,
	service *Service,
	session *gameSession,
	conn *bufferConn,
	target uint32,
	owner uint32,
	ownerKind uint16,
) ([]dnfproto.ChannelPacket, bool) {
	t.Helper()
	body := seekerQST012DeathBody(t, target, owner, ownerKind)
	conn.write.Reset()
	if err := service.handleDungeonMonsterDeath(session, body); err != nil {
		t.Fatalf("handle op39 target=%d owner=%d: %v", target, owner, err)
	}
	packets := splitAllFinishBridgePackets(t, conn.write.Bytes())
	_, accepted := session.dungeon.runtime.Room.DefeatedDeathTarget(target)
	return packets, accepted
}

func seekerQST012DeathBody(t *testing.T, target, owner uint32, ownerKind uint16) []byte {
	t.Helper()
	body := friendlyAPCDeathBody(t, target, owner, false)
	binary.LittleEndian.PutUint16(body[4:], uint16(owner))
	binary.LittleEndian.PutUint16(body[31:], ownerKind)
	binary.LittleEndian.PutUint16(body[33:], uint16(owner))
	return body
}

func seekerQST012HasOp38(packets []dnfproto.ChannelPacket, target uint32) bool {
	for _, packet := range packets {
		if packet.Header.MsgID == 38 && len(packet.Body) >= 2 &&
			binary.LittleEndian.Uint16(packet.Body[:2]) == uint16(target) {
			return true
		}
	}
	return false
}
