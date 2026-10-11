package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfmonster "longheng.io/server/internal/modules/dnf/monster"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件钉住「深渊房间不出深渊怪物」的修复（见 dungeon_hell_party_wave.go 文件头）：
// `etc/hellparty.etc` 波次表 → 房图柱子 `[hellparty]` 组号 → 深渊房间 START_MAP 的
// **隐藏模板行**（`TemplateOrder=order`、`Flag0=1`、`Flag1=0xFF`）+ START_MAP 之后的
// **NOTI 679**，以及三个开关的取值网格。
//
// fixture 形状照真档：map 60083 的柱子行内 `[hellparty]` 用 `125 25 1`（order 1）与
// `211 20 2`（order 2），`etc/hellparty.etc` 里 125 → `A 1050 0`、211 →
// `A 65745 0 65746 0 65747 0` —— 与线上同一份数据（dump 见 `_dist/_hellparty_etc_dump.txt`）。
// 因此期望值与现场验收逐字对应：order1 1 只 + order2 3 只 = 4 行隐藏模板行、
// NOTI 679 body 36 字节、等级 90（副本 `[basis level]`）。

const (
	testHellPartyDungeonID  = 900
	testHellPartyHellMapID  = 60083
	testHellPartyPlainMapID = 100
	testHellPartyBasisLevel = 90
)

// testHellPartyWaveExpectedCodes 是 fixture 下**确定**的波次结果
// （`dungeonChoice` 恒返回 0 ⇒ 加权抽组总落在第一个候选上）。
var testHellPartyWaveExpectedCodes = []int64{1050, 65745, 65746, 65747}

func bridgeHellPartyPVF() bridgePVFSource {
	monsters := "[monster]\n"
	for _, code := range []string{"71008", "71008", "71008", "71010", "71010", "71010"} {
		monsters += code + " 1 0 300 300 0 1 1 `[fixed]` `[normal]`\n"
	}
	return bridgePVFSource{
		worldmap.DefaultMapList: "100 `dungeon/test/start.map`\n60083 `dungeon/test/hell.map`\n",
		"map/dungeon/test/start.map": "[map name]\n`start`\n" +
			"[dungeon]\n900\n" +
			"[type]\n`[start]`\n" +
			monsters,
		"map/dungeon/test/hell.map": "[map name]\n`hell`\n" +
			"[dungeon]\n900\n" +
			"[type]\n`[start]`\n" +
			monsters +
			// 柱子：真档 map 60083 那一行（`[special passive object]` + 行内 `[hellparty]`）。
			"[special passive object]\n" +
			"30528 594 301 0 1 `[hellparty]` 125 25 1 125 25 1 211 20 2 `[/hellparty]`\n",
		worldmap.DefaultDungeonList: "900 `hell.dgn`\n",
		"dungeon/hell.dgn": "[name]\n`Synthetic Hell Dungeon`\n" +
			"[minimum required level]\n10\n" +
			"[basis level]\n" + itoaTest(testHellPartyBasisLevel) + "\n" +
			"[hell dungeon]\n1\n" +
			"[hell party required item count]\n28\n" +
			"[limit party count]\n1\n" +
			"[maze info]\n" +
			"[size]\n1 1\n" +
			"[greed]\n`A`\n" +
			"[map specification]\n`map` 0 0 " + itoaTest(testHellPartyPlainMapID) + "\n" +
			"[start map]\n0 0\n" +
			"[boss map]\n0 0\n" +
			"[seal door map index]\n" + itoaTest(testHellPartyHellMapID) + "\n" +
			"[seal door pos]\n0 0\n" +
			"[seal door appear rate]\n10\n",
		worldmap.DefaultWorldMapList: "1 `test.wdm`\n",
		"worldmap/test.wdm":          "[name]\n`Synthetic Area`\n[dungeon]\n900 -1\n[/dungeon]\n",
		dnfmonster.DefaultList: "71008 `gakcle_hammerer.mob`\n71010 `gakcle_sparker.mob`\n" +
			"30528 `SealedGateOfHellDungeon4.obj`\n" +
			"1050 `CosmoFiend/Braze.mob`\n" +
			"65745 `MetroCenter/APC/knight.mob`\n65746 `MetroCenter/APC/gunner.mob`\n65747 `MetroCenter/APC/fighter.mob`\n",
		"monster/gakcle_hammerer.mob":          "[name]\n`Hammerer`\n",
		"monster/gakcle_sparker.mob":           "[name]\n`Sparker`\n",
		"monster/SealedGateOfHellDungeon4.obj": "[name]\n`Sealed Gate`\n",
		"monster/CosmoFiend/Braze.mob":         "[name]\n`Braze`\n",
		"monster/MetroCenter/APC/knight.mob":   "[name]\n`Knight`\n",
		"monster/MetroCenter/APC/gunner.mob":   "[name]\n`Gunner`\n",
		"monster/MetroCenter/APC/fighter.mob":  "[name]\n`Fighter`\n",
		// 与真档 `etc/hellparty.etc` 同形的两段（`[hellparty monster group]` + `[difficulty]`）。
		currentDungeonHellPartyPVFPath: "[hellparty monster group]\n" +
			"[group index]\n125\n[group]\n`A` 1050 0\n[/group]\n" +
			"[group index]\n126\n[group]\n`A` 1051 0\n[/group]\n" +
			"[group index]\n211\n[group]\n`A` 65745 0 65746 0 65747 0\n[/group]\n" +
			"[/hellparty monster group]\n" +
			"[difficulty]\n4 `A` 8 5 100 100 0 `B` 6 4 0 0 0\n[/difficulty]\n",
	}
}

func itoaTest(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// TestParseDungeonHellPartyCatalogMatchesVendorGroupSemantics 钉住解析口径
// （厂商 `HellPartyData.cs:110-263`）：`[group index]` + 顺次 `[group]`、单字母切难度、
// `(code, entityType)` 成对、`[difficulty]` 的 `字母 + 5 整数`。
func TestParseDungeonHellPartyCatalogMatchesVendorGroupSemantics(t *testing.T) {
	catalog, err := newPVFDungeonHellPartyCatalog(bridgeHellPartyPVF())
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Count() != 3 {
		t.Fatalf("groups=%d", catalog.Count())
	}
	if !catalog.HasEntries(125, 1) || catalog.HasEntries(125, 2) {
		t.Fatalf("group 125 A/B = %t/%t want true/false", catalog.HasEntries(125, 1), catalog.HasEntries(125, 2))
	}
	entries := catalog.Entries(211, 1)
	want := []pvfDungeonHellPartyGroupEntry{{Code: 65745}, {Code: 65746}, {Code: 65747}}
	if len(entries) != len(want) {
		t.Fatalf("group 211 entries=%+v", entries)
	}
	for index := range want {
		if entries[index] != want[index] {
			t.Fatalf("group 211 entry[%d]=%+v want=%+v", index, entries[index], want[index])
		}
	}
	rule, ok := catalog.DifficultyRule(1)
	if !ok || rule.Key != 'A' || rule.RewardRollCount != 8 || rule.Probability != 100 {
		t.Fatalf("difficulty A rule=%+v ok=%t", rule, ok)
	}
	ruleB, ok := catalog.DifficultyRule(2)
	if !ok || ruleB.Key != 'B' || ruleB.RewardRollCount != 6 || ruleB.Probability != 0 {
		t.Fatalf("difficulty B rule=%+v ok=%t", ruleB, ok)
	}
	// 难度键口径：`difficulty == 2 ? 'B' : 'A'`（厂商 HellPartyData.cs:295-298）。
	if dungeonHellPartyGroupKey(1) != 'A' || dungeonHellPartyGroupKey(2) != 'B' || dungeonHellPartyGroupKey(0) != 'A' {
		t.Fatalf("group keys=%d/%d/%d", dungeonHellPartyGroupKey(1), dungeonHellPartyGroupKey(2), dungeonHellPartyGroupKey(0))
	}
	// 读不到 etc ⇒ 具名错误（预载据此降级成"没有波次"并留事件，不阻断预载）。
	if _, err := newPVFDungeonHellPartyCatalog(bridgePVFSource{}); err == nil {
		t.Fatal("missing etc/hellparty.etc must not silently parse")
	}
}

// TestResolveDungeonHellPartyWavesOverFixtureMap 钉住装配口径
// （厂商 `Dungeon.cs:1151-1239`）：按 order 分组、`HasEntries` 过滤、`Rate` 加权抽组、
// 展开怪物等级/类型。
func TestResolveDungeonHellPartyWavesOverFixtureMap(t *testing.T) {
	source := bridgeHellPartyPVF()
	table, _, _ := loadBridgeDungeonStaticData(t, source)
	catalog, err := newPVFDungeonHellPartyCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{dungeonChoice: func(int) (int, error) { return 0, nil }}
	basis := worldmap.OptionalInt{Value: testHellPartyBasisLevel, Set: true}

	waves, err := service.resolveDungeonHellPartyWaves(table, catalog, nil, testHellPartyHellMapID, basis, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(waves) != 2 {
		t.Fatalf("waves=%+v", waves)
	}
	if waves[0].Order != 1 || waves[0].GroupID != 125 || len(waves[0].Monsters) != 1 || waves[0].Monsters[0] != (dungeonHellPartyMonster{Code: 1050, Type: 0, Level: testHellPartyBasisLevel}) {
		t.Fatalf("wave 1=%+v", waves[0])
	}
	if waves[1].Order != 2 || waves[1].GroupID != 211 || len(waves[1].Monsters) != 3 {
		t.Fatalf("wave 2=%+v", waves[1])
	}
	for index, wantCode := range testHellPartyWaveExpectedCodes {
		var code int64
		if index == 0 {
			code = waves[0].Monsters[0].Code
		} else {
			code = waves[1].Monsters[index-1].Code
		}
		if code != wantCode {
			t.Fatalf("wave code[%d]=%d want=%d", index, code, wantCode)
		}
	}

	// 难度 B：fixture 两个组都没有 `B` 行 ⇒ 一个波次都不该有（厂商 HasEntries 过滤）。
	wavesB, err := service.resolveDungeonHellPartyWaves(table, catalog, nil, testHellPartyHellMapID, basis, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(wavesB) != 0 {
		t.Fatalf("difficulty B waves=%+v want none", wavesB)
	}

	// 基准等级不可用 ⇒ 硬失败，绝不编一个等级。
	if _, err := service.resolveDungeonHellPartyWaves(table, catalog, nil, testHellPartyHellMapID, worldmap.OptionalInt{}, 1); err == nil {
		t.Fatal("missing dungeon basis level must fail loudly")
	}

	// 表不可用 ⇒ 空波次（厂商 ReadHellPartyScript 的降级），不报错。
	if wavesNil, err := service.resolveDungeonHellPartyWaves(table, nil, nil, testHellPartyHellMapID, basis, 1); err != nil || len(wavesNil) != 0 {
		t.Fatalf("catalog unavailable waves=%+v error=%v", wavesNil, err)
	}
}

// prepareHellPartyDungeonRuntimeForEntryTest 走**真实进图链路**（`prepareDungeonRuntime`），
// 深渊房间就是起始房（fixture 的 `[seal door pos] 0 0`），因此隐藏模板行落在
// `dungeon_runtime_state.go` 那条入口路径上。
func prepareHellPartyDungeonRuntimeForEntryTest(
	t *testing.T,
	source bridgePVFSource,
) (*Service, *gameSession, *bufferConn, *runtimeDungeonState, worldmap.DungeonRoomScene) {
	t.Helper()
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	hellPartyTable, err := newPVFDungeonHellPartyCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "99",
		AccountID:   "account-1",
		Level:       20,
		Stats:       map[string]int64{"fatigue": 100},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:               options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		worldMapTable:         table,
		worldMapResolver:      resolver,
		dungeonMonsterTable:   monsters,
		dungeonHellPartyTable: hellPartyTable,
		dungeonChoice:         func(int) (int, error) { return 0, nil },
		dungeonSeed:           func() (uint32, error) { return 0x12345678, nil },
		repositoryProvider:    func() (dnfrepo.Group, bool) { return repositories, true },
	}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "hell-party-wave-test", selectedCharacterID: 99}
	runtime, scene, err := service.prepareDungeonRuntime(context.Background(), session, dungeoncmd.SelectDungeonRequest{
		DungeonID:            testHellPartyDungeonID,
		Difficulty:           1,
		HellPartyRequestFlag: 1,
	})
	if err != nil {
		t.Fatalf("prepare hell party dungeon runtime: %v", err)
	}
	// `prepareDungeonRuntime` 只负责造 runtime；把它挂到会话上是上层（commit）那一步。
	// op235 的判据读 `session.dungeon.runtime`，所以这里显式挂上。
	session.dungeon.runtime = runtime
	return service, session, conn, runtime, scene
}

// TestHellPartyWaveRowsAndMonsterInfoOnHellRoomStartMap 是缺陷 B 的主用例：
// 默认档下深渊房间的 START_MAP 里出现 4 行波次隐藏模板行、START_MAP 之后出现
// NOTI 679（36 字节）；三个开关的回退档各自逐字节回到今天。
func TestHellPartyWaveRowsAndMonsterInfoOnHellRoomStartMap(t *testing.T) {
	service, session, conn, runtime, scene := prepareHellPartyDungeonRuntimeForEntryTest(t, bridgeHellPartyPVF())
	if !runtime.HellRoomSet || runtime.HellRoom.MapID != testHellPartyHellMapID {
		t.Fatalf("hell room not resolved: set=%t room=%+v", runtime.HellRoomSet, runtime.HellRoom)
	}
	if scene.Map.Map.ID != testHellPartyHellMapID {
		t.Fatalf("start scene map=%d want=%d", scene.Map.Map.ID, testHellPartyHellMapID)
	}
	if runtime.HellPartyDifficulty != 1 || len(runtime.HellPartyWaves) != 2 {
		t.Fatalf("frozen waves difficulty=%d waves=%+v", runtime.HellPartyDifficulty, runtime.HellPartyWaves)
	}

	packets, err := buildCurrentDungeonEntryPackets(runtime, scene)
	if err != nil {
		t.Fatalf("build op28/op29: %v", err)
	}
	// 6 只房图怪 + 4 行隐藏模板行（父对象行默认已不发，见 A 修复）。
	if packets.StartMap[18] != 10 {
		t.Fatalf("actor count=%d body=%x", packets.StartMap[18], packets.StartMap)
	}
	wantBodyLen := 19 + 10*21 + 4
	if len(packets.StartMap) != wantBodyLen {
		t.Fatalf("op29 len=%d want=%d body=%x", len(packets.StartMap), wantBodyLen, packets.StartMap)
	}
	assertHellPartyHiddenRows(t, packets.StartMap, wantBodyLen)
	if bytes.Contains(packets.StartMap, []byte{0x40, 0x77, 0x00, 0x00}) {
		t.Fatalf("op29 still carries the pillar parent-object row: %x", packets.StartMap)
	}

	// NOTI 679：START_MAP 之后发一条，体 = int32 count + count × (int32 code, int32 level)。
	conn.write.Reset()
	if err := service.sendCurrentHellPartyMonsterInfo(session, runtime, runtime.Room, scene, "test_after_op29"); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != a21NotiHellPartyMonsterInfo || packet.Header.Classification != 0 || len(rest) != 0 {
		t.Fatalf("679 header=%+v rest=%x", packet.Header, rest)
	}
	if a21NotiHellPartyMonsterInfo != 679 {
		t.Fatalf("NOTI 679 opcode drifted: %d", a21NotiHellPartyMonsterInfo)
	}
	want := make([]byte, 0, 36)
	want = binary.LittleEndian.AppendUint32(want, uint32(len(testHellPartyWaveExpectedCodes)))
	for _, code := range testHellPartyWaveExpectedCodes {
		want = binary.LittleEndian.AppendUint32(want, uint32(code))
		want = binary.LittleEndian.AppendUint32(want, uint32(testHellPartyBasisLevel))
	}
	if !bytes.Equal(packet.Body, want) {
		t.Fatalf("679 body=%x want=%x", packet.Body, want)
	}
	if len(packet.Body) != 36 {
		t.Fatalf("679 body len=%d want=36", len(packet.Body))
	}
}

// TestHellPartyMonsterInfoPreferRoomRowsOverTheSessionFreeze 钉住 679 的内容源：
// **优先房间里真正发出去的那几行**，房间里没有波次行时才回落到本会话冻结的波次。
//
// 为什么必须这样：队伍级共享实例下同一个房间可能由队友先建（`partyDungeonSharedRoom`），
// 线上只有他抽中的组；本会话冻结的波次（各自抽签）可能不同 ⇒ 679 必须与同一条 START_MAP
// 里给出的隐藏行一致，否则等于"告诉客户端一批它手里没有的怪"。
func TestHellPartyMonsterInfoPreferRoomRowsOverTheSessionFreeze(t *testing.T) {
	service, session, conn, runtime, scene := prepareHellPartyDungeonRuntimeForEntryTest(t, bridgeHellPartyPVF())

	// 房间行在场 ⇒ 用它（entries_source=room_extended_actor_rows）。
	waves, sourceName := currentHellPartyMonsterInfoWaves(runtime, runtime.Room)
	if sourceName != "room_extended_actor_rows" || len(waves) != 2 {
		t.Fatalf("room rows source=%s waves=%+v", sourceName, waves)
	}
	if len(waves[0].Monsters) != 1 || waves[0].Monsters[0].Code != 1050 ||
		len(waves[1].Monsters) != 3 || waves[1].Monsters[0].Code != 65745 {
		t.Fatalf("room-derived waves=%+v", waves)
	}
	conn.write.Reset()
	if err := service.sendCurrentHellPartyMonsterInfo(session, runtime, runtime.Room, scene, "test_room_rows"); err != nil {
		t.Fatal(err)
	}
	packet, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if len(packet.Body) != 36 {
		t.Fatalf("room-derived 679 body len=%d", len(packet.Body))
	}

	// 房间与冻结态**故意不一致**（模拟"队友先建了这间房、他抽中的组不同"）：
	// 679 必须跟着房间走，而不是跟着本会话的冻结态走。
	other := make([]dungeonHellPartyWave, 0, 1)
	other = append(other, dungeonHellPartyWave{Order: 1, GroupID: 126,
		Monsters: []dungeonHellPartyMonster{{Code: 1051, Type: 0, Level: testHellPartyBasisLevel}}})
	conn.write.Reset()
	if err := service.sendCurrentHellPartyMonsterInfo(session, &runtimeDungeonState{
		Dungeon: runtime.Dungeon, MazeIndex: runtime.MazeIndex,
		HellRoom: runtime.HellRoom, HellRoomSet: true, HellPartyWaves: other,
	}, runtime.Room, scene, "test_room_rows_win"); err != nil {
		t.Fatal(err)
	}
	packet, _ = splitGameServerUpperPacket(t, conn.write.Bytes())
	if !bytes.Equal(packet.Body, wantHellPartyMonsterInfoBody(testHellPartyWaveExpectedCodes)) {
		t.Fatalf("679 followed the session freeze instead of the room rows: %x", packet.Body)
	}

	// 房间里没有波次行（`WAVE_ROWS=legacy` 诊断档）⇒ 回落本会话冻结态。
	t.Setenv(hellPartyWaveRowsEnv, hellPartyWaveRowsLegacyValue)
	_, session2, conn2, runtime2, scene2 := prepareHellPartyDungeonRuntimeForEntryTest(t, bridgeHellPartyPVF())
	waves2, sourceName2 := currentHellPartyMonsterInfoWaves(runtime2, runtime2.Room)
	if sourceName2 != "session_frozen_waves" || len(waves2) != 2 {
		t.Fatalf("frozen fallback source=%s waves=%+v", sourceName2, waves2)
	}
	conn2.write.Reset()
	if err := service.sendCurrentHellPartyMonsterInfo(session2, runtime2, runtime2.Room, scene2, "test_frozen_fallback"); err != nil {
		t.Fatal(err)
	}
	packet, _ = splitGameServerUpperPacket(t, conn2.write.Bytes())
	if !bytes.Equal(packet.Body, wantHellPartyMonsterInfoBody(testHellPartyWaveExpectedCodes)) {
		t.Fatalf("frozen fallback 679 body=%x", packet.Body)
	}
}

// wantHellPartyMonsterInfoBody 是对账用的期望体：`i32 count` + count × `(i32 code, i32 level)`。
func wantHellPartyMonsterInfoBody(codes []int64) []byte {
	body := make([]byte, 0, 4+8*len(codes))
	body = binary.LittleEndian.AppendUint32(body, uint32(len(codes)))
	for _, code := range codes {
		body = binary.LittleEndian.AppendUint32(body, uint32(code))
		body = binary.LittleEndian.AppendUint32(body, uint32(testHellPartyBasisLevel))
	}
	return body
}

// assertHellPartyHiddenRows 逐字节钉住那 4 行隐藏模板行：
// `TemplateOrder=order`、`PacketIndex` 接着房图怪往后排、`Flag0=1`、`Flag1=0xFF`、
// `ExtraState=0`、`Blocking=0`、`Type=0`、`Level=副本基准等级`（厂商
// `DungeonMapHandler.cs:847-879` + `DungeonNotificationBuilder.cs:166-186`）。
func assertHellPartyHiddenRows(t *testing.T, body []byte, bodyLen int) {
	t.Helper()
	if len(body) != bodyLen {
		t.Fatalf("body len=%d want=%d", len(body), bodyLen)
	}
	order := []uint16{1, 2, 2, 2}
	packetIndex := []uint32{6, 7, 8, 9}
	objectKey := []uint16{408, 409, 410, 411}
	for index, wantCode := range testHellPartyWaveExpectedCodes {
		offset := 19 + (6+index)*21
		row := body[offset : offset+21]
		got := struct {
			templateOrder uint16
			packetIndex   uint32
			objectKey     uint16
			code          uint32
			level         byte
			actorType     byte
			flag0         byte
			flag1         byte
			extraState    uint32
			blocking      byte
		}{
			templateOrder: binary.LittleEndian.Uint16(row[0:2]),
			packetIndex:   binary.LittleEndian.Uint32(row[2:6]),
			objectKey:     binary.LittleEndian.Uint16(row[6:8]),
			code:          binary.LittleEndian.Uint32(row[8:12]),
			level:         row[12],
			actorType:     row[13],
			flag0:         row[14],
			flag1:         row[15],
			extraState:    binary.LittleEndian.Uint32(row[16:20]),
			blocking:      row[20],
		}
		if got.code != uint32(wantCode) || got.templateOrder != order[index] ||
			got.packetIndex != packetIndex[index] || got.objectKey != objectKey[index] ||
			got.flag0 != hellPartyHiddenTemplateFlag || got.flag1 != hellPartyAttachAllWavesSelector ||
			got.extraState != 0 || got.blocking != 0 || got.actorType != 0 || got.level != testHellPartyBasisLevel {
			t.Fatalf("hidden row[%d]=%+v want code=%d order=%d index=%d key=%d flag0=%d flag1=%d level=%d",
				index, got, wantCode, order[index], packetIndex[index], objectKey[index],
				hellPartyHiddenTemplateFlag, hellPartyAttachAllWavesSelector, testHellPartyBasisLevel)
		}
	}
}

// TestHellPartyWaveSwitchGridMatchesDeclaredTiers 钉住三条开关的取值网格：
// 两个波次开关都 legacy ⇒ 逐字节等于改动前（连随机都不抽、也没有 NOTI）；
// 单独 legacy 各自只关掉自己那一半。
func TestHellPartyWaveSwitchGridMatchesDeclaredTiers(t *testing.T) {
	t.Run("both_legacy_reproduces_today", func(t *testing.T) {
		t.Setenv(hellPartyWaveRowsEnv, hellPartyWaveRowsLegacyValue)
		t.Setenv(hellPartyMonsterInfoEnv, hellPartyMonsterInfoLegacyValue)
		if dungeonHellPartyWavesNeeded() {
			t.Fatal("both switches legacy must not resolve waves at all")
		}
		service, session, conn, runtime, scene := prepareHellPartyDungeonRuntimeForEntryTest(t, bridgeHellPartyPVF())
		if !runtime.HellRoomSet || len(runtime.HellPartyWaves) != 0 {
			t.Fatalf("legacy tier froze waves: set=%t waves=%+v", runtime.HellRoomSet, runtime.HellPartyWaves)
		}
		packets, err := buildCurrentDungeonEntryPackets(runtime, scene)
		if err != nil {
			t.Fatal(err)
		}
		// 改动前就是"6 只房图怪"（现场 actor_count=7 那次多了柱子父对象行，A 修复后是 6）。
		if packets.StartMap[18] != 6 || len(packets.StartMap) != 19+6*21+4 {
			t.Fatalf("legacy tier op29 actor_count=%d len=%d body=%x", packets.StartMap[18], len(packets.StartMap), packets.StartMap)
		}
		conn.write.Reset()
		if err := service.sendCurrentHellPartyMonsterInfo(session, runtime, runtime.Room, scene, "test_legacy"); err != nil {
			t.Fatal(err)
		}
		if conn.write.Len() != 0 {
			t.Fatalf("legacy tier sent a 679: %x", conn.write.Bytes())
		}
	})

	t.Run("monster_info_legacy_keeps_rows_only", func(t *testing.T) {
		t.Setenv(hellPartyMonsterInfoEnv, hellPartyMonsterInfoLegacyValue)
		service, session, conn, runtime, scene := prepareHellPartyDungeonRuntimeForEntryTest(t, bridgeHellPartyPVF())
		packets, err := buildCurrentDungeonEntryPackets(runtime, scene)
		if err != nil {
			t.Fatal(err)
		}
		if packets.StartMap[18] != 10 {
			t.Fatalf("wave_rows=hidden must still emit rows: actor_count=%d", packets.StartMap[18])
		}
		assertHellPartyHiddenRows(t, packets.StartMap, 19+10*21+4)
		conn.write.Reset()
		if err := service.sendCurrentHellPartyMonsterInfo(session, runtime, runtime.Room, scene, "test_rows_only"); err != nil {
			t.Fatal(err)
		}
		if conn.write.Len() != 0 {
			t.Fatalf("monster_info=legacy sent a 679: %x", conn.write.Bytes())
		}
	})

	t.Run("wave_rows_legacy_keeps_noti_only", func(t *testing.T) {
		t.Setenv(hellPartyWaveRowsEnv, hellPartyWaveRowsLegacyValue)
		service, session, conn, runtime, scene := prepareHellPartyDungeonRuntimeForEntryTest(t, bridgeHellPartyPVF())
		packets, err := buildCurrentDungeonEntryPackets(runtime, scene)
		if err != nil {
			t.Fatal(err)
		}
		if packets.StartMap[18] != 6 || bytes.Contains(packets.StartMap, []byte{0x1a, 0x04, 0x00, 0x00}) {
			t.Fatalf("wave_rows=legacy must not emit rows: actor_count=%d body=%x", packets.StartMap[18], packets.StartMap)
		}
		// 波次仍然被解析（NOTI 那一半要用），但**没有**隐藏行。
		if len(runtime.HellPartyWaves) != 2 {
			t.Fatalf("wave_rows=legacy monster_info=noti must still resolve waves: %+v", runtime.HellPartyWaves)
		}
		conn.write.Reset()
		if err := service.sendCurrentHellPartyMonsterInfo(session, runtime, runtime.Room, scene, "test_noti_only"); err != nil {
			t.Fatal(err)
		}
		packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if packet.Header.MsgID != a21NotiHellPartyMonsterInfo || len(packet.Body) != 36 || len(rest) != 0 {
			t.Fatalf("wave_rows=legacy 679 header=%+v body_len=%d rest=%x", packet.Header, len(packet.Body), rest)
		}
	})
}

// TestHellPartySwitchReadersAcceptOnlyTheLiteralRollbackValue 钉住三个开关的语义：
// **只有**显式 `legacy` 才回退。
func TestHellPartySwitchReadersAcceptOnlyTheLiteralRollbackValue(t *testing.T) {
	for _, value := range []string{"", "hidden", "noti", "accept", "on", "1", "true", "off", "no"} {
		if got := readDungeonHellPartyWaveRowsMode(value); got != hellPartyWaveRowsHidden {
			t.Fatalf("wave rows value=%q resolved to %s", value, got)
		}
		if got := readDungeonHellPartyMonsterInfoMode(value); got != hellPartyMonsterInfoNoti {
			t.Fatalf("monster info value=%q resolved to %s", value, got)
		}
		if got := readDungeonHellPartyStartCommandMode(value); got != hellPartyStartCommandAccept {
			t.Fatalf("start cmd value=%q resolved to %s", value, got)
		}
	}
	if got := readDungeonHellPartyWaveRowsMode(" LEGACY "); got != hellPartyWaveRowsLegacy {
		t.Fatalf("wave rows rollback=%s", got)
	}
	if got := readDungeonHellPartyMonsterInfoMode("Legacy"); got != hellPartyMonsterInfoLegacy {
		t.Fatalf("monster info rollback=%s", got)
	}
	if got := readDungeonHellPartyStartCommandMode("legacy"); got != hellPartyStartCommandLegacy {
		t.Fatalf("start cmd rollback=%s", got)
	}
	t.Setenv(hellPartyWaveRowsEnv, "legacy")
	if got := hellPartyWaveRowsModeFromEnv().String(); got != "legacy" {
		t.Fatalf("env wave rows=%s", got)
	}
	// 波次解析是"任一半修复就要做"：只关 rows 时 679 那一半仍然需要波次。
	if !dungeonHellPartyWavesNeeded() {
		t.Fatal("wave_rows=legacy alone must still resolve waves for the 679 half")
	}
	t.Setenv(hellPartyMonsterInfoEnv, "legacy")
	if dungeonHellPartyWavesNeeded() {
		t.Fatal("both halves legacy must not resolve waves at all")
	}
}

// TestHellPartyStartCommandAcceptsOp235OnlyInHellRoom 钉住 op235 的分派
// （厂商 `GameProtocolHandler.cs:970-978` + `DungeonMapHandler.cs:944-963`）：
// 深渊房间里收下且**不回包**、重复收下幂等；非深渊房间/回退档一律原样落到分解机分支。
func TestHellPartyStartCommandAcceptsOp235OnlyInHellRoom(t *testing.T) {
	service, session, conn, runtime, scene := prepareHellPartyDungeonRuntimeForEntryTest(t, bridgeHellPartyPVF())
	if len(runtime.HellPartyWaves) == 0 {
		t.Fatal("fixture did not freeze waves")
	}
	if a21CmdHellPartyStart != 235 || uint16(dnfenum.CmdPacketRequestDisjointItem) != 235 {
		t.Fatalf("op235 drifted: %d", a21CmdHellPartyStart)
	}

	conn.write.Reset()
	handled, err := service.acceptCurrentHellPartyStartCommand(session, a21CmdHellPartyStart, []byte{0})
	if err != nil || !handled {
		t.Fatalf("hell room op235 handled=%t error=%v", handled, err)
	}
	if !runtime.HellPartyStarted {
		t.Fatal("hell party phase did not move to started")
	}
	if conn.write.Len() != 0 {
		t.Fatalf("HELLPARTY_START must not reply: %x", conn.write.Bytes())
	}
	// 重复一次：仍然收下（幂等），仍然不回包。
	handled, err = service.acceptCurrentHellPartyStartCommand(session, a21CmdHellPartyStart, []byte{0})
	if err != nil || !handled || conn.write.Len() != 0 {
		t.Fatalf("duplicate op235 handled=%t error=%v write=%x", handled, err, conn.write.Bytes())
	}

	// 回退档：不接管。
	t.Run("legacy_does_not_take_over", func(t *testing.T) {
		t.Setenv(hellPartyStartCommandEnv, hellPartyStartCommandLegacyValue)
		handled, err := service.acceptCurrentHellPartyStartCommand(session, a21CmdHellPartyStart, []byte{0})
		if err != nil || handled {
			t.Fatalf("legacy tier handled=%t error=%v", handled, err)
		}
	})

	// 非深渊房间：不接管（这里 = 没有冻结深渊房间的会话）。同一条会话也用来证明
	// wrapper 在不接管时**确实**落到了既有的分解机 handler。
	nonHellConn := &bufferConn{}
	nonHellSession := &gameSession{conn: nonHellConn, connID: "non-hell", selectedCharacterID: 99,
		dungeon: dungeonSessionState{runtime: &runtimeDungeonState{
			Request:     dungeoncmd.SelectDungeonRequest{DungeonID: 900},
			Dungeon:     runtime.Dungeon,
			Session:     &worldmap.DungeonSession{},
			Room:        runtime.Room,
			HellRoomSet: false,
		}}}
	t.Run("non_hell_room_does_not_take_over", func(t *testing.T) {
		handled, err := service.acceptCurrentHellPartyStartCommand(nonHellSession, a21CmdHellPartyStart, []byte{0})
		if err != nil || handled {
			t.Fatalf("non hell room handled=%t error=%v", handled, err)
		}
	})

	// 委派链路：wrapper 在不接管时必须落到既有的分解机 handler
	// （那条路对非法体回一条 235 的失败包 ⇒ 用"写出了一条 msg_id=235 的包"证明委派发生）。
	t.Run("delegates_to_the_expert_job_store_handler", func(t *testing.T) {
		nonHellConn.write.Reset()
		if err := service.handleCurrentExpertJobStoreDisjointOrHellPartyStart(nonHellSession, a21CmdHellPartyStart, []byte{0x01}); err != nil {
			t.Fatal(err)
		}
		packet, rest := splitGameServerUpperPacket(t, nonHellConn.write.Bytes())
		if packet.Header.MsgID != a21CmdHellPartyStart || len(rest) != 0 || len(packet.Body) == 0 {
			t.Fatalf("delegated failure packet header=%+v body=%x rest=%x", packet.Header, packet.Body, rest)
		}
		if scene.Map.Map.ID != runtime.HellRoom.MapID {
			t.Fatalf("fixture scene drifted: %d", scene.Map.Map.ID)
		}
	})
}
