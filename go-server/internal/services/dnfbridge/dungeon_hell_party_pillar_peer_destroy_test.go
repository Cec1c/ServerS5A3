package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件钉住操作者缺陷「**组队挑战深渊 队员的深渊柱子异常：怪物出现了、柱子没碎、
// 也没有掉落碎片**」（`dungeon_hell_party_wave_peer_notify.go`）。
//
// 上一轮那条队友投影**确实生效**（现场 `conn_id=game-000031` 有 5 条
// `game-dungeon-hell-party-wave-peer-notify … body_len=7 drop_rows_stripped=true`），
// 但操作者报的两件事都还在：
//
//	① 柱子（map-owned `[special passive object]`，真档 30528）**从来没有被投影过**
//	   ⇒ 队员屏幕上柱子一直站着（队长那一侧是本地打碎的，客户端从不上报）；
//	② 掉落行被剥掉了（击杀方 1150 字节 / 队友 7 字节）⇒ 队员看不到地上的碎片，
//	   而地面掉落行正是客户端知道"地上有东西"的唯一途径。

// partyAbyssPillarFixture 造一对同队会话（队长 char 99 / 队友 char 43），共享同一个
// 深渊房间运行时；深渊波次的奖励与保底碎片都打开（这样可以断言"掉落行也投影过去了"）。
func partyAbyssPillarFixture(
	t *testing.T,
) (*Service, *gameSession, *gameSession, *bufferConn, *bufferConn, *runtimeDungeonState, worldmap.DungeonRoomScene, *runtimeDungeonState) {
	t.Helper()
	service, leader, leaderConn, leaderRuntime, scene, _ := prepareAbyssEpicFragmentRuntime(
		t, bridgeAbyssEpicFragmentPVF(),
	)
	state := alignedcmd.PartyState{
		PartyID:    3,
		UserID:     1001,
		MaxMembers: 4,
		UserState:  1,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 1001, UserState: 1},
			{UserID: 1002, UserState: 1},
		},
	}
	storeRuntimePartyState(leader, state)
	followerConn := &bufferConn{}
	follower := &gameSession{conn: followerConn, connID: "abyss-pillar-peer-test", selectedCharacterID: 43}
	storeRuntimePartyState(follower, state)
	followerRuntime := &runtimeDungeonState{
		Dungeon:       leaderRuntime.Dungeon,
		MazeIndex:     leaderRuntime.MazeIndex,
		Seed:          leaderRuntime.Seed,
		Room:          leaderRuntime.Room,
		NextObjectKey: leaderRuntime.NextObjectKey,
		Rooms:         make(map[runtimeDungeonRoomKey]*runtimeDungeonRoomVisit),
	}
	if instance := service.adoptPartyDungeonInstance(leader, leaderRuntime, scene); instance == nil {
		t.Fatal("party dungeon instance was not published")
	}
	if instance := service.adoptPartyDungeonInstance(follower, followerRuntime, scene); instance == nil {
		t.Fatal("the second member did not join the party dungeon instance")
	}
	return service, leader, follower, leaderConn, followerConn, leaderRuntime, scene, followerRuntime
}

// abyssPillarFromScene 取场景里唯一那根柱子（`[special passive object]` + 行内 `[hellparty]`）。
func abyssPillarFromScene(
	t *testing.T,
	scene worldmap.DungeonRoomScene,
) currentDungeonMapOwnedPassiveObject {
	t.Helper()
	registration := &currentDungeonMapOwnedPassiveObjectRegistration{
		Objects: currentDungeonMapOwnedPassiveObjects(scene),
	}
	pillar, found := registration.hellPillarObject()
	if !found {
		t.Fatalf("the fixture hell room has no unique pillar: %+v", registration.Objects)
	}
	return pillar
}

// abyssOp38Bodies 按真实下行字节流切出每一条 op38 的 body。
func abyssOp38Bodies(t *testing.T, data []byte) [][]byte {
	t.Helper()
	bodies := make([][]byte, 0, 8)
	rest := data
	for len(rest) != 0 {
		var packetBody []byte
		var msgID uint16
		msgID, packetBody, rest = abyssSplitOne(t, rest)
		if msgID != currentDungeonNotificationOpcodeForTest() {
			continue
		}
		bodies = append(bodies, packetBody)
	}
	return bodies
}

func abyssSplitOne(t *testing.T, data []byte) (uint16, []byte, []byte) {
	t.Helper()
	packet, rest := splitGameServerUpperPacket(t, data)
	return packet.Header.MsgID, append([]byte(nil), packet.Body...), rest
}

func currentDungeonNotificationOpcodeForTest() uint16 {
	return uint16(38)
}

func abyssOp38Key(body []byte) uint16 {
	if len(body) < 2 {
		return 0
	}
	return binary.LittleEndian.Uint16(body[0:2])
}

func abyssOp38DropRows(body []byte) int {
	if len(body) < 3 {
		return 0
	}
	return int(body[2])
}

// abyssDropRowOwnerOffset 是 op38 掉落行里 `OwnerActorObjectKey` 的偏移
// （行布局 = u32 scene key | raw[0x77] item | u16 sentinel | u16 owner，见
// `dungeon_death_response.go:87-92` 的 writer；行宽 = `currentDungeonDeathDropWireSize`）。
func abyssDropRowOwnerOffset(index int) int {
	return 3 + index*currentDungeonDeathDropWireSize + currentDungeonDeathDropWireSize - 2
}

// assertAbyssIndependentDropFrames 钉住**独立掉落**的帧契约（默认档）：
// 两帧行数相同、每行所有者分别是 `first` / `second`、**对象 key 集合完全不相交**
// （= 各是各的地面对象，不是同一条被改写所有者）。内容由各自投骰决定。
func assertAbyssIndependentDropFrames(
	t *testing.T,
	first []byte,
	second []byte,
	firstOwner uint16,
	secondOwner uint16,
) {
	t.Helper()
	firstRows := abyssEpicFragmentDropRows(t, first)
	secondRows := abyssEpicFragmentDropRows(t, second)
	if len(firstRows) == 0 || len(firstRows) != len(secondRows) {
		t.Fatalf("independent drops must be one set per member: rows %d vs %d", len(firstRows), len(secondRows))
	}
	firstKeys := make(map[uint32]struct{}, len(firstRows))
	for index, row := range firstRows {
		if owner := binary.LittleEndian.Uint16(first[abyssDropRowOwnerOffset(index):]); owner != firstOwner {
			t.Fatalf("row#%d of the first frame owner=%d want %d", index, owner, firstOwner)
		}
		firstKeys[row.ObjectKey] = struct{}{}
	}
	for index, row := range secondRows {
		if owner := binary.LittleEndian.Uint16(second[abyssDropRowOwnerOffset(index):]); owner != secondOwner {
			t.Fatalf("row#%d of the second frame owner=%d want %d", index, owner, secondOwner)
		}
		if _, shared := firstKeys[row.ObjectKey]; shared {
			t.Fatalf("independent drops must not share a ground object: key=%d", row.ObjectKey)
		}
	}
}

// assertAbyssDropRowsDifferOnlyByOwner 断言两帧**同长、逐行同内容**，唯一差异是
// 每一行末尾的所有者字段（wantOwner 对 first / otherOwner 对 second）。
// 这是第 4 次发版那一档（`…PARTY_DROP_OWNER=shared`）的契约。
func assertAbyssDropRowsDifferOnlyByOwner(
	t *testing.T,
	first []byte,
	second []byte,
	wantOwner uint16,
	otherOwner uint16,
) {
	t.Helper()
	if len(first) != len(second) {
		t.Fatalf("frame lengths differ: %d vs %d", len(first), len(second))
	}
	count := abyssOp38DropRows(first)
	if count == 0 || count != abyssOp38DropRows(second) {
		t.Fatalf("drop row counts differ: %d vs %d", count, abyssOp38DropRows(second))
	}
	for index := 0; index < count; index++ {
		offset := abyssDropRowOwnerOffset(index)
		if binary.LittleEndian.Uint16(first[offset:offset+2]) != wantOwner {
			t.Fatalf("row#%d owner=%d want %d", index,
				binary.LittleEndian.Uint16(first[offset:offset+2]), wantOwner)
		}
		if binary.LittleEndian.Uint16(second[offset:offset+2]) != otherOwner {
			t.Fatalf("row#%d owner=%d want %d", index,
				binary.LittleEndian.Uint16(second[offset:offset+2]), otherOwner)
		}
		// 除这 2 字节外，整行必须逐字节相同 —— 物品、数量、scene key 一个都不许变。
		for cursor := offset - (currentDungeonDeathDropWireSize - 2); cursor < offset; cursor++ {
			if first[cursor] != second[cursor] {
				t.Fatalf("row#%d byte %d differs: %02x vs %02x", index, cursor, first[cursor], second[cursor])
			}
		}
	}
}

// TestHellPartyPillarAndDropsAreProjectedToThePartyMember 是缺陷 (3) 的主用例：
// 队长清完一波（含最后一波）之后，队友那一侧必须
//   - 收到**与击杀方逐字节同一份**的波次怪退役 op38（含全部掉落行 ⇒ 地上看得见）；
//   - 收到**恰好一帧**柱子破坏投影（key = 柱子的对象 code、零掉落行、每房间一次）。
func TestHellPartyPillarAndDropsAreProjectedToThePartyMember(t *testing.T) {
	logger, logPath := testPassiveObjectDropPacketLogger(t)
	service, leader, _, leaderConn, followerConn, runtime, scene, _ := partyAbyssPillarFixture(t)
	service.packetLog = logger
	pillar := abyssPillarFromScene(t, scene)
	waves := waveHellPartyActorKeys(t, runtime)
	_, lastGroup := hellPartyRewardActorKeys(t, runtime)
	finalWave := lastGroup[len(lastGroup)-1]

	leaderConn.write.Reset()
	followerConn.write.Reset()
	killDungeonActors(t, service, leader, waves...)

	leaderBodies := abyssOp38Bodies(t, leaderConn.write.Bytes())
	followerBodies := abyssOp38Bodies(t, followerConn.write.Bytes())

	// 击杀方那一份：最后一波最后一只带着碎片掉落行。
	var leaderFinal []byte
	for _, body := range leaderBodies {
		if abyssOp38Key(body) == uint16(finalWave) {
			leaderFinal = body
		}
	}
	if leaderFinal == nil || abyssOp38DropRows(leaderFinal) == 0 {
		t.Fatalf("the killer's final-wave frame must carry drop rows: bodies=%d", len(leaderBodies))
	}

	// 队友那一份：同一 key 的那一帧必须**逐字节相同**（掉落行也共享）。
	var followerFinal []byte
	pillarFrames := 0
	for _, body := range followerBodies {
		switch abyssOp38Key(body) {
		case uint16(finalWave):
			followerFinal = body
		case uint16(pillar.ObjectCode):
			pillarFrames++
			if abyssOp38DropRows(body) != 0 {
				t.Fatalf("the pillar projection must carry no drop rows: %x", body)
			}
			want, err := buildCurrentDungeonDeathNotificationBodyWithDrops(
				uint32(pillar.ObjectCode), currentDungeonDeathResponseMonster, nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(body, want) {
				t.Fatalf("pillar projection body=%x want %x", body, want)
			}
		}
	}
	if followerFinal == nil {
		t.Fatalf("the partner never saw the final wave retirement: %d frames", len(followerBodies))
	}
	// **独立掉落**（默认档）：两名成员各拿**属于自己的那一份** ——
	// 行数相同、每行所有者各是自己、**对象 key 完全不同**（各是各的地面对象）。
	assertAbyssIndependentDropFrames(
		t,
		followerFinal,
		leaderFinal,
		currentSceneActorObjectKey(43),
		currentSceneActorObjectKey(99),
	)
	if len(followerFinal) == currentDungeonZeroDropDeathBodySize {
		t.Fatal("the shipped tier must not strip the drop rows")
	}
	// 每一只波次怪一份 + 恰好一帧柱子。
	if want := len(waves) + 1; len(followerBodies) != want {
		t.Fatalf("partner frames=%d want %d (one per wave monster plus exactly one pillar frame)",
			len(followerBodies), want)
	}
	if pillarFrames != 1 {
		t.Fatalf("the partner must receive exactly one pillar frame, got %d", pillarFrames)
	}
	// 幂等：再退一只（重复 op39）不得再补一帧柱子。
	followerConn.write.Reset()
	killDungeonActors(t, service, leader, waves[0])
	for _, body := range abyssOp38Bodies(t, followerConn.write.Bytes()) {
		if abyssOp38Key(body) == uint16(pillar.ObjectCode) {
			t.Fatal("the pillar projection must fire once per room")
		}
	}

	logged := closePassiveObjectDropPacketLogger(t, logger, logPath)
	for _, field := range []string{
		"kind=game-dungeon-hell-party-pillar-peer-destroy",
		"actor=map_owned_special_passive_object_with_inline_hellparty",
		"destroy_state=destroyed_on_the_killer_client_projected_to_the_peer",
		"received_by=43",
		"object_id=" + zzItoa(pillar.ObjectCode),
		"trigger=all_hell_party_wave_monsters_retired_in_this_room",
	} {
		if !strings.Contains(logged, field) {
			t.Fatalf("the pillar projection event is missing %q:\n%s", field, logged)
		}
	}
	if !strings.Contains(logged, "drop_rows_shared=true") {
		t.Fatalf("the wave projection must document that the drop rows are shared:\n%s", logged)
	}
}

func zzItoa(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	digits := make([]byte, 0, 8)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

// TestHellPartyPeerDropRowsRollbackLiteralStripsThemAgain 钉住掉落行那一档的回退：
// `DNFBRIDGE_HELL_PARTY_WAVE_PEER_DROPS=stripped` 逐字节复现上一轮那版（7 字节、零掉落行）。
func TestHellPartyPeerDropRowsRollbackLiteralStripsThemAgain(t *testing.T) {
	t.Setenv(dungeonHellPartyWavePeerDropsEnv, dungeonHellPartyWavePeerDropsStrippedValue)
	service, leader, _, leaderConn, followerConn, runtime, _, _ := partyAbyssPillarFixture(t)
	waves := waveHellPartyActorKeys(t, runtime)
	_, lastGroup := hellPartyRewardActorKeys(t, runtime)
	finalWave := lastGroup[len(lastGroup)-1]

	leaderConn.write.Reset()
	followerConn.write.Reset()
	killDungeonActors(t, service, leader, waves...)

	for _, body := range abyssOp38Bodies(t, followerConn.write.Bytes()) {
		if abyssOp38Key(body) != uint16(finalWave) {
			continue
		}
		if len(body) != currentDungeonZeroDropDeathBodySize {
			t.Fatalf("the stripped tier must send the 7-byte zero-drop body, got %x", body)
		}
		return
	}
	t.Fatal("the partner never received the final wave frame")
}

// TestHellPartyPillarProjectionRollbackLiteralSendsNothing 钉住柱子那一档的回退：
// `DNFBRIDGE_HELL_PARTY_PILLAR_PEER_DESTROY=legacy` 下队友收不到柱子帧（= 今天的线上行为）。
func TestHellPartyPillarProjectionRollbackLiteralSendsNothing(t *testing.T) {
	t.Setenv(dungeonHellPartyPillarPeerEnv, dungeonHellPartyPillarPeerLegacyValue)
	service, leader, _, leaderConn, followerConn, runtime, scene, _ := partyAbyssPillarFixture(t)
	pillar := abyssPillarFromScene(t, scene)
	waves := waveHellPartyActorKeys(t, runtime)

	leaderConn.write.Reset()
	followerConn.write.Reset()
	killDungeonActors(t, service, leader, waves...)

	for _, body := range abyssOp38Bodies(t, followerConn.write.Bytes()) {
		if abyssOp38Key(body) == uint16(pillar.ObjectCode) {
			t.Fatalf("legacy mode must not project the pillar: %x", body)
		}
	}
	// 波次怪的投影不受影响（它是另一档）。
	if len(abyssOp38Bodies(t, followerConn.write.Bytes())) == 0 {
		t.Fatal("the wave retirement projection must still fire")
	}
}

// TestHellPartyPillarProjectionRollsBackWithTheWaveNotifySwitch 钉住两档的从属关系：
// `DNFBRIDGE_HELL_PARTY_WAVE_PEER_NOTIFY=legacy` 的既有契约是"一条投影都不发"
// （逐字节等于上一轮修复前），柱子这一帧也从属于它。
func TestHellPartyPillarProjectionRollsBackWithTheWaveNotifySwitch(t *testing.T) {
	t.Setenv(dungeonHellPartyWavePeerNotifyEnv, dungeonHellPartyWavePeerNotifyLegacyValue)
	service, leader, _, leaderConn, followerConn, runtime, _, _ := partyAbyssPillarFixture(t)
	waves := waveHellPartyActorKeys(t, runtime)

	leaderConn.write.Reset()
	followerConn.write.Reset()
	killDungeonActors(t, service, leader, waves...)

	if len(followerConn.write.Bytes()) != 0 {
		t.Fatalf("the wave-notify rollback must silence every peer projection: %x", followerConn.write.Bytes())
	}
}

// TestHellPartyPillarProjectionNeedsThePartyInstance 单人（无队伍副本实例）下两条投影
// 都是零字节副作用：柱子的 op38 只会发给"同一实例内的其它会话"。
func TestHellPartyPillarProjectionNeedsThePartyInstance(t *testing.T) {
	service, session, conn, runtime, scene := prepareHellPartyClearSettlementRuntime(t)
	if runtime.partyInstance != nil {
		t.Fatal("this fixture must be solo")
	}
	pillar := abyssPillarFromScene(t, scene)
	conn.write.Reset()
	if delivered := service.projectCurrentHellPartyPillarDestructionToPartyPeersLocked(
		session, runtime, scene, currentDungeonDeathResponseMonster,
	); delivered != 0 {
		t.Fatalf("a solo session must not project the pillar anywhere: delivered=%d", delivered)
	}
	if len(conn.write.Bytes()) != 0 {
		t.Fatalf("a solo session must not receive the pillar frame: %x", conn.write.Bytes())
	}
	_ = pillar
}
