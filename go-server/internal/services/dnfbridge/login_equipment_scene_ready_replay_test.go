package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"strings"
	"testing"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// liveSceneReadyEquipmentRow31 是 2026-09-28T14:31:03.790159876Z 现场
// `game-upper-selected-userinfo-mode1-equipment-create-rows`（conn game-000011，角色 6）
// 那份 row_summary 逐条抄下来的"31 个已穿戴槽"：槽位、物品、以及该行**携带的耐久**。
// 同一批槽位/物品数也出现在 14:25:08 的登录引导事件里
// （`game-login-equipment-container-sent ... list=3 rows=31 body_len=3692`）。
//
//	0:42601(d4) 1:42603(d4) 2:42605(d2) 3:42609(d47) 4:42611(d11) 5:42615(d0)
//	6:42607(d2) 7:42613(d5) 8:101580204(d0) 9:101590068(d0) 10:101600023(d0)
//	11:101590023(d0) 12:101040493(d48, 武器) 13:400330106(d0) 14:100070551(d38)
//	15:100170519(d30) 16:100120544(d32) 17:100270515(d25) 18:100220519(d25)
//	19..23(d0) 25..29(d0) 30:2747069(d65535) 32:100380058(d0)
type liveSceneReadyEquipmentRow31 struct {
	slot       int16
	itemID     int64
	durability uint16
}

func liveCharacter6EquipmentRows31() []liveSceneReadyEquipmentRow31 {
	return []liveSceneReadyEquipmentRow31{
		{0, 42601, 4}, {1, 42603, 4}, {2, 42605, 2}, {3, 42609, 47}, {4, 42611, 11},
		{5, 42615, 0}, {6, 42607, 2}, {7, 42613, 5}, {8, 101580204, 0}, {9, 101590068, 0},
		{10, 101600023, 0}, {11, 101590023, 0}, {12, 101040493, 48}, {13, 400330106, 0},
		{14, 100070551, 38}, {15, 100170519, 30}, {16, 100120544, 32}, {17, 100270515, 25},
		{18, 100220519, 25}, {19, 400300070, 0}, {20, 400310070, 0}, {21, 400320074, 0},
		{22, 100344527, 0}, {23, 100352839, 0}, {25, 100390011, 0}, {26, 400990170, 0},
		{27, 10006783, 0}, {28, 10006784, 0}, {29, 10006785, 0}, {30, 2747069, 65535},
		{32, 100380058, 0},
	}
}

// seedLiveCharacter631WornSlots 建出线上那一版形状：31 条已穿戴行，每行 raw 0x77 行的
// +0x0B 就是该行携带的耐久（与数据库 `dnf_equipment_entries.raw_entry` 同口径：
// 槽 12 的 raw +0x0B = 0x0030 = 48）。
func seedLiveCharacter631WornSlots(t *testing.T, repositories dnfrepo.Group) {
	t.Helper()
	ctx := context.Background()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "6",
		AccountID:   defaultAccountPrefix + "10011",
		Name:        "test78",
		Level:       90,
		Stats:       map[string]int64{"gold": 1000000},
	}); err != nil {
		t.Fatal(err)
	}
	rows := liveCharacter6EquipmentRows31()
	entries := make(map[string]dnfrepo.EquipmentEntry, len(rows))
	for _, row := range rows {
		raw := make([]byte, currentItemListEntryWireSize)
		binary.LittleEndian.PutUint16(raw[0x00:0x02], uint16(row.slot))
		binary.LittleEndian.PutUint32(raw[0x02:0x06], uint32(row.itemID))
		binary.LittleEndian.PutUint32(raw[0x06:0x0A], 999999998)
		raw[0x0A] = 15
		binary.LittleEndian.PutUint16(raw[0x0B:0x0D], row.durability)
		entries[strconv.Itoa(int(row.slot))] = dnfrepo.EquipmentEntry{
			SlotIndex: row.slot,
			ItemID:    row.itemID,
			RawEntry:  raw,
			// 线上每一行的 currentMode1EquipmentType 都等于槽号
			// （日志 `12:101040493:type=12`），行投影因此稳定。
			Extra: map[string]string{
				"current_exe_equipment_type": strconv.Itoa(int(row.slot)),
			},
		}
	}
	if err := repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "6",
		Entries:     entries,
	}); err != nil {
		t.Fatal(err)
	}
}

// newSceneReadyReplayFixture 造一份"角色 6 / 31 个已穿戴槽"的仓储 + service + session，
// 并给 service 挂一个可读回的包日志（事件断言要用）。
func newSceneReadyReplayFixture(t *testing.T) (*Service, *gameSession, *bufferConn, string) {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedLiveCharacter631WornSlots(t, repositories)
	service := newLoginEquipmentTestService(repositories)
	logger, logPath := newPeerEndpointTestLogger(t)
	service.packetLog = logger
	t.Cleanup(func() { closeSceneReadyReplayLogger(service) })
	connection := &bufferConn{}
	session := newLoginEquipmentTestSession(connection, 6)
	return service, session, connection, logPath
}

// closeSceneReadyReplayLogger 关闭并摘掉包日志；可重复调用（Cleanup 与读取互不打架）。
func closeSceneReadyReplayLogger(service *Service) {
	service.mu.Lock()
	logger := service.packetLog
	service.packetLog = nil
	service.mu.Unlock()
	if logger != nil {
		_ = logger.Close()
	}
}

func sceneReadyReplayTestLog(t *testing.T, service *Service, logPath string) string {
	t.Helper()
	closeSceneReadyReplayLogger(service)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// armLoginRouteUpToOp24 把会话摆到"七容器引导已发、客户端还没收到 typed op24"那一刻：
// 路由已 actor-bound，但还没提交场景。
func armLoginRouteUpToOp24(session *gameSession) {
	session.initialTownRouteCharacterID = session.selectedCharacterID
	session.initialTownRouteStage = currentInitialTownRouteActorBound
	session.initialTownLegacySceneReadyAccepted = false
	session.sceneBootstrapTailSent = true
	session.sceneBootstrapTailDeferred = false
	session.initialTownAdventureOverheadRefreshSent = true
	session.initialTownCombatPowerAffixesSent = true
}

// sceneReadyReplayEquipmentContainers 从一条下行流里挑出**装备容器**（class0/op13
// list 3），返回它们的字节偏移、整包字节与包体。
func sceneReadyReplayEquipmentContainers(t *testing.T, stream []byte) (offsets []int, packets [][]byte, bodies [][]byte) {
	t.Helper()
	offset := 0
	for len(stream) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
		rawLen := len(stream) - len(rest)
		if packet.Header.Classification == 0 &&
			packet.Header.MsgID == uint16(dnfenum.CmdPacketLeaveParty) &&
			len(packet.Body) >= 3 && packet.Body[0] == currentSocketListEquipment {
			offsets = append(offsets, offset)
			packets = append(packets, append([]byte(nil), stream[:rawLen]...))
			bodies = append(bodies, append([]byte(nil), packet.Body...))
		}
		offset += rawLen
		stream = rest
	}
	return offsets, packets, bodies
}

func sceneReadyReplayPacketOffsetOfMsgID(t *testing.T, stream []byte, msgID uint16) int {
	t.Helper()
	offset := 0
	for len(stream) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
		if packet.Header.MsgID == msgID {
			return offset
		}
		offset += len(stream) - len(rest)
		stream = rest
	}
	return -1
}

func sceneReadyReplayJoinSlots(rows []liveSceneReadyEquipmentRow31) string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, strconv.Itoa(int(row.slot)))
	}
	return strings.Join(out, ",")
}

// TestLoginEquipmentContainerReachesTheClientAfterTheSceneCommitLikeTheScenePath 是本次修复
// 的主用例：**同一批装备容器字节，登录路径现在的交付位置与场景路径等价**。
//
// 断言全是**发出去的包**（位置 + 整包字节 + 包体逐行字段），不是任何标志位：
//  1. 登录路径在 typed op24 场景提交**之后**（客户端 type1345 边界）发出的 list 3 容器，
//     与场景路径（进副本 loading barrier 之后那次引导）发出的 list 3 容器**逐字节相同**；
//  2. 登录路径今天只有 op24 **之前**那一份装备容器，本修复使最后一份落到 op24 之后；
//  3. 槽 12 = 101040493、该行 +0x0B 耐久 = 48（操作者的验收值），31 行一个不少。
func TestLoginEquipmentContainerReachesTheClientAfterTheSceneCommitLikeTheScenePath(t *testing.T) {
	t.Setenv(loginEquipmentContainerEnv, "")
	t.Setenv(loginEquipmentSceneReadyReplayEnv, "")
	t.Setenv(skillSwitchInventoryModeEnv, skillSwitchInventoryBeforeTransitionTier)
	service, session, connection, logPath := newSceneReadyReplayFixture(t)
	armLoginRouteUpToOp24(session)

	// ---- 登录路径：op24 之前的七容器引导 → typed op24 提交 → 客户端 type1345 ----
	if err := service.sendCurrentSelectInventoryBootstrap(
		session,
		"initial_town_before_final_transition_after_actor_before_full_player_state",
	); err != nil {
		t.Fatal(err)
	}
	transitionBody, err := buildCurrentSceneTransitionBody(31, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.sendCurrentTownTransitionLocked(session, 6, 31, 2, transitionBody); err != nil {
		t.Fatal(err)
	}
	if err := service.handleLegacyGamePacket(session, buildLegacyTownSceneReadyAckPacket()); err != nil {
		t.Fatalf("legacy type1345 boundary: %v", err)
	}
	loginStream := append([]byte(nil), connection.write.Bytes()...)

	loginOffsets, loginPackets, loginBodies := sceneReadyReplayEquipmentContainers(t, loginStream)
	if len(loginBodies) != 2 {
		t.Fatalf("login route delivered %d list-3 containers, want 2 (pre-transition + post-commit)", len(loginBodies))
	}
	commitOffset := sceneReadyReplayPacketOffsetOfMsgID(t, loginStream, currentSceneTransitionMsgID)
	if commitOffset < 0 {
		t.Fatalf("typed op24 commit packet missing from the login stream")
	}
	if !(loginOffsets[0] < commitOffset && commitOffset < loginOffsets[1]) {
		t.Fatalf("equipment container offsets %v vs typed op24 offset %d: want pre < commit < post",
			loginOffsets, commitOffset)
	}

	// ---- 场景路径：进副本 loading barrier 之后那一次同样的引导 ----
	sceneConnection := &bufferConn{}
	sceneSession := newLoginEquipmentTestSession(sceneConnection, 6)
	armLoginRouteUpToOp24(sceneSession)
	if err := service.sendCurrentSelectInventoryBootstrap(
		sceneSession,
		"party_dungeon_shared_loading_barrier_after_actor_before_player_object",
	); err != nil {
		t.Fatal(err)
	}
	sceneStream := append([]byte(nil), sceneConnection.write.Bytes()...)
	_, _, sceneBodies := sceneReadyReplayEquipmentContainers(t, sceneStream)
	if len(sceneBodies) != 1 {
		t.Fatalf("scene-entry bootstrap delivered %d list-3 containers, want 1", len(sceneBodies))
	}

	// (1) 两条路径最后到达客户端的装备容器**逐字节相同**（同一次下发、同一批行）。
	postCommit := loginBodies[1]
	if !bytes.Equal(postCommit, sceneBodies[0]) {
		t.Fatalf("post-commit login container differs from the scene-entry container:\n login=%x\n scene=%x",
			postCommit, sceneBodies[0])
	}
	if !bytes.Equal(postCommit, loginBodies[0]) {
		t.Fatalf("post-commit login container differs from the pre-transition one; the same builder must emit the same rows")
	}

	// (3) 逐行字段：31 行、槽 12 武器 + 耐久 48。
	rows := liveCharacter6EquipmentRows31()
	entrySize := currentItemListEntrySizeForType(currentSocketListEquipment)
	wantLen := 3 + len(rows)*entrySize
	if len(postCommit) != wantLen {
		t.Fatalf("container body_len = %d, want %d (3 + %d rows x %d)", len(postCommit), wantLen, len(rows), entrySize)
	}
	if got := int(binary.LittleEndian.Uint16(postCommit[1:3])); got != len(rows) {
		t.Fatalf("container row count = %d, want %d", got, len(rows))
	}
	gotSlots := map[int16]uint32{}
	gotDurability := map[int16]uint16{}
	for index := 0; index < len(rows); index++ {
		row := postCommit[3+index*entrySize : 3+(index+1)*entrySize]
		slot := int16(binary.LittleEndian.Uint16(row[0:2]))
		gotSlots[slot] = binary.LittleEndian.Uint32(row[2:6])
		gotDurability[slot] = binary.LittleEndian.Uint16(row[0x0B:0x0D])
	}
	for _, row := range rows {
		if gotSlots[row.slot] != uint32(row.itemID) {
			t.Fatalf("slot %d item = %d, want %d", row.slot, gotSlots[row.slot], row.itemID)
		}
		if gotDurability[row.slot] != row.durability {
			t.Fatalf("slot %d durability = %d, want %d", row.slot, gotDurability[row.slot], row.durability)
		}
	}
	if gotSlots[12] != 101040493 || gotDurability[12] != 48 {
		t.Fatalf("weapon row = slot 12 item %d durability %d, want 101040493 / 48", gotSlots[12], gotDurability[12])
	}

	// 事件必须把"谁 / 哪些槽 / 耐久 / 位置 / 档位"写全，下一次登录可直接判读。
	logText := sceneReadyReplayTestLog(t, service, logPath)
	for _, want := range []string{
		"EVENT kind=game-login-equipment-scene-ready-replay-sent",
		"char_id=6",
		"list=3",
		"rows=31",
		"body_len=" + strconv.Itoa(wantLen),
		"ordering_position=" + loginEquipmentSceneReadyReplayPosition,
		"boundary=" + loginEquipmentSceneReadyReplayBoundary,
		"slots_sent=" + sceneReadyReplayJoinSlots(rows),
		"switch=" + loginEquipmentSceneReadyReplayAfterSceneReady,
		"switch_env=" + loginEquipmentSceneReadyReplayEnv,
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("replay event missing %q; log=\n%s", want, logText)
		}
	}
	if !strings.Contains(logText, "12:48") {
		t.Fatalf("replay event does not carry the weapon durability 12:48; log=\n%s", logText)
	}
	if len(loginPackets) != 2 {
		t.Fatalf("login equipment packets = %d, want 2", len(loginPackets))
	}
}

// TestLoginEquipmentSceneReadyReplayRollbackIsByteForByteToday 钉回退字面量：
// 写 before_transition_only 之后，type1345 边界**一个字节都不多写** ——
// 回退档那条流 = 修复档那条流**去掉最后那个容器包**，逐字节相同。
func TestLoginEquipmentSceneReadyReplayRollbackIsByteForByteToday(t *testing.T) {
	drive := func(tier string) ([]byte, string) {
		t.Helper()
		t.Setenv(loginEquipmentContainerEnv, "")
		t.Setenv(loginEquipmentSceneReadyReplayEnv, tier)
		t.Setenv(skillSwitchInventoryModeEnv, skillSwitchInventoryBeforeTransitionTier)
		service, session, connection, logPath := newSceneReadyReplayFixture(t)
		armLoginRouteUpToOp24(session)
		if err := service.sendCurrentSelectInventoryBootstrap(
			session,
			"initial_town_before_final_transition_after_actor_before_full_player_state",
		); err != nil {
			t.Fatal(err)
		}
		transitionBody, err := buildCurrentSceneTransitionBody(31, 2, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.sendCurrentTownTransitionLocked(session, 6, 31, 2, transitionBody); err != nil {
			t.Fatal(err)
		}
		if err := service.handleLegacyGamePacket(session, buildLegacyTownSceneReadyAckPacket()); err != nil {
			t.Fatalf("tier %q legacy type1345 boundary: %v", tier, err)
		}
		stream := append([]byte(nil), connection.write.Bytes()...)
		return stream, sceneReadyReplayTestLog(t, service, logPath)
	}

	fixStream, fixLog := drive("")
	rollbackStream, rollbackLog := drive(loginEquipmentSceneReadyReplayBeforeTransitionOnly)

	_, fixPackets, fixBodies := sceneReadyReplayEquipmentContainers(t, fixStream)
	if len(fixBodies) != 2 {
		t.Fatalf("fix tier containers = %d, want 2 (pre-transition + post-commit)", len(fixBodies))
	}
	postCommitPacket := fixPackets[1]
	if len(fixStream) < len(postCommitPacket) {
		t.Fatalf("fix stream is shorter than its own post-commit container packet")
	}
	if !bytes.Equal(rollbackStream, fixStream[:len(fixStream)-len(postCommitPacket)]) {
		t.Fatalf("rollback stream is not the fix stream minus the post-commit container:\n rollback=%x\n fix-minus =%x",
			rollbackStream, fixStream[:len(fixStream)-len(postCommitPacket)])
	}
	rollbackOffsets, _, rollbackBodies := sceneReadyReplayEquipmentContainers(t, rollbackStream)
	if len(rollbackBodies) != 1 || len(rollbackOffsets) != 1 {
		t.Fatalf("rollback tier delivered %d list-3 containers, want only the pre-transition one", len(rollbackBodies))
	}
	if !strings.Contains(rollbackLog, "EVENT kind=game-login-equipment-scene-ready-replay-suppressed") ||
		!strings.Contains(rollbackLog, "switch="+loginEquipmentSceneReadyReplayBeforeTransitionOnly) ||
		!strings.Contains(rollbackLog, "switch_env="+loginEquipmentSceneReadyReplayEnv) {
		t.Fatalf("rollback tier did not record its switch value; log=\n%s", rollbackLog)
	}
	if !strings.Contains(fixLog, "EVENT kind=game-login-equipment-scene-ready-replay-sent") {
		t.Fatalf("fix tier did not record its event; log=\n%s", fixLog)
	}
	if strings.Contains(rollbackLog, "EVENT kind=game-login-equipment-scene-ready-replay-sent") {
		t.Fatalf("rollback tier must not emit the fix event; log=\n%s", rollbackLog)
	}
}

// TestLoginEquipmentSceneReadyReplayFiresOncePerSceneEntry 钉去重：同一次进场里的
// 重复 type1345 不许再补一次（与"Buff 强化换装栏"第二条边界同一个 false→true 边沿）。
func TestLoginEquipmentSceneReadyReplayFiresOncePerSceneEntry(t *testing.T) {
	t.Setenv(loginEquipmentContainerEnv, "")
	t.Setenv(loginEquipmentSceneReadyReplayEnv, "")
	t.Setenv(skillSwitchInventoryModeEnv, skillSwitchInventoryBeforeTransitionTier)
	service, session, connection, _ := newSceneReadyReplayFixture(t)
	armSceneReadyAckBoundary(session)

	if err := service.handleLegacyGamePacket(session, buildLegacyTownSceneReadyAckPacket()); err != nil {
		t.Fatal(err)
	}
	first := append([]byte(nil), connection.write.Bytes()...)
	if _, _, bodies := sceneReadyReplayEquipmentContainers(t, first); len(bodies) != 1 {
		t.Fatalf("first scene-ready ack delivered %d equipment containers, want 1", len(bodies))
	}
	connection.write.Reset()
	if err := service.handleLegacyGamePacket(session, buildLegacyTownSceneReadyAckPacket()); err != nil {
		t.Fatal(err)
	}
	if repeat := connection.write.Bytes(); len(repeat) != 0 {
		t.Fatalf("repeated scene-ready ack wrote %d bytes, want 0: %x", len(repeat), repeat)
	}
}

// TestLoginEquipmentSceneReadyReplayLeavesTheScenePathsAlone 钉范围：不是"登录/选角进城"
// 这条路由的会话（进副本时路由已被清掉、或场景还没提交）一个字节都不写。
func TestLoginEquipmentSceneReadyReplayLeavesTheScenePathsAlone(t *testing.T) {
	t.Setenv(loginEquipmentContainerEnv, "")
	t.Setenv(loginEquipmentSceneReadyReplayEnv, "")
	t.Setenv(skillSwitchInventoryModeEnv, skillSwitchInventoryBeforeTransitionTier)

	cases := []struct {
		name    string
		prepare func(session *gameSession)
	}{
		{
			name: "dungeon_entry_cleared_the_town_route",
			prepare: func(session *gameSession) {
				armSceneReadyAckBoundary(session)
				session.initialTownRouteCharacterID = 0
				session.initialTownRouteStage = currentInitialTownRouteIdle
			},
		},
		{
			name: "town_route_not_yet_committed",
			prepare: func(session *gameSession) {
				armSceneReadyAckBoundary(session)
				session.initialTownRouteStage = currentInitialTownRouteTransitionSent - 1
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service, session, connection, _ := newSceneReadyReplayFixture(t)
			testCase.prepare(session)
			if err := service.sendCurrentLegacyTownSceneReadyBoundary(session, "scene_path_guard"); err != nil {
				t.Fatal(err)
			}
			if stream := connection.write.Bytes(); len(stream) != 0 {
				t.Fatalf("out-of-scope route wrote %d bytes: %x", len(stream), stream)
			}
		})
	}
}

// TestLoginEquipmentContainerWireRowAuditReadsTheEmittedBytes 钉审计函数的读法：
// 它从**已经发出的包体**里读回槽位与耐久；解析不了就明确报 false，
// 不许把畸形包体读成一组看似合理的值。
func TestLoginEquipmentContainerWireRowAuditReadsTheEmittedBytes(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	seedLiveCharacter631WornSlots(t, repositories)
	service := newLoginEquipmentTestService(repositories)
	session := newLoginEquipmentTestSession(&bufferConn{}, 6)
	ctx := context.Background()
	body, _, _, ok := service.buildCurrentEquippedItemListBodyForSession(ctx, session)
	if !ok {
		t.Fatal("equipment container body unavailable")
	}
	entrySize := currentItemListEntrySizeForType(currentSocketListEquipment)
	slots, durability, nonzero, rows, auditOK := loginEquipmentContainerWireRowAudit(body, entrySize)
	if !auditOK {
		t.Fatalf("audit failed on our own container body (%d bytes)", len(body))
	}
	wantRows := len(liveCharacter6EquipmentRows31())
	if rows != wantRows || len(slots) != wantRows || len(durability) != wantRows {
		t.Fatalf("audit rows=%d slots=%d durability=%d, want %d", rows, len(slots), len(durability), wantRows)
	}
	if slots[0] != "0" || slots[12] != "12" {
		t.Fatalf("audit slot list head = %v", slots[:13])
	}
	if durability[12] != "12:48" {
		t.Fatalf("audit durability[12] = %q, want 12:48", durability[12])
	}
	if !strings.Contains(strings.Join(nonzero, ","), "12:48") {
		t.Fatalf("audit nonzero durability list does not carry the weapon: %v", nonzero)
	}
	if _, _, _, _, bad := loginEquipmentContainerWireRowAudit(body, 8); bad {
		t.Fatal("audit accepted an entry size that cannot hold the durability word")
	}
	if _, _, _, _, bad := loginEquipmentContainerWireRowAudit([]byte{3, 0xff, 0xff}, entrySize); bad {
		t.Fatal("audit accepted a row count that overruns the body")
	}
}
