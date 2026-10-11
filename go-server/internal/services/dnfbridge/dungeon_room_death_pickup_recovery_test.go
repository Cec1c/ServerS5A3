package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfmonster "longheng.io/server/internal/modules/dnf/monster"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 操作者现场「副本 艾肯（单人）- 遗落的神殿 第一个房间固定有一只怪物死亡后变白但
// 不消失导致无法继续前进」的真档事实钉在这里。三条互锁的事实全部来自运行期
// Script.pvf（dungeon 5097 `dungeon/Legend/Ekern/Warriors/Warriors.dgn`）：
//
//	① maze 0 的 start 是 (4,0)、boss 是 (0,0)；boss 房（map 74195
//	   `74195_B_(0,0)_Warriors.map`）里**只有一只** `[monster]`，rank `[boss]`；
//	② maze 0 与 maze 1 的通关条件都是 `[destroy object] 53764 1`；
//	③ 53764 落在 u16 之内，并且是地图里的 `[passive object]`
//	   （ActionObject/Map/Legend/Ekern/System/BossChecker/BossChecker.obj），
//	   所以"用 objectKey > 0xFFFF 判定 PVF 被动对象"的旧边界在本副本必然失效。
func TestRealScriptPVFEkern5097BossRoomAndDestroyObjectClearCondition(t *testing.T) {
	pvfPath := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNF_WORLDMAP_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	dungeon, ok := table.FindDungeon(5097)
	if !ok {
		t.Fatal("real PVF dungeon 5097 (艾肯单人-遗落的神殿) is missing")
	}
	topology, err := worldmap.BuildDungeonLayout(
		resolver,
		dungeon.ID,
		0,
		func(choice worldmap.DungeonMapChoice) (int64, error) { return choice.Candidates[0].ID, nil },
	)
	if err != nil {
		t.Fatalf("build real dungeon 5097 maze 0 layout: %v", err)
	}
	if topology.Start == nil || *topology.Start != (worldmap.RoomCoordinate{X: 4, Y: 0}) {
		t.Fatalf("real dungeon 5097 maze 0 start=%v want 4:0", topology.Start)
	}
	if len(topology.Bosses) != 1 || topology.Bosses[0] != (worldmap.RoomCoordinate{X: 0, Y: 0}) {
		t.Fatalf("real dungeon 5097 maze 0 bosses=%v want [0:0]", topology.Bosses)
	}
	bossRoom, found := topology.Room(worldmap.RoomCoordinate{X: 0, Y: 0})
	if !found || bossRoom.Map == nil {
		t.Fatalf("real dungeon 5097 maze 0 boss room is unresolved: %+v", bossRoom)
	}
	if bossRoom.Map.Map.ID != 74195 || len(bossRoom.Map.Map.Monsters) != 1 {
		t.Fatalf(
			"real dungeon 5097 maze 0 boss room map=%d monsters=%d want map 74195 with exactly one monster",
			bossRoom.Map.Map.ID,
			len(bossRoom.Map.Map.Monsters),
		)
	}
	if got := currentDungeonMonsterActorTypeForTest(t, bossRoom.Map.Map.Monsters[0]); got != 3 {
		t.Fatalf("real dungeon 5097 boss room monster actor type=%d want 3 ([boss])", got)
	}
	clearTargetID := int64(0)
	for _, maze := range dungeon.Mazes {
		for _, condition := range maze.ClearConditions {
			if normalizeDungeonPVFSymbol(condition.Type) != "destroy object" {
				continue
			}
			if condition.TargetID != 53764 || condition.Count != 1 {
				t.Fatalf("real dungeon 5097 destroy-object condition=%+v want target 53764 count 1", condition)
			}
			clearTargetID = condition.TargetID
		}
	}
	if clearTargetID == 0 {
		t.Fatal("real dungeon 5097 declares no destroy-object clear condition")
	}
	if clearTargetID > int64(^uint16(0)) {
		t.Fatalf("real dungeon 5097 clear target %d unexpectedly exceeds u16", clearTargetID)
	}
	placements := 0
	for _, object := range bossRoom.Map.Map.PassiveObjects {
		if object.ObjectID == clearTargetID {
			placements++
		}
	}
	if placements != 1 {
		t.Fatalf(
			"real dungeon 5097 boss room map %d places clear target %d %d times, want exactly one",
			bossRoom.Map.Map.ID,
			clearTargetID,
			placements,
		)
	}
}

func currentDungeonMonsterActorTypeForTest(t *testing.T, spawn worldmap.MonsterSpawn) byte {
	t.Helper()
	actorType, err := currentDungeonMonsterActorType(spawn)
	if err != nil {
		t.Fatal(err)
	}
	return actorType
}

// 操作者现场「无法继续前进」的另一半：clear condition 目标落在 u16 之内的被动对象
// （艾肯-遗落的神殿 = `destroy object 53764`，BossChecker）以前被
// `objectKey > 0xFFFF` 的旧边界挡在门外，于是 op39 被拒、副本收不了尾。
// 本用例用与真档同形的 u16 目标把整条链钉死。
func TestDestroyObjectClearConditionAcceptsU16PVFPassiveObjectTarget(t *testing.T) {
	const u16ClearTargetID = uint32(53764)
	source := bridgePVFSource{
		worldmap.DefaultMapList: "70195 `legend/boss.map`\n",
		"map/legend/boss.map": "[map name]\n`Ekern Boss`\n" +
			"[dungeon]\n7157\n[type]\n`[boss]`\n" +
			"[passive object]\n53764 476 234 0\n",
		worldmap.DefaultDungeonList: "7157 `legend.dgn`\n",
		"dungeon/legend.dgn": "[name]\n`Ekern Legend`\n" +
			"[minimum required level]\n10\n[basis level]\n20\n[limit party count]\n1\n" +
			"[maze info]\n[size]\n1 1\n[greed]\n`A`\n" +
			"[map specification]\n`boss` 0 0 70195\n[start map]\n0 0\n[boss map]\n0 0\n" +
			"[clear condition]\n[destroy object]\n53764 1\n[/clear condition]\n",
		worldmap.DefaultWorldMapList: "1 `legend.wdm`\n",
		"worldmap/legend.wdm":        "[name]\n`Legend`\n[dungeon]\n7157 -1\n[/dungeon]\n",
		dnfmonster.DefaultList:       "3001 `unused.gob`\n",
		"monster/unused.gob":         "[name]\n`Unused`\n[level]\n20\n[hp]\n1\n[exp]\n0\n",
	}
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "2",
		AccountID:   "account-1",
		Name:        "TEST",
		Job:         "0",
		Level:       20,
		Stats:       map[string]int64{"fatigue": 100},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "2",
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:             options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		worldMapTable:       table,
		worldMapResolver:    resolver,
		dungeonMonsterTable: monsters,
		dungeonChoice:       func(int) (int, error) { return 0, nil },
		dungeonSeed:         func() (uint32, error) { return 1, nil },
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
	}
	runtime, _, err := service.prepareDungeonRuntime(
		context.Background(),
		&gameSession{selectedCharacterID: 2},
		dungeoncmd.SelectDungeonRequest{DungeonID: 7157, Difficulty: 0},
	)
	if err != nil {
		t.Fatal(err)
	}
	scene, ok := runtime.Session.Scene()
	if !ok || !scene.Boss || !scene.Cleared || len(scene.PassiveObjects) != 1 {
		t.Fatalf("fixture scene=%+v found=%t", scene, ok)
	}
	if source := currentDungeonDestroyObjectClearConditionSource(runtime, scene, u16ClearTargetID); source == "" {
		t.Fatal("u16 PVF passive-object clear target was not recognized")
	}
	connection := &bufferConn{}
	session := &gameSession{
		conn:                connection,
		connID:              "u16-destroy-object-clear",
		selectedCharacterID: 2,
		dungeon:             dungeonSessionState{runtime: runtime},
	}
	if err := service.handleDungeonMonsterDeath(
		session,
		tutorialScopeVariableZeroCombatDeathBody(u16ClearTargetID),
	); err != nil {
		t.Fatal(err)
	}
	snapshot := runtime.Session.Snapshot()
	if snapshot.Run.Status != worldmap.DungeonRunCompleted ||
		!runtime.ordinaryFinalRoomClearAccepted || !runtime.settlementEntrySent {
		t.Fatalf("u16 destroy-object clear did not enter settlement: runtime=%+v snapshot=%+v", runtime, snapshot)
	}
	for _, packet := range splitAllFinishBridgePackets(t, connection.write.Bytes()) {
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketNotifyDieMonster) {
			t.Fatalf("u16 passive object emitted an op38 body=%x", packet.Body)
		}
	}
}

// 操作者现场：「一只怪死亡后变白但不消失」。两条**客户端形状**闸门（owner=0xffff +
// 固定 62 字节形状 / owner 不是本地角色 object key）默认会整条拒收死亡上报、一个字节都
// 不回，这就是客户端留下白色残影、房间永远清不掉的机制。默认档（strict）必须保持这个
// 既有边界（既有回归用例钉死了它），`room_proven` 档才追加"目标在册即放行"。
func TestHandleDungeonMonsterDeathRoomProofSwitchMatrix(t *testing.T) {
	for _, test := range []struct {
		name      string
		mode      dungeonDeathRoomProofMode
		owner     uint16
		wantAck   bool
		wantClear bool
	}{
		{
			name:    "default strict keeps owner sentinel fixed layout rejected",
			mode:    dungeonDeathRoomProofStrict,
			owner:   ^uint16(0),
			wantAck: false,
		},
		{
			name:    "default strict keeps foreign owner rejected",
			mode:    dungeonDeathRoomProofStrict,
			owner:   0x0621,
			wantAck: false,
		},
		{
			name:      "room proven accepts owner sentinel fixed layout",
			mode:      dungeonDeathRoomProofRoomProven,
			owner:     ^uint16(0),
			wantAck:   true,
			wantClear: true,
		},
		{
			name:      "room proven accepts foreign owner key",
			mode:      dungeonDeathRoomProofRoomProven,
			owner:     0x0621,
			wantAck:   true,
			wantClear: true,
		},
		{
			name:      "room proven keeps the default player owned route working",
			mode:      dungeonDeathRoomProofRoomProven,
			owner:     0,
			wantAck:   true,
			wantClear: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, runtime := prepareGenericRoomHostileDeathRuntime(t)
			service.options.dungeonDeathRoomProof = test.mode
			monster := runtime.Room.Snapshot().Monsters[0]
			if len(runtime.Room.Snapshot().Monsters) != 1 {
				t.Fatalf("fixture monsters=%+v", runtime.Room.Snapshot().Monsters)
			}
			owner := test.owner
			if owner == 0 {
				owner = currentSceneActorObjectKey(99)
			}
			scene, ok := runtime.Session.Scene()
			if !ok {
				t.Fatal("missing dungeon scene")
			}
			wantProven := test.mode == dungeonDeathRoomProofRoomProven
			if got := service.dungeonDeathRoomProofActive() &&
				currentDungeonRoomAnnouncedLiveHostile(runtime, scene, monster.ObjectKey); got && !wantProven {
				t.Fatalf("switch %s reported an active room proof", test.mode)
			}
			if wantProven && !currentDungeonRoomAnnouncedLiveHostile(runtime, scene, monster.ObjectKey) {
				t.Fatalf("fixture target %d is not a proven announced live room hostile", monster.ObjectKey)
			}
			connection := &bufferConn{}
			session := &gameSession{
				conn:                connection,
				connID:              "dungeon-death-room-proof-matrix",
				selectedCharacterID: 99,
				dungeon:             dungeonSessionState{runtime: runtime},
			}
			body := make([]byte, dungeoncmd.DieMonsterRequestSize)
			binary.LittleEndian.PutUint32(body[0:4], monster.ObjectKey)
			binary.LittleEndian.PutUint16(body[4:6], owner)
			if err := service.handleDungeonMonsterDeath(session, body); err != nil {
				t.Fatal(err)
			}
			if !test.wantAck {
				if connection.write.Len() != 0 {
					t.Fatalf("strict mode emitted %x for an unproven owner/shape", connection.write.Bytes())
				}
				if got := runtime.Room.Snapshot().Monsters[0].State; got != runtimeDungeonMonsterAnnounced {
					t.Fatalf("strict mode advanced monster state to %s", got)
				}
				if runtime.Session.Snapshot().Scene.Cleared {
					t.Fatal("strict mode cleared the room for an unproven death report")
				}
				return
			}
			packet, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
			if packet.Header.MsgID != uint16(dnfenum.CmdPacketNotifyDieMonster) ||
				packet.Header.Classification != 0 ||
				binary.LittleEndian.Uint16(packet.Body[0:2]) != uint16(monster.ObjectKey) {
				t.Fatalf(
					"proven room hostile death was not acknowledged header=%+v body=%x",
					packet.Header,
					packet.Body,
				)
			}
			// 死亡必须离开房间的"活怪集合"，并且房间因此可清。
			if _, _, err := runtime.Room.CommitActorDeathReport(monster.ObjectKey, runtime.Session); err == nil {
				t.Fatal("accepted death did not consume the room's live monster entry")
			}
			sceneSnapshot := runtime.Session.Snapshot().Scene
			if len(sceneSnapshot.DefeatedObjects) != 1 || sceneSnapshot.DefeatedObjects[0] != monster.ObjectKey {
				t.Fatalf("room live-monster set not updated defeated=%v", sceneSnapshot.DefeatedObjects)
			}
			if test.wantClear && !sceneSnapshot.Cleared {
				t.Fatal("single-hostile room did not become clearable after the accepted death")
			}
		})
	}
}

func TestReadDungeonDeathRoomProofModeDefaultsToStrict(t *testing.T) {
	for value, want := range map[string]dungeonDeathRoomProofMode{
		"":                          dungeonDeathRoomProofStrict,
		"strict":                    dungeonDeathRoomProofStrict,
		"legacy":                    dungeonDeathRoomProofStrict,
		"room-proven":               dungeonDeathRoomProofStrict,
		"room_proven":               dungeonDeathRoomProofRoomProven,
		" ROOM_PROVEN ":             dungeonDeathRoomProofRoomProven,
		"typo_never_opens_the_door": dungeonDeathRoomProofStrict,
	} {
		if got := readDungeonDeathRoomProofMode(value); got != want {
			t.Fatalf("DNFBRIDGE_DUNGEON_DEATH_ROOM_PROOF=%q mode=%s want=%s", value, got, want)
		}
	}
	var zero Service
	if zero.dungeonDeathRoomProof() != dungeonDeathRoomProofStrict || zero.dungeonDeathRoomProofActive() {
		t.Fatal("zero-value service did not keep the strict dungeon death room proof")
	}
}

// 未在册的目标仍然必须被拒（不能因为放宽 owner 判据就把伪造死亡也放进来）。
func TestHandleDungeonMonsterDeathStillRejectsUnannouncedTargetWithForeignOwner(t *testing.T) {
	service, runtime := prepareGenericRoomHostileDeathRuntime(t)
	monster := runtime.Room.Snapshot().Monsters[0]
	connection := &bufferConn{}
	session := &gameSession{
		conn:                connection,
		connID:              "dungeon-death-owner-reject-test",
		selectedCharacterID: 99,
		dungeon:             dungeonSessionState{runtime: runtime},
	}
	body := make([]byte, dungeoncmd.DieMonsterRequestSize)
	binary.LittleEndian.PutUint32(body[0:4], monster.ObjectKey+500)
	binary.LittleEndian.PutUint16(body[4:6], 0x0621)
	if err := service.handleDungeonMonsterDeath(session, body); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("unannounced foreign death emitted %d bytes", connection.write.Len())
	}
	if monster.ObjectKey == 0 || runtime.Session.Snapshot().Scene.Cleared {
		t.Fatalf("rejected death mutated room state scene=%+v", runtime.Session.Snapshot().Scene)
	}
}

// 房间级拾取账本回归（操作者「每个房间只能拾取一个物品」）：
//
//	① 同一房间内先注册**一批**两件掉落（生产路径就是一批 registerBatch），
//	   两件都必须能各自被拾取并各自入账；
//	② 过图之后，新房间的掉落必须能被拾取，而上一房间遗留的掉落必须被 RoomMismatch
//	   拒绝（不能跨房间拾取，也不能因为换房间把账本截断/覆盖）。
func TestCurrentDungeonPickupLedgerSurvivesRoomChangeAndMultipleSameRoomPickups(t *testing.T) {
	service, runtime, repositories := prepareGenericRoomPickupRuntime(t)
	connection := &bufferConn{}
	session := &gameSession{
		conn:                connection,
		connID:              "dungeon-room-pickup-lifecycle-test",
		selectedCharacterID: 99,
		dungeon:             dungeonSessionState{runtime: runtime},
	}
	startScene, ok := runtime.Session.Scene()
	if !ok {
		t.Fatal("missing start scene")
	}
	startKey := runtimeDungeonRoomKeyFromScene(startScene)
	firstRoomKeys := registerCurrentDungeonPickupBatch(t, service, runtime, 2)
	if len(firstRoomKeys) != 2 || firstRoomKeys[0] == firstRoomKeys[1] {
		t.Fatalf("start-room batch keys=%v", firstRoomKeys)
	}
	for index, key := range firstRoomKeys {
		before := len(runtime.DropOwner.byObjectKey)
		connection.write.Reset()
		if err := service.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(key)); err != nil {
			t.Fatal(err)
		}
		pickup, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
		itemUpdate, trailing := splitGameServerUpperPacket(t, rest)
		if pickup.Header.MsgID != currentDungeonPickupResultOpcode ||
			binary.LittleEndian.Uint32(pickup.Body[0:4]) != key ||
			itemUpdate.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) ||
			len(trailing) != 0 {
			t.Fatalf(
				"same-room pickup %d header=%+v body=%x update=%+v trailing=%x",
				index,
				pickup.Header,
				pickup.Body,
				itemUpdate.Header,
				trailing,
			)
		}
		drop := runtime.DropOwner.byObjectKey[key]
		if drop == nil || drop.Status != runtimeDungeonDropConsumed {
			t.Fatalf("same-room pickup %d did not consume its own drop: %+v", index, drop)
		}
		if len(runtime.DropOwner.byObjectKey) != before {
			t.Fatalf("pickup %d changed the drop ledger size %d -> %d", index, before, len(runtime.DropOwner.byObjectKey))
		}
	}
	inventory, found, err := repositories.Inventory.Load(context.Background(), "99")
	if err != nil || !found || len(inventory.Slots) != 1 {
		t.Fatalf("same-room pickups did not persist two units into one stack slots=%+v err=%v", inventory.Slots, err)
	}
	if stack := inventory.Slots["0:3"]; stack.Count != 2 {
		t.Fatalf("same-room pickups persisted count=%d want 2 slots=%+v", stack.Count, inventory.Slots)
	}

	// 过图：start 房 (0,0) 无怪 ⇒ 已清 ⇒ op45 必须被接受。
	moveBody := make([]byte, dungeoncmd.MoveMapRequestSize)
	moveBody[0] = 1
	connection.write.Reset()
	if err := service.handleDungeonMoveMap(session, moveBody); err != nil {
		t.Fatalf("room transition: %v", err)
	}
	moved, ok := runtime.Session.Scene()
	if !ok || moved.Coordinate != (worldmap.RoomCoordinate{X: 1, Y: 0}) {
		t.Fatalf("room transition scene=%+v ok=%t", moved, ok)
	}
	movedKey := runtimeDungeonRoomKeyFromScene(moved)
	if movedKey == startKey {
		t.Fatalf("room key did not change across the transition: %+v", movedKey)
	}
	if len(runtime.DropOwner.byObjectKey) != 2 {
		t.Fatalf("room transition dropped ledger entries len=%d", len(runtime.DropOwner.byObjectKey))
	}
	for _, key := range firstRoomKeys {
		if _, err := runtime.DropOwner.owned(key, movedKey, currentSceneActorObjectKey(99)); err == nil {
			t.Fatalf("previous-room drop %d became pickable in the new room", key)
		}
	}
	secondRoomKeys := registerCurrentDungeonPickupBatch(t, service, runtime, 2)
	connection.write.Reset()
	if err := service.handleCurrentDungeonPickup(
		session,
		currentDungeonPickupTestBody(secondRoomKeys[0]),
	); err != nil {
		t.Fatal(err)
	}
	pickup, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if pickup.Header.MsgID != currentDungeonPickupResultOpcode ||
		binary.LittleEndian.Uint32(pickup.Body[0:4]) != secondRoomKeys[0] {
		t.Fatalf("new-room pickup header=%+v body=%x rest=%x", pickup.Header, pickup.Body, rest)
	}
	inventory, _, err = repositories.Inventory.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	if stack := inventory.Slots["0:3"]; stack.Count != 3 {
		t.Fatalf("new-room pickup persisted count=%d want 3 slots=%+v", stack.Count, inventory.Slots)
	}
	if len(runtime.DropOwner.byObjectKey) != 4 {
		t.Fatalf("new-room registration truncated the ledger len=%d want 4", len(runtime.DropOwner.byObjectKey))
	}
}

func registerCurrentDungeonPickupBatch(
	t *testing.T,
	service *Service,
	runtime *runtimeDungeonState,
	count int,
) []uint32 {
	t.Helper()
	catalog, err := service.dungeonMonsterCatalog()
	if err != nil {
		t.Fatal(err)
	}
	drops, err := catalog.DropCatalog()
	if err != nil {
		t.Fatal(err)
	}
	definition, err := drops.ResolveItem(3227)
	if err != nil {
		t.Fatal(err)
	}
	scene, ok := runtime.Session.Scene()
	if !ok {
		t.Fatal("missing dungeon scene")
	}
	owner := runtime.DropOwner
	if owner == nil {
		owner = newRuntimeDungeonDropOwner()
	}
	roomKey := runtimeDungeonRoomKeyFromScene(scene)
	objectKey := runtime.NextObjectKey
	sceneSlot := owner.nextSceneSlot
	batch := make([]*runtimeDungeonDrop, 0, count)
	keys := make([]uint32, 0, count)
	for index := 0; index < count; index++ {
		drop := &runtimeDungeonDrop{
			ObjectKey:           objectKey,
			SceneSlot:           sceneSlot,
			Item:                definition,
			Amount:              1,
			OwnerActorObjectKey: currentSceneActorObjectKey(99),
			Room:                roomKey,
			Status:              runtimeDungeonDropAvailable,
		}
		batch = append(batch, drop)
		keys = append(keys, objectKey)
		objectKey++
		sceneSlot++
	}
	if err := owner.registerBatch(batch, sceneSlot); err != nil {
		t.Fatal(err)
	}
	runtime.DropOwner = owner
	runtime.NextObjectKey = objectKey
	return keys
}

func prepareGenericRoomHostileDeathRuntime(t *testing.T) (*Service, *runtimeDungeonState) {
	t.Helper()
	service, runtime := prepareSyntheticMoveRuntimeFromPVF(t, bridgeGenericDungeonMovePVF(true))
	if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		t.Fatal(err)
	}
	if err := runtime.cacheCurrentDungeonRoom(); err != nil {
		t.Fatal(err)
	}
	return service, runtime
}

func prepareGenericRoomPickupRuntime(t *testing.T) (*Service, *runtimeDungeonState, dnfrepo.Group) {
	t.Helper()
	source := bridgeGenericDungeonMovePVF(false)
	source["monster/test.gob"] += "[item]\n3227 10\n[/item]\n"
	source[dungeonDropStackableList] = "3227 `material/test_drop.stk`\n"
	source[dungeonDropEquipmentList] = "9001 `weapon/test_drop.equ`\n"
	source["stackable/material/test_drop.stk"] = "[name]\n`Test Drop`\n" +
		"[stackable type]\n`[material]`\n[stack limit]\n999\n[attach type]\n`[free]`\n"
	source["equipment/weapon/test_drop.equ"] = "[name]\n`Test Equipment`\n" +
		"[durability]\n50\n[attach type]\n`[trade]`\n"
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "99",
		AccountID:   "account-1",
		Level:       20,
		Stats:       map[string]int64{"fatigue": 100},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "99",
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:             options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		worldMapTable:       table,
		worldMapResolver:    resolver,
		dungeonMonsterTable: monsters,
		dungeonChoice:       func(int) (int, error) { return 0, nil },
		dungeonSeed:         func() (uint32, error) { return 0x12345678, nil },
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
	}
	runtime, _, err := service.prepareDungeonRuntime(
		context.Background(),
		&gameSession{selectedCharacterID: 99},
		dungeoncmd.SelectDungeonRequest{DungeonID: 700, Difficulty: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		t.Fatal(err)
	}
	if err := runtime.cacheCurrentDungeonRoom(); err != nil {
		t.Fatal(err)
	}
	return service, runtime, repositories
}

// 真档默认 route 必须仍然按当前 EXE 的 op39 形状放行（不能被本次放宽反向影响）。
func TestHandleDungeonMonsterDeathKeepsDefaultPlayerOwnedRoute(t *testing.T) {
	service, runtime := prepareGenericRoomHostileDeathRuntime(t)
	monster := runtime.Room.Snapshot().Monsters[0]
	connection := &bufferConn{}
	session := &gameSession{
		conn:                connection,
		connID:              "dungeon-death-default-route-test",
		selectedCharacterID: 99,
		dungeon:             dungeonSessionState{runtime: runtime},
	}
	body := make([]byte, dungeoncmd.DieMonsterRequestSize)
	binary.LittleEndian.PutUint32(body[0:4], monster.ObjectKey)
	binary.LittleEndian.PutUint16(body[4:6], currentSceneActorObjectKey(99))
	if err := service.handleDungeonMonsterDeath(session, body); err != nil {
		t.Fatal(err)
	}
	packet, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	if packet.Header.MsgID != uint16(dnfenum.CmdPacketNotifyDieMonster) ||
		!bytes.Equal(packet.Body[0:2], []byte{byte(monster.ObjectKey), byte(monster.ObjectKey >> 8)}) {
		t.Fatalf("default player-owned death header=%+v body=%x", packet.Header, packet.Body)
	}
}
