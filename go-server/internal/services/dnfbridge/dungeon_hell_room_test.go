package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 房图覆盖端到端：把深渊房间那一间换成 `[seal door map index]` 那张图之后，
// **那间屋子的场景里必须真的出现柱子**（场景完全从房图派生）。
// 这条用例同时钉住两条失败路径：未知房间、未索引的图，都必须硬失败。
func TestApplyDungeonHellRoomMapBringsThePillarIntoTheRoomScene(t *testing.T) {
	source := bridgeDungeonPVF(false)
	source[worldmap.DefaultMapList] += "101 `dungeon/test/hell.map`\n"
	source["map/dungeon/test/hell.map"] = "[map name]\n`hell`\n" +
		"[type]\n`[hell]`\n" +
		"[special passive object]\n" +
		"6010 7 8 9 1 `[hellparty]` 4 100 0 5 100 1 `[/hellparty]`\n"
	table, resolver, _ := loadBridgeDungeonStaticData(t, source)
	topology, err := worldmap.BuildDungeonLayout(resolver, 700, 0,
		func(choice worldmap.DungeonMapChoice) (int64, error) {
			return choice.Candidates[0].ID, nil
		})
	if err != nil {
		t.Fatalf("build synthetic layout: %v", err)
	}
	start := worldmap.RoomCoordinate{X: 0, Y: 0}
	before, ok := topology.Room(start)
	if !ok || before.Map == nil || before.Map.Map.ID != 100 {
		t.Fatalf("start room before override=%+v ok=%t want map 100", before, ok)
	}
	if err := applyDungeonHellRoomMap(topology, table, dungeonHellRoom{MapID: 101, X: 0, Y: 0}); err != nil {
		t.Fatalf("apply hell room map: %v", err)
	}
	after, ok := topology.Room(start)
	if !ok || after.Map == nil || after.Map.Map.ID != 101 || after.Map.Source != worldmap.ResolutionHellSealDoor {
		t.Fatalf("start room after override=%+v ok=%t want map 101 source hell_seal_door", after, ok)
	}
	run, err := worldmap.NewDungeonRun(topology)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	session, err := worldmap.NewDungeonSession(run)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	scene, ok := session.Scene()
	if !ok {
		t.Fatal("session has no scene")
	}
	if scene.Map.Map.ID != 101 {
		t.Fatalf("session scene map=%d want 101 (this is what op29 sends)", scene.Map.Map.ID)
	}
	pillars := 0
	for _, object := range scene.SpecialPassiveObjects {
		if len(object.HellParty) != 0 {
			pillars++
		}
	}
	if pillars != 1 {
		t.Fatalf("scene special passive objects=%+v want exactly the pillar", scene.SpecialPassiveObjects)
	}

	// 失败路径：坐标不是这个迷宫的房间 ⇒ 硬失败（绝不凭空造屋子）。
	if err := applyDungeonHellRoomMap(topology, table, dungeonHellRoom{MapID: 101, X: 9, Y: 9}); err == nil {
		t.Fatal("override on a non-room coordinate must fail")
	}
	// 失败路径：那张图不在真档 `map/map.lst` 里 ⇒ 硬失败。
	if err := applyDungeonHellRoomMap(topology, table, dungeonHellRoom{MapID: 99999, X: 0, Y: 0}); err == nil {
		t.Fatal("override with an unindexed map id must fail")
	}
	// 失败路径：没有 worldmap 表 ⇒ 硬失败。
	if err := applyDungeonHellRoomMap(topology, nil, dungeonHellRoom{MapID: 101, X: 0, Y: 0}); err == nil {
		t.Fatal("override without a worldmap table must fail")
	}
}

// ── 真档 PVF 用例（`DNFBRIDGE_REAL_PVF_SMOKE` 守卫）─────────────────────────
//
// 这条用例的由来：`[hellparty]` 在**真实** PVF 里是行内反引号字符串 token，
// 不是 section（全库 18,374 张 map 里 `[hellparty]` section 出现 0 次）。
// 改动前 `Map.SpecialPassiveObjects[*].HellParty` 因此**永远是空的**，
// `hellPartyMapCarriesPillar` 恒 false，整条深渊房间修复在线上会静默变成空操作。
// 合成 PVF 用例发现不了这一点 —— 它用的是解析器"以为"的语法；所以必须有这条
// 真档用例，直接用 `[seal door map index]` 指到的那几张真图上判。
func TestRealPVFHellRoomResolvesForMetroCenterDungeons(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open real pvf: %v", err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatalf("load real worldmap: %v", err)
	}

	// 地轨中心（area 39）的五个可选副本的真实 `[seal door map index]` / `[seal door pos]`。
	want := map[int64]dungeonHellRoom{
		310: {MapID: 60083, X: 3, Y: 0}, // time_plaza
		311: {MapID: 60084, X: 2, Y: 1}, // canyon_of_beast
		312: {MapID: 60085, X: 1, Y: 2}, // hideout_of_fear
		313: {MapID: 60086, X: 3, Y: 2}, // gust_zone
		314: {MapID: 60087, X: 2, Y: 2}, // forest_of_redwitch
	}
	for dungeonID, wantRoom := range want {
		dungeon, ok := table.FindDungeon(dungeonID)
		if !ok {
			t.Fatalf("real dungeon %d not indexed", dungeonID)
		}
		found := 0
		for index := range dungeon.Mazes {
			room, ok := resolveDungeonHellRoom(&dungeon.Mazes[index], table)
			if !ok {
				continue
			}
			if room != wantRoom {
				t.Fatalf("dungeon %d maze %d hell room=%+v want=%+v", dungeonID, index, room, wantRoom)
			}
			found++
		}
		if found == 0 {
			t.Fatalf("dungeon %d resolved no hell room at all", dungeonID)
		}
	}

	// 柱子对象：真档 60083 必须**恰好**读出一个带 `[hellparty]` 行的 special passive
	// object，且就是 30528（`passiveobject.lst` → SealedGateOfHellDungeon4.obj）。
	hellMap, ok := table.FindMap(60083)
	if !ok {
		t.Fatal("real map 60083 not indexed")
	}
	pillars := 0
	for _, object := range hellMap.SpecialPassiveObjects {
		if len(object.HellParty) == 0 {
			continue
		}
		pillars++
		if object.ObjectID != 30528 {
			t.Fatalf("pillar object=%d want 30528", object.ObjectID)
		}
	}
	if pillars != 1 {
		t.Fatalf("map 60083 pillar count=%d want 1, objects=%+v diagnostics=%+v",
			pillars, hellMap.SpecialPassiveObjects, hellMap.Diagnostics)
	}
}

// ── 开关归一化 ────────────────────────────────────────────────────────────
//
// 本条的零值是**修复档**（与 hellBlockedSlots / dungeonEntrySelectionMode 相反），
// 所以只有显式 `legacy` 才回退，其余任何值都必须落在修复档。
func TestReadDungeonHellRoomModeDefaultsToFixAndOnlyLegacyRollsBack(t *testing.T) {
	tests := []struct {
		raw  string
		want dungeonHellRoomMode
	}{
		{raw: "", want: dungeonHellRoomVendor},
		{raw: "   ", want: dungeonHellRoomVendor},
		{raw: "vendor", want: dungeonHellRoomVendor},
		{raw: "on", want: dungeonHellRoomVendor},
		{raw: "true", want: dungeonHellRoomVendor},
		{raw: "1", want: dungeonHellRoomVendor},
		{raw: "junk", want: dungeonHellRoomVendor},
		{raw: "legacy", want: dungeonHellRoomLegacy},
		{raw: " LEGACY ", want: dungeonHellRoomLegacy},
		{raw: "Legacy", want: dungeonHellRoomLegacy},
	}
	for _, test := range tests {
		if got := readDungeonHellRoomMode(test.raw); got != test.want {
			t.Fatalf("readDungeonHellRoomMode(%q)=%v want=%v", test.raw, got, test.want)
		}
	}
	if !readDungeonHellRoomMode("").enabled() || readDungeonHellRoomMode("legacy").enabled() {
		t.Fatal("enabled() must be true only for the fix档")
	}
	if readDungeonHellRoomMode("").String() != "vendor" || readDungeonHellRoomMode("legacy").String() != "legacy" {
		t.Fatalf("String() mismatch: %v / %v", readDungeonHellRoomMode(""), readDungeonHellRoomMode("legacy"))
	}
}

// ── op29 offset 7 / fog 字节 ───────────────────────────────────────────────
//
// 逐字节对应厂商 DungeonMapHandler.cs:512-513：
//
//	var hellPartyMapMode = run.HellMode ? run.HellPartyMode : (byte)2;
//	var startMapFogFlag  = run.HellMode ? (byte)1 : (byte)0;
func TestDungeonStartMapHellStateMatchesVendorProjection(t *testing.T) {
	tests := []struct {
		name        string
		mode        dungeonHellRoomMode
		entryOption uint16
		wantMode    byte
		wantFog     byte
	}{
		// legacy：逐字节回到改动前（两个字节都恒为 0）。
		{name: "legacy ordinary", mode: dungeonHellRoomLegacy, entryOption: 0, wantMode: 0, wantFog: 0},
		{name: "legacy hell", mode: dungeonHellRoomLegacy, entryOption: 0x0001, wantMode: 0, wantFog: 0},
		{name: "legacy hell with difficulty", mode: dungeonHellRoomLegacy, entryOption: 0x0201, wantMode: 0, wantFog: 0},

		// 修复档：普通房间 = 标准值 2，fog 0（厂商 "Normal rooms use 2"）。
		{name: "fix ordinary", mode: dungeonHellRoomVendor, entryOption: 0, wantMode: 2, wantFog: 0},
		// 只带难度字节（低字节 0）不是深渊请求 —— 厂商 manualHellParty 只看 body[5]。
		{name: "fix difficulty-only is not a hell request", mode: dungeonHellRoomVendor, entryOption: 0x0100, wantMode: 2, wantFog: 0},
		// 深渊请求、玩家没点名难度 ⇒ 厂商权重不可用时的回退值 1。
		{name: "fix hell no difficulty", mode: dungeonHellRoomVendor, entryOption: 0x0001, wantMode: 1, wantFog: 1},
		// 深渊请求 + 点名难度 2 ⇒ 原样用请求里的难度。
		{name: "fix hell difficulty 2", mode: dungeonHellRoomVendor, entryOption: 0x0201, wantMode: 2, wantFog: 1},
		{name: "fix hell difficulty 1", mode: dungeonHellRoomVendor, entryOption: 0x0101, wantMode: 1, wantFog: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotMode, gotFog := dungeonStartMapHellState(test.mode, test.entryOption)
			if gotMode != test.wantMode || gotFog != test.wantFog {
				t.Fatalf("dungeonStartMapHellState(%v, %#04x)=(%d,%d) want=(%d,%d)",
					test.mode, test.entryOption, gotMode, gotFog, test.wantMode, test.wantFog)
			}
		})
	}
}

// TestLiveOp16BodiesProveTheHellFlagSitsAtBody7 钉住 2026-09-28 现场抓到的两条真实 op16 包体
// （见 `_dist/hell-pillar-diagnosis.md`），并证明修复档从 body[7] 读标志、回退档逐字节复现今天
// "永远不是深渊"的行为。
//
//	310  ⇒ 36 01 00 00 | 00 | 00 00 | 01 | 00 | ff ff | 00×10   （挑战深渊派对：body[7]=1）
//	5000 ⇒ 88 13 00 00 | 00 | 00 00 | 00 | 01 | ff ff | 00×10   （训练房：body[7]=0 / body[8]=1）
func TestLiveOp16BodiesProveTheHellFlagSitsAtBody7(t *testing.T) {
	hellBody := []byte{
		0x36, 0x01, 0x00, 0x00, // dungeon 310
		0x00,       // difficulty
		0x00, 0x00, // entry_option body[5:7] —— 真机 245/245 次恒为 0
		0x01,       // body[7] = 厂商 Flag1 = HellPartyRequestFlag
		0x00,       // body[8] = Flag2
		0xff, 0xff, // runtime token 哨兵
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	}
	practiceBody := []byte{
		0x88, 0x13, 0x00, 0x00, // dungeon 5000
		0x00,       // difficulty
		0x00, 0x00, // entry_option
		0x00, // body[7]
		0x01, // body[8] = Flag2（厂商 PracticeMode => Flag1==0 && Flag2!=0）
		0xff, 0xff,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	}
	for _, tc := range []struct {
		name    string
		body    []byte
		wantID  uint32
		hellBit bool
	}{
		{name: "hell_310", body: hellBody, wantID: 310, hellBit: true},
		{name: "practice_5000", body: practiceBody, wantID: 5000, hellBit: false},
	} {
		request, err := dungeoncmd.DecodeSelectDungeonRequest(tc.body)
		if err != nil {
			t.Fatalf("%s: decode: %v", tc.name, err)
		}
		if request.DungeonID != tc.wantID {
			t.Fatalf("%s: dungeon id = %d, want %d", tc.name, request.DungeonID, tc.wantID)
		}
		if request.EntryOption != 0 {
			t.Fatalf("%s: entry_option = %#04x, want 0 (the real client never fills body[5:7])", tc.name, request.EntryOption)
		}
		if got := request.HellPartyRequestFlag != 0; got != tc.hellBit {
			t.Fatalf("%s: HellPartyRequestFlag(%#02x) => hell=%t, want %t", tc.name, request.HellPartyRequestFlag, got, tc.hellBit)
		}

		// 修复档（默认）：body[7] 就是深渊标志。
		t.Setenv(dungeonHellRequestByteEnv, "")
		if got := dungeonRequestedHell(dungeonHellRequestOption(request)); got != tc.hellBit {
			t.Fatalf("%s: fixed tier hell=%t, want %t", tc.name, got, tc.hellBit)
		}

		// 回退档：逐字节复现今天的行为 —— body[5:7] 为 0 ⇒ 永远不是深渊请求。
		t.Setenv(dungeonHellRequestByteEnv, dungeonHellRequestByteEntryOptionValue)
		if dungeonRequestedHell(dungeonHellRequestOption(request)) {
			t.Fatalf("%s: rollback tier must stay on today's always-false behaviour", tc.name)
		}
	}
}

func TestDungeonRequestedHellOnlyReadsTheLowByte(t *testing.T) {
	if dungeonRequestedHell(0) || dungeonRequestedHell(0x0100) || dungeonRequestedHell(0xff00) {
		t.Fatal("difficulty byte alone must not count as a hell request")
	}
	if !dungeonRequestedHell(0x0001) || !dungeonRequestedHell(0x0201) {
		t.Fatal("low byte HellPartyRequestFlag must count as a hell request")
	}
}

func TestResolveDungeonHellPartyModeUsesRequestDifficultyThenFallsBackToOne(t *testing.T) {
	if got := resolveDungeonHellPartyMode(0x0201); got != 2 {
		t.Fatalf("requested difficulty 2 => %d", got)
	}
	if got := resolveDungeonHellPartyMode(0x0101); got != 1 {
		t.Fatalf("requested difficulty 1 => %d", got)
	}
	if got := resolveDungeonHellPartyMode(0x0001); got != 1 {
		t.Fatalf("no requested difficulty must fall back to vendor's hardcoded 1, got %d", got)
	}
}

// ── 深渊房间解析（PVF 三条判据）─────────────────────────────────────────────
//
// 厂商 Dungeon.cs:904-1029 FindHellMapRoom + TryBuildSealDoorHellPartyRoom。
func TestResolveDungeonHellRoomRequiresSealDoorRealRoomAndPillar(t *testing.T) {
	// 基准：把合成 PVF 的 test.dgn 迷宫加上 `[seal door map index] 101` +
	// `[seal door pos] 0 0`（该迷宫唯一的房间就是 (0,0)），再补一张带柱子的 101 号图。
	// 柱子 = 带 `[hellparty]` 子行的 `[special passive object]`（parse_map.go attachHellParty）。
	base := bridgeDungeonPVF(false)
	base[worldmap.DefaultMapList] += "101 `dungeon/test/hell.map`\n"
	// 柱子行用**真实 PVF 形态**：`[hellparty]` 是带反引号的字符串 token，
	// 不是 section（见 worldmap/parse_map.go 的长注）。真实样本是
	// `30528 594 301 0 1 `[hellparty]` 125 25 1 … `[/hellparty]``。
	base["map/dungeon/test/hell.map"] = "[map name]\n`hell`\n" +
		"[type]\n`[hell]`\n" +
		"[special passive object]\n" +
		"6010 7 8 9 1 `[hellparty]` 4 100 0 5 100 1 `[/hellparty]`\n"

	withSealDoor := func(pos string) bridgePVFSource {
		source := bridgePVFSource{}
		for key, value := range base {
			source[key] = value
		}
		source["dungeon/test.dgn"] = strings.Replace(
			base["dungeon/test.dgn"],
			"[start map]\n0 0\n",
			"[seal door map index]\n101\n[seal door pos]\n"+pos+"\n[start map]\n0 0\n",
			1)
		return source
	}

	loadMaze := func(t *testing.T, source bridgePVFSource) (worldmap.Maze, *worldmap.Table) {
		t.Helper()
		table, _, _ := loadBridgeDungeonStaticData(t, source)
		dungeon, ok := table.FindDungeon(700)
		if !ok {
			t.Fatal("synthetic dungeon 700 not indexed")
		}
		if len(dungeon.Mazes) == 0 {
			t.Fatal("synthetic dungeon 700 has no maze")
		}
		return dungeon.Mazes[0], table
	}

	t.Run("seal door with a real room and a pillar resolves", func(t *testing.T) {
		maze, table := loadMaze(t, withSealDoor("0 0"))
		if !maze.SealDoorMapIndex.Set || maze.SealDoorMapIndex.Value != 101 {
			t.Fatalf("seal door map index not parsed: %+v", maze.SealDoorMapIndex)
		}
		if len(maze.SealDoorPosition) < 2 || maze.SealDoorPosition[0] != 0 || maze.SealDoorPosition[1] != 0 {
			t.Fatalf("seal door position not parsed: %v", maze.SealDoorPosition)
		}
		room, ok := resolveDungeonHellRoom(&maze, table)
		if !ok {
			t.Fatal("resolveDungeonHellRoom rejected a well-formed seal door")
		}
		if room.MapID != 101 || room.X != 0 || room.Y != 0 {
			t.Fatalf("hell room=%+v want {101 0 0}", room)
		}
	})

	t.Run("seal door pointing at a non-room is rejected", func(t *testing.T) {
		maze, table := loadMaze(t, withSealDoor("1 1"))
		if _, ok := resolveDungeonHellRoom(&maze, table); ok {
			t.Fatal("(1,1) is not a room of this maze and must be rejected")
		}
	})

	t.Run("hell map without a pillar is rejected", func(t *testing.T) {
		source := withSealDoor("0 0")
		source["map/dungeon/test/hell.map"] = "[map name]\n`hell`\n" +
			"[type]\n`[hell]`\n" +
			"[special passive object]\n" +
			"6010 7 8 9 1 `[monster]` 3001 50 1 2 3\n"
		maze, table := loadMaze(t, source)
		if _, ok := resolveDungeonHellRoom(&maze, table); ok {
			t.Fatal("a map with no [hellparty] row is not a hell map (vendor IsHellPartyMap)")
		}
	})

	t.Run("missing seal door sections are rejected", func(t *testing.T) {
		maze, table := loadMaze(t, base)
		if _, ok := resolveDungeonHellRoom(&maze, table); ok {
			t.Fatal("a maze with no [seal door map index] must not resolve")
		}
	})

	t.Run("nil maze and nil table never resolve", func(t *testing.T) {
		maze, table := loadMaze(t, withSealDoor("0 0"))
		if _, ok := resolveDungeonHellRoom(nil, table); ok {
			t.Fatal("nil maze resolved")
		}
		if _, ok := resolveDungeonHellRoom(&maze, nil); ok {
			t.Fatal("nil table resolved")
		}
	})
}

// ── op28/op29 线上投影 ────────────────────────────────────────────────────
//
// 本测试只钉"runtime 上的深渊字段怎么落到字节上"，字段本身由上面两个测试负责。
func TestBuildCurrentDungeonEntryPacketsProjectsHellRoomOntoTheWire(t *testing.T) {
	build := func(t *testing.T, runtime *runtimeDungeonState) currentDungeonEntryPackets {
		t.Helper()
		packets, err := buildCurrentDungeonEntryPacketsForModes(
			runtime,
			worldmap.DungeonRoomScene{
				Coordinate: worldmap.RoomCoordinate{X: 3, Y: 0},
				Map:        worldmap.ResolvedMap{Map: worldmap.Map{ID: 60083}},
			},
			currentDungeonEntryAllowHellOption,
			currentDungeonEntrySelectionModeStrict)
		if err != nil {
			t.Fatalf("build entry packets: %v", err)
		}
		return packets
	}
	newRuntime := func(entryOption uint16) *runtimeDungeonState {
		return &runtimeDungeonState{
			Request:        dungeoncmd.SelectDungeonRequest{DungeonID: 700, EntryOption: entryOption},
			Dungeon:        worldmap.Dungeon{ID: 700, Mazes: []worldmap.Maze{{}}},
			MazeIndex:      0,
			Session:        &worldmap.DungeonSession{},
			Room:           &runtimeDungeonRoom{coordinate: worldmap.RoomCoordinate{X: 3, Y: 0}, mapID: 60083},
			BossCoordinate: worldmap.RoomCoordinate{X: 3, Y: 0},
			BossSet:        true,
			Seed:           0x12345678,
		}
	}

	t.Run("hell run carries the seal door room", func(t *testing.T) {
		runtime := newRuntime(0x0001)
		runtime.HellRoom = dungeonHellRoom{MapID: 60083, X: 3, Y: 0}
		runtime.HellRoomSet = true
		runtime.StartMapHellPartyMode, runtime.StartMapHellFogFlag =
			dungeonStartMapHellState(dungeonHellRoomVendor, 0x0001)
		packets := build(t, runtime)

		// 厂商 DungeonEntryHandler.cs:1329-1334：真坐标 + hellPartyEnabled=1 + value2=0x0B。
		if packets.DungeonInfo[10] != 3 || packets.DungeonInfo[11] != 0 {
			t.Fatalf("hell room coords=%x want (3,0)", packets.DungeonInfo[10:12])
		}
		if got := binary.LittleEndian.Uint16(packets.DungeonInfo[14:16]); got != 1 {
			t.Fatalf("hellPartyEnabled=%d want 1", got)
		}
		if packets.DungeonInfo[18] != 0x0B {
			t.Fatalf("value2=%#x want 0x0b", packets.DungeonInfo[18])
		}
		// PacketSeed 保持 UINT32_MAX（本仓没有新证据支持改它）。
		if got := binary.LittleEndian.Uint32(packets.DungeonInfo[20:24]); got != ^uint32(0) {
			t.Fatalf("packetSeed=%#x want sentinel", got)
		}
		// DungeonValue=12 是现场常量，必须没被碰过。
		if got := binary.LittleEndian.Uint16(packets.DungeonInfo[16:18]); got != 12 {
			t.Fatalf("dungeonValue=%d want 12", got)
		}
		// op29 offset 7 = hellPartyMode(1)、末尾往前第 3 字节 = fog(1)。
		if packets.StartMap[7] != 1 {
			t.Fatalf("start-map offset 7=%d want 1", packets.StartMap[7])
		}
		if got := packets.StartMap[len(packets.StartMap)-3]; got != 1 {
			t.Fatalf("start-map fog=%d want 1 body=%x", got, packets.StartMap)
		}
	})

	t.Run("ordinary run keeps the standard mode marker and the absent projection", func(t *testing.T) {
		runtime := newRuntime(0)
		runtime.StartMapHellPartyMode, runtime.StartMapHellFogFlag =
			dungeonStartMapHellState(dungeonHellRoomVendor, 0)
		packets := build(t, runtime)

		if packets.DungeonInfo[10] != 0xff || packets.DungeonInfo[11] != 0xff {
			t.Fatalf("ordinary hell coords=%x want ff ff", packets.DungeonInfo[10:12])
		}
		if got := binary.LittleEndian.Uint16(packets.DungeonInfo[14:16]); got != 0 {
			t.Fatalf("ordinary hellPartyEnabled=%d want 0", got)
		}
		if packets.DungeonInfo[18] != 0 {
			t.Fatalf("ordinary value2=%#x want 0", packets.DungeonInfo[18])
		}
		if packets.StartMap[7] != 2 {
			t.Fatalf("ordinary start-map offset 7=%d want the standard 2", packets.StartMap[7])
		}
		if got := packets.StartMap[len(packets.StartMap)-3]; got != 0 {
			t.Fatalf("ordinary fog=%d want 0", got)
		}
	})

	t.Run("legacy rollback reproduces the pre-change bytes", func(t *testing.T) {
		runtime := newRuntime(0x0001)
		runtime.StartMapHellPartyMode, runtime.StartMapHellFogFlag =
			dungeonStartMapHellState(dungeonHellRoomLegacy, 0x0001)
		packets := build(t, runtime)

		if packets.DungeonInfo[10] != 0xff || packets.DungeonInfo[11] != 0xff {
			t.Fatalf("legacy hell coords=%x want ff ff", packets.DungeonInfo[10:12])
		}
		if got := binary.LittleEndian.Uint16(packets.DungeonInfo[14:16]); got != 0 {
			t.Fatalf("legacy hellPartyEnabled=%d want 0", got)
		}
		if packets.DungeonInfo[18] != 0 || packets.StartMap[7] != 0 {
			t.Fatalf("legacy value2=%#x offset7=%d want 0/0", packets.DungeonInfo[18], packets.StartMap[7])
		}
		if got := packets.StartMap[len(packets.StartMap)-3]; got != 0 {
			t.Fatalf("legacy fog=%d want 0", got)
		}
	})
}
