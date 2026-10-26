package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfmonster "longheng.io/server/internal/modules/dnf/monster"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 74字节结构夹具；不透明运行块/footer已合成，仅保留布局和前缀差异。
const missionInformerCapturedRetirement = "a2010000ffff00000000000000000000000000000000000000000000000000a8000701708192a3b4c5d6e7f8091a2b3c4d5e6f8091a2b3c4d5e6f708192a3b4c5d6e7f90a1b2c3d4e5f6"

func missionInformerCapturedBody(t *testing.T, key uint32) []byte {
	t.Helper()
	body, err := hex.DecodeString(missionInformerCapturedRetirement)
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(body, key)
	return body
}

type missionInformerFixture struct {
	svc     *Service
	session *gameSession
	conn    *bufferConn
	repos   dnfrepo.Group
	source  dnfpvf.Source
	key     uint32
}

func prepareMissionInformerFixture(t *testing.T, clearRoom bool) missionInformerFixture {
	t.Helper()
	return prepareMissionInformerFixtureAtKey(t, clearRoom, 409)
}

func prepareMissionInformerFixtureAtKey(t *testing.T, clearRoom bool, nextKey uint32) missionInformerFixture {
	t.Helper()
	source := bridgeDungeonPVF(false)
	source[worldmap.DefaultDungeonList] = "50 `Act6/AstrayThief.dgn`\n"
	source[worldmap.DefaultMapList] = "400075 `AstrayThief/M3797_s6201.map`\n400076 `AstrayThief/M3797_e6201.map`\n400080 `AstrayThief/M3797_n6202.map`\n"
	source["worldmap/test.wdm"] = "[name]\n`测试区域`\n[dungeon]\n50 -1\n[/dungeon]\n"
	source["dungeon/Act6/AstrayThief.dgn"] = "[name]\n`测试任务迷宫`\n[minimum required level]\n47\n[basis level]\n49\n[difficulty level]\n1 1 1 0 0\n[maze info]\n[quest connection]\n0 3797 1\n[size]\n4 1\n[greed]\n`AABBFFEE`\n[map specification]\n`map` 3 0 400075 `map` 2 0 400076 `boss` 1 0 400080\n[/map specification]\n[start map]\n3 0\n[/start map]\n[boss map]\n1 0\n[/boss map]\n"
	source["map/AstrayThief/M3797_s6201.map"] = "[dungeon]\n50\n[type]\n`[normal]`\n[monster]\n3001 10 0 100 200 0 0 0 `[fixed]` `[normal]`\n"
	source["map/AstrayThief/M3797_n6202.map"] = source["map/AstrayThief/M3797_s6201.map"]
	source["map/AstrayThief/M3797_e6201.map"] = "[dungeon]\n50\n[type]\n`[normal]`\n[monster]\n" + strings.Repeat("3001 10 0 100 200 0 0 0 `[fixed]` `[normal]`\n", 9) + "[/monster]\n[ai character]\n11117 777 253 0 `[monster]` `[normal]` 0 0\n[/ai character]\n[passive object]\n853 552 152 0\n[/passive object]\n"
	source[dnfmonster.DefaultList] = "3001 `test.gob`\n"
	source[defaultDungeonAICharacterList] = "11117 `MissionQuest/Gunner/CodenameInfo/CodenameInfo_direction.aic`\n"
	source["AICharacter/MissionQuest/Gunner/CodenameInfo/CodenameInfo_direction.aic"] = "[minimum info]\n`测试情报员` 2 3 0 1 50\n[ai pattern]\n`CodenameInfo_AI.h`\n[/ai pattern]\n"
	source[dnfquest.DefaultList] = "3797 `mission/mission_archaeology_04_01.qst`\n"
	source["n_quest/mission/mission_archaeology_04_01.qst"] = "[name]\n`掌握信息的人`\n[grade]\n`[mission]`\n[level]\n47 99\n[type]\n`[clear map]`\n[int data]\n400080 25110\n[/int data]\n"
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	ai, err := newPVFDungeonAICharacterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	quests, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	f := prepareMissionInformerService(t, source, table, resolver, monsters, ai, quests)
	f.session.dungeon.runtime.NextObjectKey = nextKey
	moveMissionInformerToSecondRoom(t, f)
	if clearRoom {
		clearMissionInformerOrdinaryMonsters(t, f)
	}
	f.conn.write.Reset()
	return f
}

func prepareMissionInformerService(t *testing.T, source dnfpvf.Source, table *worldmap.Table, resolver *worldmap.Resolver, monsters *pvfDungeonMonsterCatalog, ai *pvfDungeonAICharacterCatalog, quests *dnfquest.Catalog) missionInformerFixture {
	t.Helper()
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "account-1", Job: "0", Level: 90, Stats: map[string]int64{"fatigue": 156, "exp": 1000}}
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{"1": {ItemID: 100, Count: 2}}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "1", States: map[int64]dnfrepo.QuestState{3797: {Status: "active", ProgressValue: 1}}}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{options: options{accountID: "account-1", currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain}, worldMapTable: table, worldMapResolver: resolver, dungeonMonsterTable: monsters, dungeonAICharacterTable: ai, questCatalog: quests, dungeonChoice: func(int) (int, error) { return 0, nil }, dungeonSeed: func() (uint32, error) { return 1, nil }, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "任务情报员退场回归", selectedCharacterID: 1}
	runtime, _, err := svc.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: 50, Difficulty: 2})
	if err != nil {
		t.Fatal(err)
	}
	session.dungeon.runtime = runtime
	session.dungeon.runToken = 1
	runtime.lifecycleToken = 1
	if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		t.Fatal(err)
	}
	return missionInformerFixture{svc: svc, session: session, conn: conn, repos: repos, source: source}
}

func moveMissionInformerToSecondRoom(t *testing.T, f missionInformerFixture) {
	t.Helper()
	runtime := f.session.dungeon.runtime
	for _, monster := range runtime.Room.Snapshot().Monsters {
		if _, _, err := runtime.Room.CommitActorDeathReport(monster.ObjectKey, runtime.Session); err != nil {
			t.Fatal(err)
		}
	}
	body := make([]byte, dungeoncmd.MoveMapRequestSize)
	body[0], body[1] = 2, 0
	if err := f.svc.handleDungeonMoveMap(f.session, body); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil && !errors.Is(err, errDungeonActorAlreadyAnnounced) {
		t.Fatal(err)
	}
	scene, _ := runtime.Session.Scene()
	if scene.Map.Map.ID != 400076 || len(runtime.Room.Snapshot().Monsters) != 9 {
		t.Fatalf("第二房不符：%+v", scene)
	}
}

func clearMissionInformerOrdinaryMonsters(t *testing.T, f missionInformerFixture) {
	t.Helper()
	runtime := f.session.dungeon.runtime
	for _, monster := range runtime.Room.Snapshot().Monsters {
		if monster.State == runtimeDungeonMonsterDefeated {
			continue
		}
		if _, _, err := runtime.Room.CommitActorDeathReport(monster.ObjectKey, runtime.Session); err != nil {
			t.Fatal(err)
		}
	}
}

func missionInformerActor(t *testing.T, f missionInformerFixture) runtimeDungeonExtendedActor {
	t.Helper()
	for _, actor := range f.session.dungeon.runtime.Room.Snapshot().ExtendedActors {
		if actor.AICharacter != nil && actor.AICharacter.Code == 11117 {
			return actor
		}
	}
	t.Fatal("没有在册情报员11117")
	return runtimeDungeonExtendedActor{}
}

func assertMissionInformerOnlyDeath(t *testing.T, stream []byte, key uint32) []byte {
	t.Helper()
	if len(stream) == 0 {
		t.Fatal("情报员离场没有收到op38")
	}
	packet, rest := splitGameServerUpperPacket(t, stream)
	if packet.Header.MsgID != 38 || packet.Header.Classification != 0 || len(packet.Body) != 7 || len(rest) != 0 || binary.LittleEndian.Uint16(packet.Body) != uint16(key) || packet.Body[2] != 0 {
		t.Fatalf("预期单个零掉落op38，实际header=%+v body=%x rest=%x", packet.Header, packet.Body, rest)
	}
	return append([]byte(nil), packet.Body...)
}

func missionInformerAssets(t *testing.T, f missionInformerFixture) []any {
	t.Helper()
	character, _, err := f.repos.Character.Load(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	inventory, _, err := f.repos.Inventory.Load(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	quest, _, err := f.repos.Quest.Load(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	return []any{character, inventory, quest}
}

func TestMissionInformerCapturedRetirementZeroAssetsAndReplay(t *testing.T) {
	f := prepareMissionInformerFixture(t, true)
	actor := missionInformerActor(t, f)
	if actor.ObjectKey != 418 {
		t.Fatalf("对象键=%d，预期现场418", actor.ObjectKey)
	}
	before := missionInformerAssets(t, f)
	body := missionInformerCapturedBody(t, actor.ObjectKey)
	if err := f.svc.handleDungeonMonsterDeath(f.session, body); err != nil {
		t.Fatal(err)
	}
	first := assertMissionInformerOnlyDeath(t, f.conn.write.Bytes(), actor.ObjectKey)
	if _, ok := f.session.dungeon.runtime.Room.DefeatedDeathTarget(actor.ObjectKey); !ok {
		t.Fatal("情报员生命周期未提交")
	}
	f.conn.write.Reset()
	// 同一形状的动态footer与坐标不参与生命周期解释。
	body[len(body)-1] ^= 0x5a
	body[39] ^= 0x31
	if err := f.svc.handleDungeonMonsterDeath(f.session, body); err != nil {
		t.Fatal(err)
	}
	if next := assertMissionInformerOnlyDeath(t, f.conn.write.Bytes(), actor.ObjectKey); !bytes.Equal(first, next) {
		t.Fatal("重放改变了冻结的op38")
	}
	if !reflect.DeepEqual(before, missionInformerAssets(t, f)) {
		t.Fatal("退场或重放改动了角色、库存或任务")
	}
}

func TestMissionInformerRetirementWriteFailureKeepsFrozenNotification(t *testing.T) {
	f := prepareMissionInformerFixture(t, true)
	key := missionInformerActor(t, f).ObjectKey
	before := missionInformerAssets(t, f)
	wantErr := errors.New("注入情报员op38写失败")
	f.session.conn = &failNthDungeonWriteConn{failAt: 1, err: wantErr}
	if err := f.svc.handleDungeonMonsterDeath(f.session, missionInformerCapturedBody(t, key)); !errors.Is(err, wantErr) {
		t.Fatalf("写失败=%v", err)
	}
	want, found := f.session.dungeon.runtime.Room.DeathResponseBody(key)
	if !found {
		t.Fatal("写失败未保留冻结通知")
	}
	// 在提交之后任务账变化，不应使已提交的同房离场丧失补发能力。
	q, _, _ := f.repos.Quest.Load(context.Background(), "1")
	q.States[3797] = dnfrepo.QuestState{Status: "completed"}
	if err := f.repos.Quest.Save(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	f.session.conn = f.conn
	if err := f.svc.handleDungeonMonsterDeath(f.session, missionInformerCapturedBody(t, key)); err != nil {
		t.Fatal(err)
	}
	if got := assertMissionInformerOnlyDeath(t, f.conn.write.Bytes(), key); !bytes.Equal(got, want) {
		t.Fatal("通知补发不等于提交时冻结的op38")
	}
	after := missionInformerAssets(t, f)
	if !reflect.DeepEqual(before[:2], after[:2]) {
		t.Fatal("写失败重试改动了战斗资产")
	}
}

func TestMissionInformerRetirementRejectsUnprovedBindingsAndShapes(t *testing.T) {
	for _, name := range []string{"unannounced actor", "wrong character", "wrong run", "wrong account", "wrong room", "no active quest", "wrong metadata", "wrong packet code", "wrong definition path", "wrong AI pattern", "wrong quest connection", "oversized tail", "combat prefix", "contribution", "truncated", "unknown object 853"} {
		t.Run(name, func(t *testing.T) {
			f := prepareMissionInformerFixture(t, true)
			runtime := f.session.dungeon.runtime
			key := missionInformerActor(t, f).ObjectKey
			body := missionInformerCapturedBody(t, key)
			switch name {
			case "unannounced actor":
				runtime.Room.extendedActors[0].State = runtimeDungeonMonsterPlanned
			case "wrong character":
				f.session.selectedCharacterID = 2
			case "wrong run":
				runtime.lifecycleToken++
			case "wrong account":
				f.svc.options.accountID = "other-account"
			case "wrong room":
				runtime.Room.mapID++
			case "no active quest":
				if err := f.repos.Quest.Save(context.Background(), dnfrepo.QuestRecord{CharacterID: "1", States: map[int64]dnfrepo.QuestState{}}); err != nil {
					t.Fatal(err)
				}
			case "wrong metadata":
				runtime.Room.extendedActors[0].AICharacterMetadata.Name = "另一份定义"
			case "wrong packet code":
				runtime.Room.extendedActors[0].Packet.Code++
			case "wrong definition path", "wrong AI pattern":
				ai := f.svc.dungeonAICharacterTable.cache[11117]
				if name == "wrong definition path" {
					ai.Path = "AICharacter/MissionQuest/Gunner/CodenameInfo/CodenameInfo.aic"
				} else {
					for index := range ai.Sections {
						if ai.Sections[index].Name == "ai pattern" {
							ai.Sections[index].Tokens[0].Value = "Other_AI.h"
						}
					}
				}
				f.svc.dungeonAICharacterTable.cache[11117] = ai
				runtime.Room.extendedActors[0].AICharacterMetadata = &ai
			case "wrong quest connection":
				runtime.Dungeon.Mazes[runtime.MazeIndex].QuestConnection = []int64{0, 3798, 1}
			case "oversized tail":
				body = append(body, 0)
			case "combat prefix":
				body[18] = 1
			case "contribution":
				body = friendlyAPCDeathBody(t, key, 1, false)
			case "truncated":
				body = body[:len(body)-1]
			case "unknown object 853":
				binary.LittleEndian.PutUint32(body, 853)
			}
			before := missionInformerAssets(t, f)
			if err := f.svc.handleDungeonMonsterDeath(f.session, body); err != nil {
				t.Fatal(err)
			}
			if _, defeated := runtime.Room.DefeatedDeathTarget(key); defeated || f.conn.write.Len() != 0 || !reflect.DeepEqual(before, missionInformerAssets(t, f)) {
				t.Fatal("未证明的离场改动了演员、资产或发出通知")
			}
		})
	}
}

func TestMissionInformerRetirementDoesNotReplayAfterLeavingRoom(t *testing.T) {
	f := prepareMissionInformerFixture(t, true)
	key := missionInformerActor(t, f).ObjectKey
	if err := f.svc.handleDungeonMonsterDeath(f.session, missionInformerCapturedBody(t, key)); err != nil {
		t.Fatal(err)
	}
	assertMissionInformerOnlyDeath(t, f.conn.write.Bytes(), key)
	body := make([]byte, dungeoncmd.MoveMapRequestSize)
	body[0] = 1
	if err := f.svc.handleDungeonMoveMap(f.session, body); err != nil {
		t.Fatal(err)
	}
	scene, _ := f.session.dungeon.runtime.Session.Scene()
	if scene.Map.Map.ID == 400076 {
		t.Fatal("未离开已清空第二房")
	}
	f.conn.write.Reset()
	if err := f.svc.handleDungeonMonsterDeath(f.session, missionInformerCapturedBody(t, key)); err != nil {
		t.Fatal(err)
	}
	if f.conn.write.Len() != 0 {
		t.Fatal("离房后旧对象仍被补发离场")
	}
}

func TestMissionInformerRetirementCancelsPendingReturnAfterNotification(t *testing.T) {
	for _, failFirstWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "首次成功", true: "写失败后补发"}[failFirstWrite], func(t *testing.T) {
			f := prepareMissionInformerFixture(t, true)
			runtime := f.session.dungeon.runtime
			key := missionInformerActor(t, f).ObjectKey
			runtime.townReturnPending = true
			runtime.townReturnOp24Sent = true
			runtime.townReturnRequestMsgID = 42
			runtime.townReturnSource = "测试尚未确认的回城"
			f.session.confirmedDungeonReturnStatePending = true
			if failFirstWrite {
				wantErr := errors.New("注入待决回城后通知写失败")
				f.session.conn = &failNthDungeonWriteConn{failAt: 1, err: wantErr}
				if err := f.svc.handleDungeonMonsterDeath(f.session, missionInformerCapturedBody(t, key)); !errors.Is(err, wantErr) {
					t.Fatalf("写失败=%v", err)
				}
				if !runtime.townReturnPending {
					t.Fatal("未写出离场通知就取消了原待决回城")
				}
				f.session.conn = f.conn
			}
			if err := f.svc.handleDungeonMonsterDeath(f.session, missionInformerCapturedBody(t, key)); err != nil {
				t.Fatal(err)
			}
			assertMissionInformerOnlyDeath(t, f.conn.write.Bytes(), key)
			if f.session.dungeon.runtime != runtime || runtime.townReturnPending || runtime.townReturnOp24Sent || f.session.confirmedDungeonReturnStatePending {
				t.Fatal("已确认图内退场后仍保留可退休副本的回城凭据")
			}
		})
	}
}
