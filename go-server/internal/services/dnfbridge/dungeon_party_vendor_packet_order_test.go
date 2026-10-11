package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件是"厂商 9.16 包序对齐"这一轮的回归：**厂商日志里的原始 hex 当夹具**，
// 把两条新增下发（副本进场 op11 remotes、start-map 后的 op9 房间槽位表）逐字节钉死，
// 并把两个开关的档位矩阵钉成"默认 = 厂商行为 / legacy = 逐字节回退"。
//
// 夹具来源（只读引用 `D:\DNF90-test\server\runtime\logs\packet_log.txt`）：
//   - op11 remotes（formation 与副本进场两处 body 逐字节相同）：
//     :3337/:5361 conn=game-000059 → 010200c0a80065c0a8006575301c000000010002000064
//     :3340/:5380 conn=game-000061 → 010100c0a80065c0a8006575311b000000010002000064
//   - op9 房间槽位表（68B，去掉 16 字节头）：
//     :5407 conn=game-000061（char 2 / object_key 2）
//     :5411 conn=game-000059（char 1 / object_key 1）
//     :25721 conn=game-000052（过图那次，行里的等级变成 t1=5 / t2=2）

// dungeonPartyVendorSlotTableFixture 是一条厂商 op9 房间槽位表的夹具：
// 完整 SEND hex（含 16 字节头）+ 逐字节 body。
type dungeonPartyVendorSlotTableFixture struct {
	name      string
	objectKey uint16
	actorName string
	actorJob  byte
	levels    [2]byte
	names     [2]string
	fullHex   string
}

func dungeonPartyVendorSlotTableFixtures() []dungeonPartyVendorSlotTableFixture {
	return []dungeonPartyVendorSlotTableFixture{
		{
			name:      "captured_conn61_char2_entry",
			objectKey: 2,
			actorName: "t2",
			actorJob:  0,
			levels:    [2]byte{1, 1},
			names:     [2]string{"t1", "t2"},
			fullHex:   "000900540000005500f2c7720000000001000f2702000000000000000200000074320004000000000000000000000002000100000100000200000074310064010102000001000002000000743200640100000000",
		},
		{
			name:      "captured_conn59_char1_entry",
			objectKey: 1,
			actorName: "t1",
			actorJob:  0,
			levels:    [2]byte{1, 1},
			names:     [2]string{"t1", "t2"},
			fullHex:   "00090054000000a1ffa4ede80300000001000f2701000000000000000200000074310004000000000000000000000002000100000100000200000074310064010102000001000002000000743200640100000000",
		},
		{
			// 过图那一次（10:41:18.7315192Z，party 3）：同形，但行里的等级是真实等级 5/2。
			name:      "captured_move_char2_level5_2",
			objectKey: 2,
			actorName: "t2",
			actorJob:  0,
			levels:    [2]byte{5, 2},
			names:     [2]string{"t1", "t2"},
			fullHex:   "00090054000000b39d2e80f20000000001000f2702000000000000000200000074320004000000000000000000000002000100000500000200000074310064010102000002000002000000743200640100000000",
		},
	}
}

func dungeonPartyVendorFixtureBody(t *testing.T, fullHex string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(fullHex)
	if err != nil {
		t.Fatalf("vendor fixture hex: %v", err)
	}
	if len(raw) < 16 {
		t.Fatalf("vendor fixture too short len=%d", len(raw))
	}
	return raw[16:]
}

// TestVendorOp9RoomSlotTableBodyByteForByteFromCapturedFixtures 是这一轮的核心断言：
// 我们的 builder 必须**逐字节**复现厂商那三条 op9 房间槽位表 body。
func TestVendorOp9RoomSlotTableBodyByteForByteFromCapturedFixtures(t *testing.T) {
	for _, fixture := range dungeonPartyVendorSlotTableFixtures() {
		want := dungeonPartyVendorFixtureBody(t, fixture.fullHex)
		if len(want) != 68 {
			t.Fatalf("%s: fixture body_len=%d want 68", fixture.name, len(want))
		}
		got := buildDungeonPartyRoomSlotTableBody(
			dungeonPartyRoomSlotTableActor{
				ObjectKey: fixture.objectKey,
				Name:      fixture.actorName,
				Job:       fixture.actorJob,
			},
			[]dungeonPartyRoomSlotTableRow{
				{Slot: 0, UserID: 1, Job: 0, Level: fixture.levels[0], Name: fixture.names[0]},
				{Slot: 1, UserID: 2, Job: 0, Level: fixture.levels[1], Name: fixture.names[1]},
			},
			0,
			vendorDungeonPartyRoomSlotTableShape,
		)
		if len(got) != len(want) {
			t.Fatalf("%s: body_len=%d want=%d\n got=% X\nwant=% X", fixture.name, len(got), len(want), got, want)
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("%s: byte[%d]=%02x want=%02x\n got=% X\nwant=% X",
					fixture.name, index, got[index], want[index], got, want)
			}
		}
		// 逐字段回读：把"为什么这些字节是这样"钉在断言里。
		if got[2] != 0x0f || got[3] != 0x27 {
			t.Fatalf("%s: stable scene value=% X want 0f 27", fixture.name, got[2:4])
		}
		if binary.LittleEndian.Uint16(got[4:6]) != fixture.objectKey {
			t.Fatalf("%s: object_key=%d want=%d", fixture.name, binary.LittleEndian.Uint16(got[4:6]), fixture.objectKey)
		}
		if got[7] != currentSceneObjectContext || got[9] != currentSceneOp9ActorDisplayKind {
			t.Fatalf("%s: owner=%d kind=%d want %d/%d", fixture.name, got[7], got[9], currentSceneObjectContext, currentSceneOp9ActorDisplayKind)
		}
		if got[10] != currentSceneObjectRoute || got[11] != currentSceneObjectContext {
			t.Fatalf("%s: route=%d context=%d want %d/%d", fixture.name, got[10], got[11], currentSceneObjectRoute, currentSceneObjectContext)
		}
		if got[19] != vendorDungeonPartyRoomSlotTableShape.ActorAux {
			t.Fatalf("%s: actor_aux=%02x want=%02x", fixture.name, got[19], vendorDungeonPartyRoomSlotTableShape.ActorAux)
		}
		if got[31] != 2 {
			t.Fatalf("%s: row_count=%d want 2", fixture.name, got[31])
		}
		// 行尾 3 字节 + 尾部 4 字节（host_slot 在倒数第 3 字节）。
		if got[len(got)-7] != 0x00 ||
			got[len(got)-6] != vendorDungeonPartyRoomSlotTableShape.RowHPPercent ||
			got[len(got)-5] != vendorDungeonPartyRoomSlotTableShape.RowRoomFlag {
			t.Fatalf("%s: last row tail=% X want 00 64 01", fixture.name, got[len(got)-7:len(got)-4])
		}
		if got[len(got)-3] != 0 || got[len(got)-2] != 0 || got[len(got)-1] != 0 {
			t.Fatalf("%s: body tail=% X want 00 00 00 (host_slot=0)", fixture.name, got[len(got)-4:])
		}
	}
}

// TestVendorOp9RoomSlotTableRowLevelTracksMemberLevel：厂商过图那一条夹具里 t1=5 / t2=2，
// 证明行里的那个字节是**成员等级**而不是常量（actor 段第 19 字节才是常量 0x04）。
func TestVendorOp9RoomSlotTableRowLevelTracksMemberLevel(t *testing.T) {
	want := dungeonPartyVendorFixtureBody(t, dungeonPartyVendorSlotTableFixtures()[2].fullHex)
	got := buildDungeonPartyRoomSlotTableBody(
		dungeonPartyRoomSlotTableActor{ObjectKey: 2, Name: "t2"},
		[]dungeonPartyRoomSlotTableRow{
			{Slot: 0, UserID: 1, Level: 5, Name: "t1"},
			{Slot: 1, UserID: 2, Level: 2, Name: "t2"},
		},
		0,
		vendorDungeonPartyRoomSlotTableShape,
	)
	if string(got) != string(want) {
		t.Fatalf("level fixture mismatch\n got=% X\nwant=% X", got, want)
	}
}

// TestDungeonRoomSlotTableSwitchDefaultsAndReadback 钉住两个新开关的档位解析：
// **默认 = 厂商行为**，非法值也回落默认，只有显式写回退值才回退。
func TestDungeonRoomSlotTableSwitchDefaultsAndReadback(t *testing.T) {
	if got := readDungeonPartyRoomSlotTableMode(""); got != dungeonPartyRoomSlotTableVendor {
		t.Fatalf("empty env mode=%v want vendor", got)
	}
	if got := readDungeonPartyRoomSlotTableMode("nonsense"); got != dungeonPartyRoomSlotTableVendor {
		t.Fatalf("junk env mode=%v want vendor", got)
	}
	if got := readDungeonPartyRoomSlotTableMode(" ONCE "); got != dungeonPartyRoomSlotTableOnce {
		t.Fatalf("once env mode=%v", got)
	}
	if got := readDungeonPartyRoomSlotTableMode("legacy"); got != dungeonPartyRoomSlotTableLegacy {
		t.Fatalf("legacy env mode=%v", got)
	}
	if got := readDungeonPartyRoomSlotTableMode(""); got.String() != dungeonPartyRoomSlotTableValueVendor {
		t.Fatalf("default label=%q", got.String())
	}
	if got := readPartyDungeonEntryPeerEndpointsMode(""); got != partyDungeonEntryPeerEndpointsVendor {
		t.Fatalf("empty entry env mode=%v want vendor", got)
	}
	if got := readPartyDungeonEntryPeerEndpointsMode("whatever"); got != partyDungeonEntryPeerEndpointsVendor {
		t.Fatalf("junk entry env mode=%v want vendor", got)
	}
	if got := readPartyDungeonEntryPeerEndpointsMode(" LEGACY "); got != partyDungeonEntryPeerEndpointsLegacy {
		t.Fatalf("legacy entry env mode=%v", got)
	}
	var zero Service
	var nilService *Service
	if got := readDungeonStartMapFatigueMode(""); got != dungeonStartMapFatigueVendor {
		t.Fatalf("empty start-map fatigue mode=%v want vendor", got)
	}
	if got := readDungeonStartMapFatigueMode("junk"); got != dungeonStartMapFatigueVendor {
		t.Fatalf("junk start-map fatigue mode=%v want vendor", got)
	}
	if got := readDungeonStartMapFatigueMode(" LEGACY "); got != dungeonStartMapFatigueLegacy {
		t.Fatalf("legacy start-map fatigue mode=%v", got)
	}
	if got := zero.dungeonStartMapFatigue(); got != dungeonStartMapFatigueVendor {
		t.Fatalf("zero-value service start-map fatigue=%v", got)
	}
	if got := nilService.dungeonStartMapFatigue(); got != dungeonStartMapFatigueVendor {
		t.Fatalf("nil service start-map fatigue=%v", got)
	}
	if got := zero.dungeonPartyRoomSlotTable(); got != dungeonPartyRoomSlotTableVendor {
		t.Fatalf("zero-value service slot table mode=%v", got)
	}
	if got := zero.partyDungeonEntryPeerEndpoints(); got != partyDungeonEntryPeerEndpointsVendor {
		t.Fatalf("zero-value service entry endpoints mode=%v", got)
	}
	if got := nilService.dungeonPartyRoomSlotTable(); got != dungeonPartyRoomSlotTableVendor {
		t.Fatalf("nil service slot table mode=%v", got)
	}
	if got := nilService.partyDungeonEntryPeerEndpoints(); got != partyDungeonEntryPeerEndpointsVendor {
		t.Fatalf("nil service entry endpoints mode=%v", got)
	}
	// 档位 × 触发路径：`once` 只在过图那条路径补发，`legacy` 两条都不发。
	if !dungeonPartyRoomSlotTableVendor.sendsFor(dungeonPartyRoomSlotTableTriggerEntry) ||
		!dungeonPartyRoomSlotTableVendor.sendsFor(dungeonPartyRoomSlotTableTriggerMove) {
		t.Fatal("vendor mode must send on both triggers")
	}
	if dungeonPartyRoomSlotTableOnce.sendsFor(dungeonPartyRoomSlotTableTriggerEntry) {
		t.Fatal("once mode must skip the entry trigger (the post-start-map op9 already covered that commit)")
	}
	if !dungeonPartyRoomSlotTableOnce.sendsFor(dungeonPartyRoomSlotTableTriggerMove) {
		t.Fatal("once mode must still send on the room-move trigger")
	}
	if dungeonPartyRoomSlotTableLegacy.sendsFor(dungeonPartyRoomSlotTableTriggerEntry) ||
		dungeonPartyRoomSlotTableLegacy.sendsFor(dungeonPartyRoomSlotTableTriggerMove) {
		t.Fatal("legacy mode must never send")
	}
}

// dungeonPacketOrderFixture 造一份"两人同队 + 两台会话"的最小夹具（与
// dungeon_entry_packets_test.go 的用法一致，但不跑完整个进图流程）。
func dungeonPacketOrderFixture(t *testing.T) (*Service, *gameSession, *gameSession, alignedcmd.PartyState) {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	for _, id := range []string{"99", "100"} {
		if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
			CharacterID: id,
			AccountID:   "account-1",
			Job:         atSwordmanTutorialJob,
			Level:       20,
			Stats: map[string]int64{
				"fatigue": 100, "town_id": 38, "area_id": 1,
				"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
			},
		}); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: id, Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
	}
	service := &Service{
		options:            options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		onlinePlayers:      newOnlinePlayerManager(),
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	logger, _ := newPeerEndpointTestLogger(t)
	service.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	channel := channelcatalog.Channel{ServerID: 1, ID: 42, Type: 11, Name: "ch.42", Port: 10042}
	leader := &gameSession{conn: &bufferConn{}, channel: channel, residentChannel: channel, selectedCharacterID: 99}
	follower := &gameSession{conn: &bufferConn{}, channel: channel, residentChannel: channel, selectedCharacterID: 100}
	service.bindGameSessionCharacter(leader, 99)
	service.bindGameSessionCharacter(follower, 100)
	state := alignedcmd.PartyState{
		PartyID: 99,
		UserID:  99,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 99, UserState: 1, HPPercent: 100, MPPercent: 100},
			{UserID: 100, UserState: 1, HPPercent: 100, MPPercent: 100},
		},
	}
	storeRuntimePartyState(leader, state)
	storeRuntimePartyState(follower, state)
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 99, TownID: 38, AreaID: 1, Session: leader})
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 100, TownID: 38, AreaID: 1, Session: follower})
	return service, leader, follower, state
}

func upperMsgIDSequence(t *testing.T, stream []byte) []uint16 {
	t.Helper()
	sequence := make([]uint16, 0, 16)
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacket(t, stream)
		sequence = append(sequence, packet.Header.MsgID)
		stream = rest
	}
	return sequence
}

// testPacketLogText 把缓冲刷回磁盘并读回本 Service 的包日志。
// 日志文件的关闭交给夹具注册的 `t.Cleanup`（否则 TempDir 清理会因文件仍被占用而失败）。
func testPacketLogText(t *testing.T, service *Service) string {
	t.Helper()
	if service == nil || service.packetLog == nil {
		return ""
	}
	logger := service.packetLog
	path := ""
	if logger.file != nil {
		path = logger.file.Name()
	}
	logger.mu.Lock()
	if logger.writer != nil {
		_ = logger.writer.Flush()
	}
	logger.mu.Unlock()
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func countUpperFrames(t *testing.T, stream []byte, msgID uint16, predicate func(body []byte) bool) int {
	t.Helper()
	count := 0
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacket(t, stream)
		if packet.Header.MsgID == msgID && (predicate == nil || predicate(packet.Body)) {
			count++
		}
		stream = rest
	}
	return count
}

// TestDungeonEntryPeerEndpointsRemotesMatchesFormationBuilderByteForByte 钉住
// "进场那一条 op11 与 formation 那份逐字节相同"：两者都出自
// `buildRuntimePartyPeerEndpointInfoForReceiver`，这里对同一份 state 直接比对字节。
func TestDungeonEntryPeerEndpointsRemotesMatchesFormationBuilderByteForByte(t *testing.T) {
	service, leader, _, state := dungeonPacketOrderFixture(t)
	want := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state)
	if len(want) == 0 || want[0] != 1 {
		t.Fatalf("formation op11 body=% X want one peer row", want)
	}
	sent, err := service.sendDungeonEntryPeerEndpointRemotes(leader, "test_entry")
	if err != nil {
		t.Fatal(err)
	}
	if !sent {
		t.Fatal("default mode must send the dungeon-entry op11 remotes")
	}
	frames := leader.conn.(*bufferConn).write.Bytes()
	found := 0
	for len(frames) > 0 {
		packet, rest := splitGameServerUpperPacket(t, frames)
		if packet.Header.Classification == 0 && packet.Header.MsgID == 0x000b {
			if string(packet.Body) != string(want) {
				t.Fatalf("entry op11 body=% X want formation body=% X", packet.Body, want)
			}
			found++
		}
		frames = rest
	}
	if found != 1 {
		t.Fatalf("entry op11 frames=%d want exactly 1", found)
	}
	log := testPacketLogText(t, service)
	if !strings.Contains(log, "game-party-dungeon-entry-peer-endpoints-send") {
		t.Fatalf("missing entry op11 event in log:\n%s", log)
	}
}

// TestDungeonStartMapFatiguePrecedesRoomSlotTable 钉住厂商 ③ 的收尾顺序
// `op29 → op36 → op9-room-slot-table`：fatigue 必须在房间槽位表之前。
func TestDungeonStartMapFatiguePrecedesRoomSlotTable(t *testing.T) {
	service, leader, _, _ := dungeonPacketOrderFixture(t)
	if err := service.sendDungeonStartMapFatigue(leader, "test_move"); err != nil {
		t.Fatal(err)
	}
	if err := service.sendDungeonPartyRoomSlotTable(
		leader,
		nil,
		worldmap.DungeonRoomScene{},
		dungeonPartyRoomSlotTableTriggerMove,
		"test_move",
	); err != nil {
		t.Fatal(err)
	}
	frames := leader.conn.(*bufferConn).write.Bytes()
	first, rest := splitGameServerUpperPacket(t, frames)
	if first.Header.Classification != 0 || first.Header.MsgID != currentFatigueMsgID || len(first.Body) != 10 {
		t.Fatalf("first frame=%+v body=% X want class0/op36 10B", first.Header, first.Body)
	}
	second, trailing := splitGameServerUpperPacket(t, rest)
	if second.Header.Classification != 0 ||
		second.Header.MsgID != uint16(dnfenum.CmdPacketRecoverStamina) ||
		!isDungeonRoomSlotTableBody(second.Body) {
		t.Fatalf("second frame=%+v body=% X want the room slot table op9", second.Header, second.Body)
	}
	if len(trailing) != 0 {
		t.Fatalf("trailing frames=%x", trailing)
	}
	log := testPacketLogText(t, service)
	for _, want := range []string{
		"game-party-dungeon-start-map-fatigue-send",
		"position=op29_then_op36_then_op9_room_slot_table",
		"fatigue_used=",
		"fatigue_limit=",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("start-map fatigue log missing %q:\n%s", want, log)
		}
	}
}

// TestDungeonStartMapFatigueLegacySendsNothing：`legacy` 档回到"op29 之后不发 op36"。
func TestDungeonStartMapFatigueLegacySendsNothing(t *testing.T) {
	service, leader, _, _ := dungeonPacketOrderFixture(t)
	service.options.dungeonStartMapFatigue = dungeonStartMapFatigueLegacy
	if err := service.sendDungeonStartMapFatigue(leader, "test_move_legacy"); err != nil {
		t.Fatal(err)
	}
	if got := countUpperFrames(t, leader.conn.(*bufferConn).write.Bytes(), currentFatigueMsgID, nil); got != 0 {
		t.Fatalf("legacy mode sent %d fatigue frames, want zero", got)
	}
}

// TestDungeonEntryPeerEndpointsLegacySendsNothing：`legacy` 档逐字节回到"进场不发 op11"。
func TestDungeonEntryPeerEndpointsLegacySendsNothing(t *testing.T) {
	service, leader, _, _ := dungeonPacketOrderFixture(t)
	service.options.partyDungeonEntryPeerEndpoints = partyDungeonEntryPeerEndpointsLegacy
	sent, err := service.sendDungeonEntryPeerEndpointRemotes(leader, "test_entry_legacy")
	if err != nil {
		t.Fatal(err)
	}
	if sent {
		t.Fatal("legacy mode must not send the dungeon-entry op11 remotes")
	}
	if got := len(leader.conn.(*bufferConn).write.Bytes()); got != 0 {
		t.Fatalf("legacy mode wrote %d bytes, want zero", got)
	}
}

// TestDungeonRoomSlotTableVendorShapeOnLivePath：默认档在真实服务路径上发出的那一条 op9
// 必须是厂商形状（16B 行 / 行尾 00 64 01 / host_slot=0 / 只有对端与自己的两行）。
func TestDungeonRoomSlotTableVendorShapeOnLivePath(t *testing.T) {
	service, leader, _, _ := dungeonPacketOrderFixture(t)
	if err := service.sendDungeonPartyRoomSlotTable(
		leader,
		nil,
		worldmap.DungeonRoomScene{},
		dungeonPartyRoomSlotTableTriggerMove,
		"test_move",
	); err != nil {
		t.Fatal(err)
	}
	frames := leader.conn.(*bufferConn).write.Bytes()
	packet, rest := splitGameServerUpperPacket(t, frames)
	if len(rest) != 0 {
		t.Fatalf("unexpected trailing frames=%x", rest)
	}
	if packet.Header.Classification != 0 ||
		packet.Header.MsgID != uint16(dnfenum.CmdPacketRecoverStamina) {
		t.Fatalf("room slot table header=%+v want class0/op9", packet.Header)
	}
	if !isDungeonRoomSlotTableBody(packet.Body) {
		t.Fatalf("room slot table missing vendor row tail body=% X", packet.Body)
	}
	actorNameLen := int(binary.LittleEndian.Uint32(packet.Body[12:16]))
	countOffset := 16 + actorNameLen + 13
	if packet.Body[countOffset] != 2 {
		t.Fatalf("row_count=%d want 2 body=% X", packet.Body[countOffset], packet.Body)
	}
	if binary.LittleEndian.Uint16(packet.Body[4:6]) != currentSceneActorObjectKey(99) {
		t.Fatalf("object_key=%d want=%d", binary.LittleEndian.Uint16(packet.Body[4:6]), currentSceneActorObjectKey(99))
	}
	log := testPacketLogText(t, service)
	for _, want := range []string{
		"game-party-dungeon-room-slot-table-send",
		"source=party_start_room_after_op29_actors",
		"row_count=2",
		"host_slot=0",
		"row_hp_percent=100",
		"row_room_flag=1",
		"actor_aux=4",
		"packet_order=all-members-through-op28-then-op800-all-op29-optional-trap-event-op312-ff-op36-op9-room-slot-table",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("room slot table log missing %q:\n%s", want, log)
		}
	}
}

// TestDungeonRoomSlotTableSwitchMatrixDoesNotPanic：3 档 × 2 条触发路径 × 组队/单人
// 共 12 组，全部不许 panic；发帧数**按硬编码的期望表**核对（不从被测函数推导，
// 否则改坏 `sendsFor` 时矩阵会跟着一起"自洽"地通过）。
func TestDungeonRoomSlotTableSwitchMatrixDoesNotPanic(t *testing.T) {
	cases := []struct {
		mode    dungeonPartyRoomSlotTableMode
		trigger dungeonPartyRoomSlotTableTrigger
		party   bool
		want    int
	}{
		{dungeonPartyRoomSlotTableVendor, dungeonPartyRoomSlotTableTriggerEntry, true, 1},
		{dungeonPartyRoomSlotTableVendor, dungeonPartyRoomSlotTableTriggerMove, true, 1},
		{dungeonPartyRoomSlotTableVendor, dungeonPartyRoomSlotTableTriggerEntry, false, 0},
		{dungeonPartyRoomSlotTableVendor, dungeonPartyRoomSlotTableTriggerMove, false, 0},
		{dungeonPartyRoomSlotTableOnce, dungeonPartyRoomSlotTableTriggerEntry, true, 0},
		{dungeonPartyRoomSlotTableOnce, dungeonPartyRoomSlotTableTriggerMove, true, 1},
		{dungeonPartyRoomSlotTableOnce, dungeonPartyRoomSlotTableTriggerEntry, false, 0},
		{dungeonPartyRoomSlotTableOnce, dungeonPartyRoomSlotTableTriggerMove, false, 0},
		{dungeonPartyRoomSlotTableLegacy, dungeonPartyRoomSlotTableTriggerEntry, true, 0},
		{dungeonPartyRoomSlotTableLegacy, dungeonPartyRoomSlotTableTriggerMove, true, 0},
		{dungeonPartyRoomSlotTableLegacy, dungeonPartyRoomSlotTableTriggerEntry, false, 0},
		{dungeonPartyRoomSlotTableLegacy, dungeonPartyRoomSlotTableTriggerMove, false, 0},
	}
	for _, test := range cases {
		service, leader, _, _ := dungeonPacketOrderFixture(t)
		if !test.party {
			storeRuntimePartyState(leader, alignedcmd.PartyState{})
		}
		service.options.dungeonPartyRoomSlotTable = test.mode
		if err := service.sendDungeonPartyRoomSlotTable(
			leader,
			nil,
			worldmap.DungeonRoomScene{},
			test.trigger,
			"test_matrix",
		); err != nil {
			t.Fatalf("mode=%s trigger=%s party=%t: %v", test.mode, test.trigger, test.party, err)
		}
		got := countUpperFrames(t, leader.conn.(*bufferConn).write.Bytes(), uint16(dnfenum.CmdPacketRecoverStamina), isDungeonRoomSlotTableBody)
		if got != test.want {
			t.Fatalf("mode=%s trigger=%s party=%t frames=%d want=%d", test.mode, test.trigger, test.party, got, test.want)
		}
	}
}

// TestDungeonEntrySequenceSendsOp11RemotesRightAfterOp2Mode1：在**真实进图流程**里，
// 新补的那条 op11 必须落在 op2 mode1-all 之后（厂商 packet_order ① 的位置），
// 并且日志里出现与厂商同名的 `game-party-dungeon-entry-prepared` 事件。
func TestDungeonEntrySequenceSendsOp11RemotesRightAfterOp2Mode1(t *testing.T) {
	source := bridgeDungeonPVF(false)
	source["dungeon/test.dgn"] = strings.Replace(source["dungeon/test.dgn"], "[limit party count]\n1\n", "[limit party count]\n4\n", 1)
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	service, leader, follower, _ := dungeonPacketOrderFixture(t)
	service.worldMapTable = table
	service.worldMapResolver = resolver
	service.dungeonMonsterTable = monsters
	service.dungeonChoice = func(int) (int, error) { return 0, nil }
	service.dungeonSeed = func() (uint32, error) { return 0x12345678, nil }
	bindDungeonSelectorOriginForTestAt(t, service, leader, 38, 1, 450, 234)
	bindDungeonSelectorOriginForTestAt(t, service, follower, 38, 1, 450, 234)

	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body[:4], 700)
	if err := service.handleDungeonSelectUpper(leader, body); err != nil {
		t.Fatal(err)
	}
	stream := leader.conn.(*bufferConn).write.Bytes()
	op11Index := -1
	mode1Index := -1
	index := 0
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacket(t, stream)
		switch {
		case packet.Header.Classification == 0 && packet.Header.MsgID == 0x000b:
			if op11Index < 0 {
				op11Index = index
			}
		case packet.Header.Classification == 0 &&
			packet.Header.MsgID == uint16(dnfenum.CmdPacketSetUDPIPPort) &&
			len(packet.Body) > 0 && packet.Body[0] == 1:
			if mode1Index < 0 {
				mode1Index = index
			}
		}
		index++
		stream = rest
	}
	if op11Index < 0 {
		t.Fatal("dungeon entry did not send the op11 remotes frame")
	}
	if mode1Index < 0 {
		t.Fatal("dungeon entry did not send the op2 mode1 actor-binding frame")
	}
	if mode1Index > op11Index {
		t.Fatalf("entry op11 index=%d is before op2 mode1 index=%d, want after", op11Index, mode1Index)
	}
	log := testPacketLogText(t, service)
	for _, want := range []string{
		"game-party-dungeon-entry-peer-endpoints-send",
		"op2_mode1_all_then_op11_remotes_then_op27",
		"game-party-dungeon-entry-prepared",
		"release=false",
		"vendor_packet_order=op16-owner-ack-op5-op23-ff-op2-mode0-all-op2-mode1-all-op11-remotes-op3-all-op26-leader-op28-then-all-member-barrier",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("dungeon entry log missing %q:\n%s", want, log)
		}
	}
}

// TestDungeonEntryPeerEndpointsLegacyKeepsEntryFrameOrder：`legacy` 档下整条进图序列
// 相对默认档**只少那一条 op11**（其余 msg_id 序列逐项相同）——即"逐字节回退"的可判据。
func TestDungeonEntryPeerEndpointsLegacyKeepsEntryFrameOrder(t *testing.T) {
	run := func(mode partyDungeonEntryPeerEndpointsMode) []uint16 {
		source := bridgeDungeonPVF(false)
		source["dungeon/test.dgn"] = strings.Replace(source["dungeon/test.dgn"], "[limit party count]\n1\n", "[limit party count]\n4\n", 1)
		table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
		service, leader, follower, _ := dungeonPacketOrderFixture(t)
		service.worldMapTable = table
		service.worldMapResolver = resolver
		service.dungeonMonsterTable = monsters
		service.dungeonChoice = func(int) (int, error) { return 0, nil }
		service.dungeonSeed = func() (uint32, error) { return 0x12345678, nil }
		service.options.partyDungeonEntryPeerEndpoints = mode
		bindDungeonSelectorOriginForTestAt(t, service, leader, 38, 1, 450, 234)
		bindDungeonSelectorOriginForTestAt(t, service, follower, 38, 1, 450, 234)
		body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
		binary.LittleEndian.PutUint32(body[:4], 700)
		if err := service.handleDungeonSelectUpper(leader, body); err != nil {
			t.Fatal(err)
		}
		return upperMsgIDSequence(t, leader.conn.(*bufferConn).write.Bytes())
	}
	vendor := run(partyDungeonEntryPeerEndpointsVendor)
	legacy := run(partyDungeonEntryPeerEndpointsLegacy)
	if len(vendor) != len(legacy)+1 {
		t.Fatalf("vendor frames=%d legacy frames=%d want exactly one extra op11\nvendor=%v\nlegacy=%v",
			len(vendor), len(legacy), vendor, legacy)
	}
	// 把 vendor 序列里那一条 op11 去掉后，必须与 legacy 序列逐项相同。
	trimmed := make([]uint16, 0, len(vendor))
	removed := 0
	for _, msgID := range vendor {
		if msgID == 0x000b && removed == 0 {
			removed++
			continue
		}
		trimmed = append(trimmed, msgID)
	}
	if len(trimmed) != len(legacy) {
		t.Fatalf("trimmed vendor frames=%d legacy=%d", len(trimmed), len(legacy))
	}
	for index := range legacy {
		if trimmed[index] != legacy[index] {
			t.Fatalf("frame[%d] vendor=%d legacy=%d\nvendor=%v\nlegacy=%v",
				index, trimmed[index], legacy[index], vendor, legacy)
		}
	}
}
