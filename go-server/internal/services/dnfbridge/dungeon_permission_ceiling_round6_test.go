package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 段107 后半（难度后端）纯投影用例。只覆盖 op5 权限快照的读侧投影
// （currentDungeonMaxClearState / currentDungeonResourceStateEntriesFromPermissionRecord）
// 与"选角 ack 后立刻发快照"这条线路的最高难度 state，不改产品行为。
//
// 合成副本与 bridgeDungeonPVF 的 test.dgn 同构，只替换难度元数据段；因此
// maxState 的取值完全由被测实现的计数规则决定：
//   - 无难度元数据段、或 [difficulty level] 非零项 <= 1 => maxState 0
//   - [difficulty level] 非零项计数 n（n > 1）          => maxState n-1
//   - [designate dungeon difficulty] 非空               => maxState 4
const (
	round6HighDungeonID  = 7145 // 5 个非零 difficulty level => maxState 4
	round6LowDungeonID   = 3    // 1 个非零 difficulty level => maxState 0
	round6PlainDungeonID = 700  // 无难度元数据段            => maxState 0
	round6DesigDungeonID = 5056 // designate dungeon difficulty => maxState 4
)

// round6DungeonFile 生成与 bridgeDungeonPVF 的 test.dgn 同构的合成副本文件。
func round6DungeonFile(difficultyMetadata string) string {
	return "[name]\n`Synthetic Dungeon`\n" +
		"[minimum required level]\n10\n" +
		"[basis level]\n20\n" +
		"[limit party count]\n1\n" +
		difficultyMetadata +
		"[maze info]\n[size]\n1 1\n[greed]\n`A`\n" +
		"[map specification]\n`map` 0 0 100\n[start map]\n0 0\n[boss map]\n0 0\n"
}

// round6PermissionPVF 在 bridgeDungeonPVF 之上追加三个合成副本，用于覆盖
// 最高难度、非零计数不足（maxState 0）与 designate dungeon difficulty 三条 ceiling 规则。
func round6PermissionPVF() bridgePVFSource {
	source := bridgeDungeonPVF(false)
	// 700 保持 bridgeDungeonPVF 原样：无难度元数据段 => maxState 0。
	source[worldmap.DefaultDungeonList] = source[worldmap.DefaultDungeonList] +
		fmt.Sprintf("%d `high.dgn`\n", round6HighDungeonID) +
		fmt.Sprintf("%d `low.dgn`\n", round6LowDungeonID) +
		fmt.Sprintf("%d `desig.dgn`\n", round6DesigDungeonID)
	source[worldmap.DefaultMapList] = source[worldmap.DefaultMapList] +
		"101 `dungeon/high/start.map`\n" +
		"102 `dungeon/low/start.map`\n" +
		"103 `dungeon/desig/start.map`\n"
	source["dungeon/high.dgn"] = round6DungeonFile("[difficulty level]\n1 2 3 4 5\n")
	source["dungeon/low.dgn"] = round6DungeonFile("[difficulty level]\n1\n")
	source["dungeon/desig.dgn"] = round6DungeonFile("[designate dungeon difficulty]\n0 1 2 3\n")
	source["map/dungeon/high/start.map"] = round6StartMap(round6HighDungeonID)
	source["map/dungeon/low/start.map"] = round6StartMap(round6LowDungeonID)
	source["map/dungeon/desig/start.map"] = round6StartMap(round6DesigDungeonID)
	return source
}

func round6StartMap(dungeonID int) string {
	return "[map name]\n`start`\n" +
		"[dungeon]\n" + fmt.Sprintf("%d", dungeonID) + "\n" +
		"[type]\n`[start]`\n"
}

func round6PermissionTable(t *testing.T) *worldmap.Table {
	t.Helper()
	table, _, _ := loadBridgeDungeonStaticData(t, round6PermissionPVF())
	return table
}

func round6Record(entries ...dnfrepo.DungeonPermissionEntry) dnfrepo.DungeonPermissionRecord {
	return dnfrepo.DungeonPermissionRecord{CharacterID: "99", Entries: entries}
}

//  1. 最高难度：maxState 4 的副本 + 落库 state 4 必须原样出现在快照里，
//     且不得被夹取或跳过（现状无用例覆盖这一档）。
func TestRound6PermissionProjectionKeepsHighestDifficultyState(t *testing.T) {
	table := round6PermissionTable(t)
	dungeon, found := table.FindDungeon(round6HighDungeonID)
	if !found {
		t.Fatalf("synthetic dungeon %d missing from table", round6HighDungeonID)
	}
	if got := currentDungeonMaxClearState(dungeon); got != 4 {
		t.Fatalf("synthetic dungeon %d maxClearState=%d want 4 (fixture ceiling)", round6HighDungeonID, got)
	}
	entries := currentDungeonResourceStateEntriesFromPermissionRecord(
		table,
		round6Record(dnfrepo.DungeonPermissionEntry{DungeonID: round6HighDungeonID, ClearState: 4}),
	)
	want := []currentDungeonResourceStateEntry{{DungeonID: round6HighDungeonID, State: 4}}
	if len(entries) != 1 || entries[0] != want[0] {
		t.Fatalf("projection=%v want=%v", entries, want)
	}
	body := buildCurrentDungeonResourceStateEntriesBody(entries)
	if !bytes.Equal(body, buildCurrentDungeonResourceStateEntriesBody(want)) {
		t.Fatalf("projected body=%x want=%x", body, buildCurrentDungeonResourceStateEntriesBody(want))
	}
}

// 2) 读侧夹取：落库 state 高于 PVF ceiling 时夹到 ceiling，行不得消失。
func TestRound6PermissionProjectionClampsStateAbovePVFCeiling(t *testing.T) {
	table := round6PermissionTable(t)
	entries := currentDungeonResourceStateEntriesFromPermissionRecord(
		table,
		round6Record(dnfrepo.DungeonPermissionEntry{DungeonID: round6HighDungeonID, ClearState: 5}),
	)
	want := []currentDungeonResourceStateEntry{{DungeonID: round6HighDungeonID, State: 4}}
	if len(entries) != 1 || entries[0] != want[0] {
		t.Fatalf("clamped projection=%v want=%v", entries, want)
	}
}

//  3. maxState 0：无难度元数据段、非零项 <= 1 的副本即使已落库，也不会出现在快照里。
//     这是唯一能在服务端让一条已落库进度行从快照中消失的路径，独立于发包时序。
func TestRound6PermissionProjectionDropsDungeonsWithoutClearCeiling(t *testing.T) {
	table := round6PermissionTable(t)
	for _, id := range []int{round6PlainDungeonID, round6LowDungeonID} {
		dungeon, found := table.FindDungeon(int64(id))
		if !found {
			t.Fatalf("synthetic dungeon %d missing from table", id)
		}
		if got := currentDungeonMaxClearState(dungeon); got != 0 {
			t.Fatalf("dungeon %d maxClearState=%d want 0 (fixture ceiling)", id, got)
		}
	}
	entries := currentDungeonResourceStateEntriesFromPermissionRecord(
		table,
		round6Record(
			dnfrepo.DungeonPermissionEntry{DungeonID: round6PlainDungeonID, ClearState: 1},
			dnfrepo.DungeonPermissionEntry{DungeonID: round6LowDungeonID, ClearState: 2},
		),
	)
	if len(entries) != 0 {
		t.Fatalf("ceiling-less rows leaked into snapshot: %v", entries)
	}
	// 对照：同一个 record 里带 ceiling 的副本仍然保留。
	mixed := currentDungeonResourceStateEntriesFromPermissionRecord(
		table,
		round6Record(
			dnfrepo.DungeonPermissionEntry{DungeonID: round6PlainDungeonID, ClearState: 1},
			dnfrepo.DungeonPermissionEntry{DungeonID: round6HighDungeonID, ClearState: 4},
		),
	)
	wantMixed := []currentDungeonResourceStateEntry{{DungeonID: round6HighDungeonID, State: 4}}
	if len(mixed) != 1 || mixed[0] != wantMixed[0] {
		t.Fatalf("mixed projection=%v want=%v", mixed, wantMixed)
	}
}

//  4. 排序 + 输入保持：输出按 DungeonID 升序，且不得改写调用方传入的 record。
//     同时覆盖 designate dungeon difficulty => maxState 4 与未知副本被跳过。
func TestRound6PermissionProjectionSortsAndPreservesRepositoryInput(t *testing.T) {
	table := round6PermissionTable(t)
	for id, wantCeiling := range map[int]byte{round6DesigDungeonID: 4, round6HighDungeonID: 4} {
		dungeon, found := table.FindDungeon(int64(id))
		if !found {
			t.Fatalf("synthetic dungeon %d missing from table", id)
		}
		if got := currentDungeonMaxClearState(dungeon); got != wantCeiling {
			t.Fatalf("dungeon %d maxClearState=%d want %d", id, got, wantCeiling)
		}
	}
	record := round6Record(
		dnfrepo.DungeonPermissionEntry{DungeonID: round6HighDungeonID, ClearState: 4},
		dnfrepo.DungeonPermissionEntry{DungeonID: round6DesigDungeonID, ClearState: 3},
		dnfrepo.DungeonPermissionEntry{DungeonID: 0, ClearState: 1},
		dnfrepo.DungeonPermissionEntry{DungeonID: 999999, ClearState: 2},
		dnfrepo.DungeonPermissionEntry{DungeonID: round6PlainDungeonID, ClearState: 0},
	)
	before := append([]dnfrepo.DungeonPermissionEntry(nil), record.Entries...)
	entries := currentDungeonResourceStateEntriesFromPermissionRecord(table, record)
	want := []currentDungeonResourceStateEntry{
		{DungeonID: round6DesigDungeonID, State: 3},
		{DungeonID: round6HighDungeonID, State: 4},
	}
	if len(entries) != len(want) {
		t.Fatalf("sorted projection=%v want=%v", entries, want)
	}
	for index := range want {
		if entries[index] != want[index] {
			t.Fatalf("sorted projection=%v want=%v", entries, want)
		}
	}
	if len(record.Entries) != len(before) {
		t.Fatalf("projection changed repository input length=%d want %d", len(record.Entries), len(before))
	}
	for index := range before {
		if record.Entries[index] != before[index] {
			t.Fatalf("projection mutated repository input at %d: got=%+v want=%+v", index, record.Entries[index], before[index])
		}
	}
}

//  5. 选角恢复最高难度：落库 state 4（= PVF ceiling）时，选角 ack 之后的 op5 行必须是 4。
//     复用 dungeon_resource_state_test.go 的选角 fixture 形态，不改旧用例。
func TestRound6SelectCharacterSnapshotKeepsHighestDifficultyState(t *testing.T) {
	table, resolver, _ := loadBridgeDungeonStaticData(t, round6PermissionPVF())
	repositories := testRepositoryGroup()
	character := dnfrepo.CharacterRecord{
		CharacterID: "99",
		AccountID:   defaultAccountPrefix + "1",
		Slot:        3,
		Name:        "worldmap-owner",
		Job:         "0",
		Level:       20,
		Stats: map[string]int64{
			"town_id": 38, "area_id": 1,
			"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
			currentDungeonTutorialCompletedKey: 1,
		},
	}
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	if _, updated, err := repositories.DungeonPermission.UpsertMax(context.Background(), "99", round6HighDungeonID, 4); err != nil || !updated {
		t.Fatalf("seed dungeon permission updated=%t err=%v", updated, err)
	}
	service := &Service{
		options:            options{accountPrefix: defaultAccountPrefix},
		worldMapTable:      table,
		worldMapResolver:   resolver,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	connection := &bufferConn{}
	session := &gameSession{
		conn:            connection,
		channel:         channelcatalog.Channel{ID: 16, Type: 1, Name: "ch.16", Port: 10016},
		residentChannel: channelcatalog.Channel{ID: 16, Type: 1, Name: "ch.16", Port: 10016},
		rosterRequested: true,
	}
	request := make([]byte, 2)
	binary.LittleEndian.PutUint16(request, 99)
	if err := service.sendUpperCSharpSelectInit(session, request); err != nil {
		t.Fatal(err)
	}

	ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketSelectCharacter) {
		t.Fatalf("first packet header=%+v body=%x", ack.Header, ack.Body)
	}
	permission, _ := splitGameServerUpperPacket(t, rest)
	wantPermission := buildCurrentDungeonResourceStateEntriesBody([]currentDungeonResourceStateEntry{{DungeonID: round6HighDungeonID, State: 4}})
	if permission.Header.Classification != 0 || permission.Header.MsgID != currentDungeonResourceStateMsgID || !bytes.Equal(permission.Body, wantPermission) {
		t.Fatalf("post-select ACK permission header=%+v body=%x want=%x", permission.Header, permission.Body, wantPermission)
	}
}
