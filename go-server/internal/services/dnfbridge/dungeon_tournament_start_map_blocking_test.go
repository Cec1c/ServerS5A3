package dnfbridge

import (
	"encoding/binary"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// ── P8：锦标赛副本进不去的硬阻塞（普通 START_MAP 的 rank 闸门）───────────────
//
// 现场（线上原文，2026-09-27 18:10，dungeon 120 / char 3）：
//
//	EVENT kind=game-dungeon-select-blocked … reason=dungeon_entry_packet_preflight_failed
//	  failure_class=entry_preflight_unavailable failure_reason_byte=1 reply_sent=true
//	  error=plan current start-map body: monster index 0 code 56465:
//	        dnf start-map monster rank is unsupported: rank=""
//
// 本文件的用例钉三件事：
//  1. 成因就是"锦标赛投影出来的 MonsterSpawn 没有 rank"（`tournamentRoomMonsters`
//     只写 Code/Level/Position/Rank，`[tournament enemies]` 语法里根本没有 rank 列）；
//  2. 同一个空 rank 在**非**锦标赛语境下**仍然**照旧报错 —— rank 闸门一个字节没改；
//  3. vendor 档下这张图按厂商口径不生成普通 START_MAP，而 op39 的 owner 校验
//     （objectKey / firstSequence 空间）逐值不变。

// tournamentStartMapBlockingFixture 造一份"锦标赛地图上已经投影了 4 只对手、
// 但没有任何普通 `[monster]` 段"的运行时。
//
// 关键：投影走**生产路径同一个函数**（`tournamentRoomMonsters` +
// `ProjectTournamentRoomActors`），所以夹具里 `Rank == ""` 不是手写的假数据，
// 而是线上那 4 只怪的真实成因。
func tournamentStartMapBlockingFixture(t *testing.T) (*runtimeDungeonState, worldmap.DungeonRoomScene) {
	t.Helper()
	table, resolver, catalog := loadBridgeDungeonStaticData(t, bridgeDungeonPVF(false))
	dungeon, found := table.FindDungeon(700)
	if !found {
		t.Fatal("fixture dungeon 700 is not indexed by the synthetic PVF")
	}
	topology, err := worldmap.BuildDungeonLayout(resolver, 700, 0, func(choice worldmap.DungeonMapChoice) (int64, error) {
		return choice.Candidates[0].ID, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if topology.Start == nil {
		t.Fatal("fixture dungeon has no start room")
	}
	if len(topology.Bosses) == 0 {
		t.Fatal("fixture dungeon has no boss room")
	}
	startRoom, ok := topology.Room(*topology.Start)
	if !ok || startRoom.Map == nil {
		t.Fatalf("fixture start room %v unresolved", *topology.Start)
	}
	definition := tournamentFlowFixtureDefinition(startRoom.Map.Map.ID)
	tournament, reason, ok := newTournamentDungeonRuntime(&definition, 1, deterministicTournamentNext(31))
	if !ok {
		t.Fatalf("fixture tournament runtime rejected: %s", reason)
	}
	roomActors, err := tournamentRoomActors(definition, tournament)
	if err != nil {
		t.Fatalf("project tournament room actors: %v", err)
	}
	if err := topology.ProjectTournamentRoomActors(*topology.Start, roomActors); err != nil {
		t.Fatal(err)
	}
	run, err := worldmap.NewDungeonRun(topology)
	if err != nil {
		t.Fatal(err)
	}
	dungeonSession, err := worldmap.NewDungeonSession(run)
	if err != nil {
		t.Fatal(err)
	}
	scene, ok := dungeonSession.Scene()
	if !ok {
		t.Fatal("fixture session has no scene")
	}
	room, _, err := newRuntimeDungeonRoom(scene, catalog, firstDungeonMonsterObjectKey)
	if err != nil {
		t.Fatal(err)
	}
	// `Dungeon.Metadata.BasisLevel` 是 `currentDungeonMonsterLevel` 的必需输入
	// （真档 120 是 `[basis level] 72`），Boss 坐标是 op28 的前置；
	// 两者都按 `prepareDungeonRuntime` 的口径给上，否则失败原因会被更早的校验遮住、
	// 盖掉我们要钉的 rank 语义。副本本身直接用合成 PVF 真解析出来的 700。
	runtimeState := &runtimeDungeonState{
		Dungeon:        dungeon,
		Room:           room,
		Session:        dungeonSession,
		Seed:           0x5EED1234,
		Request:        dungeoncmd.SelectDungeonRequest{Difficulty: 0},
		BossSet:        true,
		BossCoordinate: topology.Bosses[0],
		Tournament:     tournament,
	}
	if !tournament.tryBindFirstActorSequence(uint16(room.monsters[0].ObjectKey)) {
		t.Fatal("bind first actor sequence failed")
	}
	return runtimeState, scene
}

// 成因取证：`[tournament enemies]` 的 `[monster]` 候选（厂商
// `TournamentDungeonDefinition.cs:414 actorType = (byte)0`）投出来的 `MonsterSpawn`
// **没有 rank**，而普通 START_MAP 的 actor 行要求 rank ∈ {normal, champion, …}。
func TestTournamentProjectedMonstersCarryNoRankAndBlockTheOrdinaryStartMap(t *testing.T) {
	runtimeState, scene := tournamentStartMapBlockingFixture(t)
	roomSnapshot := runtimeState.Room.Snapshot()
	if len(roomSnapshot.Monsters) != tournamentRoundCount {
		t.Fatalf("projected monsters=%d want=%d", len(roomSnapshot.Monsters), tournamentRoundCount)
	}
	for index, monster := range roomSnapshot.Monsters {
		// 第 1..3 轮是 `[monster]` 候选（actorType 0）⇒ 没有 rank；
		// 第 4 轮被厂商升格成 actorType 3（`TournamentDungeonRuntime.cs:505-506`），
		// 本仓用 `Rank = "[boss]"` 表达 ⇒ 这一只有 rank，也正是它能过闸门的原因。
		wantRank := ""
		if index == len(roomSnapshot.Monsters)-1 {
			wantRank = "[boss]"
		}
		if monster.Spawn.Rank != wantRank {
			t.Fatalf("projected monster %d rank=%q want=%q (票根：这 4 只怪只声明 Code/Level/Position)",
				index, monster.Spawn.Rank, wantRank)
		}
		if monster.RoomClearSkipByte != 0 {
			t.Fatalf("projected monster %d blocking byte=%d want 0 (厂商 IsBlocking=true)",
				index, monster.RoomClearSkipByte)
		}
	}
	// 非锦标赛语境（默认档）下同一批怪**必须**照旧被 rank 闸门挡下：
	// 这条断言同时证明"我们只是绕开了锦标赛地图，没有放宽 rank 校验"。
	if _, err := buildCurrentDungeonEntryPackets(runtimeState, scene); !errors.Is(err, errDungeonStartMapMonsterRank) {
		t.Fatalf("ordinary start-map build error=%v want %v (rank 闸门不得被本次修复放宽)",
			err, errDungeonStartMapMonsterRank)
	}
}

// vendor 档：这张图不生成普通 START_MAP(op29)，但 op28(dungeon-info) 照旧构建。
// 默认档（未设置 / off / 未知值）逐字节保持今天的失败语义。
func TestTournamentMapSuppressesOrdinaryStartMapOnlyInVendorMode(t *testing.T) {
	runtimeState, scene := tournamentStartMapBlockingFixture(t)

	// 默认档四条取值：全部照旧失败（错误身份与改动前逐字相同）。
	for _, value := range []string{"", "off", "OFF", "sausages"} {
		t.Setenv(dungeonTournamentEnvKey, value)
		if _, err := buildCurrentDungeonEntryPackets(runtimeState, scene); !errors.Is(err, errDungeonStartMapMonsterRank) {
			t.Fatalf("switch=%q error=%v want %v", value, err, errDungeonStartMapMonsterRank)
		}
		if _, suppressed := currentTournamentMapSuppressesOrdinaryStartMap(runtimeState, scene); suppressed {
			t.Fatalf("switch=%q must not suppress the ordinary start-map", value)
		}
	}

	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	reason, suppressed := currentTournamentMapSuppressesOrdinaryStartMap(runtimeState, scene)
	if !suppressed || reason != currentTournamentStartMapSuppressionReason {
		t.Fatalf("suppression=(%q,%t) want (%q,true)", reason, suppressed, currentTournamentStartMapSuppressionReason)
	}
	packets, err := buildCurrentDungeonEntryPackets(runtimeState, scene)
	if err != nil {
		t.Fatalf("vendor-mode entry packets: %v", err)
	}
	if !packets.StartMapSuppressed || packets.StartMapSuppressedReason != currentTournamentStartMapSuppressionReason {
		t.Fatalf("packets suppression=%t reason=%q", packets.StartMapSuppressed, packets.StartMapSuppressedReason)
	}
	if len(packets.StartMap) != 0 {
		t.Fatalf("suppressed start-map body=%x want empty", packets.StartMap)
	}
	// op28 必须照旧可发：厂商在锦标赛地图上只跳过 START_MAP，不跳过 DUNGEON_INFO
	// （`DungeonMapHandler.cs:943` 的分支只包住 START_MAP 那一包）。
	if len(packets.DungeonInfo) != 36 {
		t.Fatalf("dungeon-info body len=%d want 36", len(packets.DungeonInfo))
	}
	if binary.LittleEndian.Uint32(packets.DungeonInfo[0:4]) != 700 {
		t.Fatalf("dungeon-info head=%x", packets.DungeonInfo[:4])
	}
}

// 判据与厂商 `sendTournamentProjection = tournament != null && sentMapId == Definition.MapId`
// 逐字对齐：别的地图**不**抑制（那张图走普通 start-map，与厂商同）。
func TestTournamentStartMapSuppressionFollowsTheDefinitionMapId(t *testing.T) {
	runtimeState, scene := tournamentStartMapBlockingFixture(t)
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)

	foreign := scene
	foreign.Map = scene.Map
	foreign.Map.Map = scene.Map.Map
	foreign.Map.Map.ID = scene.Map.Map.ID + 1
	if _, suppressed := currentTournamentMapSuppressesOrdinaryStartMap(runtimeState, foreign); suppressed {
		t.Fatalf("map %d must not be treated as the tournament map %d",
			foreign.Map.Map.ID, runtimeState.Tournament.Definition.MapID)
	}
}

// op39 的 owner 校验在"op29 被抑制"之后必须逐值不变：
//   - 房间 actor 的 objectKey 空间还是 `firstDungeonMonsterObjectKey + i`；
//   - 状态机的 firstSequence 仍然绑在 `room.monsters[0].ObjectKey`；
//   - 386 的路径 actor 段写出的 u16 sequence 与之一一对应；
//   - 真喂一条 op39 进去，权威死亡提交被接受、赛制推进到第 2 轮。
func TestSuppressedTournamentStartMapKeepsOp39OwnerKeySpace(t *testing.T) {
	runtimeState, scene := tournamentStartMapBlockingFixture(t)
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)

	packets, err := buildCurrentDungeonEntryPackets(runtimeState, scene)
	if err != nil || !packets.StartMapSuppressed {
		t.Fatalf("entry packets err=%v suppressed=%t", err, packets.StartMapSuppressed)
	}
	// 进图尾段里紧跟 op29 的那一步（无论 op29 是否发出都会执行）。
	announced, err := runtimeState.Room.AnnounceAllActors(runtimeState.Session)
	if err != nil {
		t.Fatalf("announce actors without op29: %v", err)
	}
	if announced != tournamentRoundCount {
		t.Fatalf("announced actors=%d want=%d", announced, tournamentRoundCount)
	}
	roomSnapshot := runtimeState.Room.Snapshot()
	firstSequence := runtimeState.Tournament.boundFirstSequence()
	if firstSequence != uint16(roomSnapshot.Monsters[0].ObjectKey) {
		t.Fatalf("firstSequence=%d want room monsters[0]=%d", firstSequence, roomSnapshot.Monsters[0].ObjectKey)
	}
	// 386 的路径 actor 段：每轮 1 个 `u16 seq + i32 code + u8 level + u8 type`，
	// 轮头 1 字节 ⇒ 步长 9（既有用例 TestTournamentStartMapProjectionSendsInfoThenMapInfo
	// 钉的第一条在 body[225:227]）。
	infoBody, err := buildTournamentInfoBody(runtimeState.Tournament, 0, firstSequence)
	if err != nil {
		t.Fatalf("build tournament info body: %v", err)
	}
	for index, monster := range roomSnapshot.Monsters {
		offset := 225 + index*9
		if got := binary.LittleEndian.Uint16(infoBody[offset : offset+2]); got != uint16(monster.ObjectKey) {
			t.Fatalf("386 path actor %d sequence=%d want object key=%d (公告者换成 386，id 空间不得改)",
				index, got, monster.ObjectKey)
		}
		if got := binary.LittleEndian.Uint32(infoBody[offset+2 : offset+6]); got != uint32(monster.Spawn.MonsterID) {
			t.Fatalf("386 path actor %d code=%d want=%d", index, got, monster.Spawn.MonsterID)
		}
	}

	// 真喂 op39：走的是客户端上报 `RuntimeObjectKey` 的那条权威路径。
	connection := &bufferConn{}
	session := &gameSession{
		conn:                connection,
		connID:              "tournament-suppressed-start-map",
		selectedCharacterID: 99,
	}
	session.dungeon.runtime = runtimeState
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	if err := service.handleDungeonMonsterDeath(session, tutorialScopeVariableZeroCombatDeathBody(uint32(firstSequence))); err != nil {
		t.Fatalf("op39 after a suppressed start-map: %v", err)
	}
	after := runtimeState.Room.Snapshot()
	if after.Monsters[0].State != runtimeDungeonMonsterDefeated {
		t.Fatalf("op39 for object_key=%d was not accepted (state=%s) —— owner 校验断在公告空间上",
			firstSequence, after.Monsters[0].State)
	}
	if round := runtimeState.Tournament.currentRoundNumber(); round != 2 {
		t.Fatalf("tournament round after the first kill=%d want 2", round)
	}
}
