package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件钉的是第四轮修法：**登录路径提交前的权威载体顺序**。
//
// 全部断言都是**发出去的包与它们的相对顺序**（不是标志位）：
//   - 修复档：typed op24 之前最后一件 `class0/op2` 载体必须是**带 31 条装备 create 行的 mode1**，
//     那份 name-tag mode0 外观表必须排在它**之前**；
//   - 回退档：两帧字节完全相同、只是顺序回到"mode1 在前、mode0 在后"（= 今天，即缺陷形态）；
//   - 进副本那条路径两档**逐字节相同**（本改动不碰它）。

// loginAuthoritativeDriveOptions 是驱动登录路由的参数。
type loginAuthoritativeDriveOptions struct {
	tier              string                  // DNFBRIDGE_LOGIN_AUTHORITATIVE_BEFORE_COMMIT
	roundsTwoThreeOff bool                    // 第 2/3 轮的开关一起写回退字面量
	throughSceneAck   bool                    // 是否继续走"type1345 ack + 客户端第一条报文"
	pvpChannel        bool                    // 无双入口的800会检查角色已穿戴槽位。
	channelOverride   *channelcatalog.Channel // 核对现用不同频道的真实入口顺序。
}

// loginAuthoritativeDrive 把登录路由真正推到 typed op24（可选继续到 ack 与客户端首包），
// 返回整条下行流、op24 的字节偏移、包日志路径与 service/session。
func loginAuthoritativeDrive(t *testing.T, options loginAuthoritativeDriveOptions) ([]byte, int, string, *Service, *gameSession) {
	t.Helper()
	t.Setenv(loginAuthoritativeBeforeCommitEnv, options.tier)
	if options.roundsTwoThreeOff {
		t.Setenv(loginEquipmentContainerEnv, "")
		t.Setenv(loginEquipmentSceneReadyReplayEnv, loginEquipmentSceneReadyReplayBeforeTransitionOnly)
		t.Setenv(loginActorGenerationRebuildEnv, loginActorGenerationRebuildBeforeTransitionOnly)
	} else {
		t.Setenv(loginEquipmentContainerEnv, "")
		t.Setenv(loginEquipmentSceneReadyReplayEnv, "")
		t.Setenv(loginActorGenerationRebuildEnv, "")
	}
	t.Setenv(skillSwitchInventoryModeEnv, skillSwitchInventoryBeforeTransitionTier)

	service, session, repositories := newTownMoveTest(t)
	if options.pvpChannel || options.channelOverride != nil && isPVPChannel(*options.channelOverride) {
		// PK独立技能由角色结算事务初始化，角色读端与事务必须使用同一仓储。
		// 通用城镇夹具替换了Character读端，其旧事务仍指向空的内存Character。
		repositories = dnfrepomemory.NewMemoryGroup()
		service.repositoryProvider = func() (dnfrepo.Group, bool) { return repositories, true }
	}
	// 线上每一个 class0 载体的头都是 16 字节（`packet_log.txt` 每条 SEND 的 `header_size=16`），
	// 夹具按同一个口径写包，顺序断言才是在比"线上那条流"。
	service.options.gameUpperHeader = gameUpperHeaderServer16
	service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
	// 事件断言用：给 service 挂一个可读回的包日志。
	logger, logPath := newPeerEndpointTestLogger(t)
	service.packetLog = logger
	t.Cleanup(func() { closeSceneReadyReplayLogger(service) })
	// 线上真形状的角色 6（31 个已穿戴槽，逐条抄自 packet_log.txt 的 row_summary）。
	seedLiveCharacter631WornSlots(t, repositories)
	// 登录路由的 op19/技能页投影与 op21/op574 任务快照需要这两份目录（合成档，不碰真 PVF）。
	enableTownMoveSkillProjection(t, service, repositories, "6")
	service.initialSPTable = map[int]int{90: 3710}
	service.initialTPTable = map[int]int{90: 41}
	service.questCatalog = buildQuestListTestCatalog(t)
	service.adventureGroupTable = loadAdventureGroupTestTables(t)
	service.characterStats = testCharacterStatTable(t)
	loginAuthoritativeEnrichCreatureSlot(t, service, repositories)
	// 晶体契约/租赁点等账号级状态（序列里 op898 那一段的读端）。
	if repositories.Account == nil {
		t.Fatal("account repository unavailable")
	}
	if err := repositories.Account.Save(context.Background(), dnfrepo.AccountRecord{
		AccountID: defaultAccountPrefix + "10011",
		Metadata:  map[string]string{currentRentalPointMetadataKey: "0"},
	}); err != nil {
		t.Fatal(err)
	}

	session.selectedCharacterID = 6
	session.townSceneReadyCharacterID = 6
	// 账号口径与夹具里角色 6 的 AccountID 对齐（晶体契约/冒险团都按它读）。
	session.accountID = defaultAccountPrefix + "10011"
	if options.pvpChannel {
		session.channel = channelcatalog.Channel{ID: 75, Type: 8, Group: "pvp"}
	}
	if options.channelOverride != nil {
		session.channel = *options.channelOverride
	}
	// 线上 owner_channel=10（本次登录 conn game-000021），`currentTownActorOwnerContext`
	// 在默认档（不启用 client-channel 对齐）读的就是这个已提交的 town owner。
	session.townActorOwnerChannel = 10
	session.connectionTownActorOwnerChannel = 10
	service.armCurrentInitialTownRoute(session, 6)
	connection := session.conn.(*bufferConn)

	// 与生产调用点 `initial_town_route_progress.go:171-191` 逐句相同：两个前置载体与
	// typed op24 的包体都由同一批构建器产出，然后交给真正的登录路由函数。
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	charID, charName, character, hasCharacter := service.selectedCharacterForEnter(ctx, session)
	if charID != 6 || !hasCharacter {
		t.Fatalf("selected actor load = %d found=%t, want 6 true", charID, hasCharacter)
	}
	ownerChannel := currentTownActorOwnerContext(session)
	objectBody := service.buildCurrentSceneObjectListBodyForSessionInContext(
		ctx, session, charID, charName, character, hasCharacter, ownerChannel,
	)
	adventureSummary := service.currentAccountAdventureGroupSummaryForPacket(ctx, session, character, hasCharacter)
	mode1Body := service.buildCurrentActorBindingMode1BodyForSelectedInContext(
		ctx, session, character, hasCharacter, charID, uint32(adventureSummary.ManageLevel), ownerChannel,
	)
	row := currentSceneTransitionRow{
		ObjectOrResourceKey: currentSceneActorObjectKey(charID),
		Value1:              450,
		Value2:              234,
		Value3:              5,
		Value4:              3,
	}
	transitionBody, err := buildCurrentSceneTransitionBody(38, 1, []currentSceneTransitionRow{row})
	if err != nil {
		t.Fatal(err)
	}

	session.townMu.Lock()
	err = service.sendCurrentInitialTownActorRoutePacketsLocked(
		session, charID, 38, 1, row, objectBody, mode1Body, transitionBody,
	)
	session.townMu.Unlock()
	if err != nil {
		t.Fatalf("login town actor route: %v", err)
	}

	commitOffset := loginAuthoritativeOffsetOfMsgID(t, connection.write.Bytes(), currentSceneTransitionMsgID)
	if commitOffset < 0 {
		t.Fatal("typed op24 commit packet missing from the login stream")
	}
	if options.throughSceneAck {
		if err := service.handleGameStreamPacket(session, loginActorGenerationLegacyStream(buildLegacyTownSceneReadyAckPacket())); err != nil {
			t.Fatalf("legacy type1345 boundary: %v", err)
		}
		if err := service.handleGameStreamPacket(session, loginActorGenerationLegacyStream(loginActorGenerationClientPacket())); err != nil {
			t.Fatalf("first client packet after the scene-ready ack: %v", err)
		}
	}
	return append([]byte(nil), connection.write.Bytes()...), commitOffset, logPath, service, session
}

// loginAuthoritativeSplitAll 按**长度字**切整条下行流（不假设头长以外的任何东西，
// 因为这条流里混着若干种 class0 载体）。每个 16 字节头的帧都返回偏移与包体。
func loginAuthoritativeSplitAll(t *testing.T, stream []byte) []loginActorGenerationFrame {
	t.Helper()
	frames := make([]loginActorGenerationFrame, 0, 64)
	offset := 0
	remaining := stream
	for len(remaining) > 0 {
		if len(remaining) < 16 {
			t.Fatalf("trailing %d bytes at stream offset %d: %x", len(remaining), offset, remaining)
		}
		total := int(binary.LittleEndian.Uint32(remaining[3:7]))
		if total < 16 || total > len(remaining) {
			t.Fatalf("frame at stream offset %d has length %d (remaining %d) header=%x",
				offset, total, len(remaining), remaining[:16])
		}
		frame := remaining[:total]
		mode := byte(0xff)
		if total > 16 {
			mode = frame[16]
		}
		frames = append(frames, loginActorGenerationFrame{
			offset: offset,
			end:    offset + total,
			mode:   mode,
			msgID:  binary.LittleEndian.Uint16(frame[1:3]),
			body:   append([]byte(nil), frame[16:]...),
			packet: append([]byte(nil), frame...),
		})
		offset += total
		remaining = remaining[total:]
	}
	return frames
}

// loginAuthoritativeOp2BeforeCommit 返回 typed op24 之前的所有 `class0/op2` 帧（actor 世代载体）。
func loginAuthoritativeOp2BeforeCommit(t *testing.T, stream []byte, commitOffset int) []loginActorGenerationFrame {
	t.Helper()
	frames := make([]loginActorGenerationFrame, 0, 8)
	for _, frame := range loginAuthoritativeSplitAll(t, stream) {
		if frame.msgID == uint16(dnfenum.CmdPacketSetUDPIPPort) && frame.offset < commitOffset {
			frames = append(frames, frame)
		}
	}
	return frames
}

// loginAuthoritativeOffsetOfMsgID 在整条流里找某个 msg_id 的第一帧偏移。
func loginAuthoritativeOffsetOfMsgID(t *testing.T, stream []byte, msgID uint16) int {
	t.Helper()
	for _, frame := range loginAuthoritativeSplitAll(t, stream) {
		if frame.msgID == msgID {
			return frame.offset
		}
	}
	return -1
}

// loginAuthoritativeEnrichCreatureSlot 给夹具的槽 26 补齐线上那一份宠物行的序列号
// （现场 `mode0_creature_serial_or_handle=8 mode0_creature_source=equipment_slot26_raw`，
// 读端是 raw[24:28]）。属夹具补齐，不改第 1~3 轮的共用夹具函数。
func loginAuthoritativeEnrichCreatureSlot(t *testing.T, service *Service, repositories dnfrepo.Group) {
	t.Helper()
	if repositories.Equipment == nil {
		t.Fatal("equipment repository unavailable")
	}
	ctx := context.Background()
	record, found, err := repositories.Equipment.Load(ctx, "6")
	if err != nil || !found {
		t.Fatalf("load seeded equipment: found=%t err=%v", found, err)
	}
	entry, worn := record.Entries["26"]
	if !worn || len(entry.RawEntry) < 28 {
		t.Fatal("seeded slot 26 row is missing or too short")
	}
	binary.LittleEndian.PutUint32(entry.RawEntry[24:28], 8)
	if entry.Extra == nil {
		entry.Extra = make(map[string]string, 2)
	}
	entry.Extra["creature_serial_or_handle"] = "8"
	entry.Extra["creature_level"] = "1"
	record.Entries["26"] = entry
	if err := repositories.Equipment.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	// 线上这对宠物对象还需要它的成长/饱食记录（op105 的读端）。
	if repositories.Pet == nil {
		t.Fatal("pet repository unavailable")
	}
	if err := repositories.Pet.Save(ctx, dnfrepo.PetRecord{
		CharacterID: "6",
		EquippedKey: "8",
		Entries: map[string]dnfrepo.PetEntry{
			"8": {
				PetKey:      "8",
				CreatureKey: 400990170,
				ItemID:      400990170,
				Name:        "pet",
				Level:       1,
				Satiety:     50,
				ModeFlag:    1,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// loginAuthoritativeReferenceMode1 用**进副本那条路径的同一个构建器**
// （`scene_post_start_map.go:381-406` → `buildCurrentActorBindingMode1BodyForSelectedWithEquipmentInContext`）
// 在**同一条会话**上把 mode1 再装配一遍，作为对照字节。
func loginAuthoritativeReferenceMode1(t *testing.T, service *Service, session *gameSession) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	charID, _, character, hasCharacter := service.selectedCharacterForEnter(ctx, session)
	summary := service.currentAccountAdventureGroupSummaryForPacket(ctx, session, character, hasCharacter)
	return service.buildCurrentActorBindingMode1BodyForSelectedWithEquipmentInContext(
		ctx,
		session,
		character,
		hasCharacter,
		charID,
		uint32(summary.ManageLevel),
		currentTownActorOwnerContext(session),
		true,
	)
}

// loginAuthoritativeReferenceMode0 用与发送侧完全相同的构建器复算那份 name-tag mode0 外观表，
// 用来证明"排在装备 mode1 之前的那一帧就是它"，而不是别的 mode0。
func loginAuthoritativeReferenceMode0(t *testing.T, service *Service, session *gameSession) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	loadedID, charName, character, hasCharacter := service.selectedCharacterForEnter(ctx, session)
	if loadedID != 6 || !hasCharacter {
		t.Fatalf("selected actor load = %d found=%t, want 6 true", loadedID, hasCharacter)
	}
	body, err := service.buildCurrentSceneObjectListBodyForSessionInContextStrict(
		ctx,
		session,
		6,
		charName,
		character,
		hasCharacter,
		currentTownActorOwnerContext(session),
	)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// loginAuthoritativeMode0RowWalk 走一遍那份 mode0 的外观表，返回行数与"是否每一行的
// raw 块长度都是 0"（外观行没有耐久字段、没有实例种子字段，见修法文件头 §二）。
func loginAuthoritativeMode0RowWalk(body []byte) (rows int, allRawEmpty bool, ok bool) {
	_, tailStart, levelOK := currentSceneObjectLevelForLog(body)
	if !levelOK || tailStart+8 > len(body) {
		return 0, false, false
	}
	tail := body[tailStart:]
	// tail[6] 是外观表行数；`currentSceneObjectEquipSummaryEnd` 用同一口径走位。
	rows = int(tail[6])
	equipEnd, endOK := currentSceneObjectEquipSummaryEnd(tail, 6)
	if !endOK {
		return rows, false, false
	}
	position := 7
	allRawEmpty = true
	for index := 0; index < rows; index++ {
		if position+9 > len(tail) {
			return rows, false, false
		}
		rawLen := int(int32(uint32(tail[position+5]) | uint32(tail[position+6])<<8 |
			uint32(tail[position+7])<<16 | uint32(tail[position+8])<<24))
		if rawLen != 0 {
			allRawEmpty = false
		}
		position += 5 + 4 + rawLen + 10
	}
	if position != equipEnd {
		return rows, false, false
	}
	return rows, allRawEmpty, true
}

// mode1WeaponInstance 是现场武器 create 行 +5 的实例/品质种子（999999998 = 0x3B9AC9FE），
// 那份只带外观表的 mode0 里**不可能**出现它。
var mode1WeaponInstance = []byte{0xfe, 0xc9, 0x9a, 0x3b}

// TestLoginAuthoritativeBeforeCommitEquipmentMode1IsTheLastCarrierBeforeTheTypedOp24Commit 是主用例。
func TestLoginAuthoritativeBeforeCommitEquipmentMode1IsTheLastCarrierBeforeTheTypedOp24Commit(t *testing.T) {
	stream, commitOffset, logPath, service, session := loginAuthoritativeDrive(t, loginAuthoritativeDriveOptions{})
	frames := loginAuthoritativeOp2BeforeCommit(t, stream, commitOffset)

	// (1) 提交前恰好四帧 actor 载体，顺序是：
	//     mode0 引导（前置绑定）→ mode1 binding-only（前置绑定）→ **name-tag mode0 外观表** →
	//     **mode1 完整装备 create 行**。
	if len(frames) != 4 {
		t.Fatalf("login route delivered %d class0/op2 actor frames before typed op24, want 4 (mode0 + binding mode1 + nametag mode0 + equipment mode1)", len(frames))
	}
	wantModes := []byte{0, 1, 0, 1}
	for index, want := range wantModes {
		if frames[index].mode != want {
			t.Fatalf("actor frame %d mode = %d, want %d (mode sequence %v)", index, frames[index].mode, want, wantModes)
		}
	}

	// (2) 提交前**最后一件**载体是带装备 create 行的 mode1（= 进副本/回城那条不变量）。
	last := frames[len(frames)-1]
	if last.mode != 1 {
		t.Fatalf("last actor carrier before typed op24 is mode %d, want mode 1 (equipment-bearing mode1)", last.mode)
	}
	if last.offset >= commitOffset {
		t.Fatalf("equipment mode1 starts at %d, not before the typed op24 commit at %d", last.offset, commitOffset)
	}
	createCount, slots, durability, nonzero, weapon, auditOK := loginActorGenerationMode1WireRowAudit(last.body)
	if !auditOK {
		t.Fatalf("equipment mode1 wire row walk failed on our own body (%d bytes)", len(last.body))
	}
	wantRows := len(liveCharacter6EquipmentRows31())
	if createCount != wantRows || len(slots) != wantRows {
		t.Fatalf("equipment mode1 create rows = %d slots = %d, want %d", createCount, len(slots), wantRows)
	}
	if got := int(last.body[currentMode1CreateCountOffset]); got != wantRows {
		t.Fatalf("equipment mode1 body[%d] (create count) = %d, want %d", currentMode1CreateCountOffset, got, wantRows)
	}
	if !strings.Contains(strings.Join(durability, ","), "12:48") {
		t.Fatalf("equipment mode1 durability list does not carry the weapon: %v", durability)
	}
	if !strings.Contains(strings.Join(nonzero, ","), "12:48") {
		t.Fatalf("equipment mode1 nonzero durability list does not carry the weapon: %v", nonzero)
	}
	if weapon[1] != "present" {
		t.Fatalf("equipment mode1 weapon projection = %v, want present", weapon)
	}
	if !bytes.Equal(last.body, loginAuthoritativeReferenceMode1(t, service, session)) {
		t.Fatal("equipment mode1 before the commit is not the scene-path builder output")
	}

	// (3) 排在它之前的那一帧就是 name-tag mode0 外观表（同一台构建器逐字节相同）。
	if !bytes.Equal(frames[2].body, loginAuthoritativeReferenceMode0(t, service, session)) {
		t.Fatalf("the actor frame before the equipment mode1 is not the name-tag mode0 body:\n got=%x\nwant=%x",
			frames[2].body, loginAuthoritativeReferenceMode0(t, service, session))
	}

	// (4) 这一帧确实是**残缺**载体：外观表在、raw 块长度为 0、**没有**任何实例种子字节，
	//     而装备 mode1 里那 4 个字节必须在。
	rows, allRawEmpty, walkOK := loginAuthoritativeMode0RowWalk(frames[2].body)
	if !walkOK || rows == 0 {
		t.Fatalf("name-tag mode0 appearance walk failed (rows=%d ok=%t)", rows, walkOK)
	}
	if !allRawEmpty {
		t.Fatal("name-tag mode0 appearance rows unexpectedly carry raw blocks")
	}
	if bytes.Contains(frames[2].body, mode1WeaponInstance) {
		t.Fatal("name-tag mode0 appearance table carries the equipment instance seed; the carrier model is wrong")
	}
	if !bytes.Contains(last.body, mode1WeaponInstance) {
		t.Fatal("equipment mode1 does not carry the weapon instance seed; the fixture is wrong")
	}

	// (5) 事件：位置 + 顺序 + 行 + 耐久 + 开关。
	log := sceneReadyReplayTestLog(t, service, logPath)
	for _, want := range []string{
		"game-login-authoritative-before-commit-ordered",
		"ordering_position=" + loginAuthoritativeBeforeCommitOrderPosition,
		"frames_before_typed_op24=" + loginAuthoritativeBeforeCommitFramesFixed,
		"last_actor_carrier_before_typed_op24=class0_op2_mode1_actor_equipment_create_rows",
		"nametag_mode0_position=before_equipment_mode1",
		"nametag_replay_emitted=1",
		"equipment_mode1_sent=1",
		"mode1_row_walk=1",
		"mode1_create_count=31",
		"actor_slot_count=31",
		"12:48",
		"actor_weapon_item_id=101040493",
		"actor_weapon_durability_sent=48",
		"mode0_carries_durability=0",
		"mode0_carries_instance_seed=0",
		"switch=" + loginAuthoritativeBeforeCommitEquipmentMode1Last,
		"switch_env=" + loginAuthoritativeBeforeCommitEnv,
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("order event is missing %q\n---- log ----\n%s", want, log)
		}
	}
}

// TestLoginAuthoritativeBeforeCommitRollbackIsTheSameBytesInTodaysOrder 钉回退档：
// 两帧字节与修复档**完全相同**，只是顺序回到今天（装备 mode1 在前、外观表在后 = 缺陷形态）。
func TestLoginAuthoritativeBeforeCommitRollbackIsTheSameBytesInTodaysOrder(t *testing.T) {
	fixStream, fixCommit, _, _, _ := loginAuthoritativeDrive(t, loginAuthoritativeDriveOptions{})
	fixFrames := loginAuthoritativeOp2BeforeCommit(t, fixStream, fixCommit)

	legacyStream, legacyCommit, logPath, service, _ := loginAuthoritativeDrive(t, loginAuthoritativeDriveOptions{
		tier: loginAuthoritativeBeforeCommitNameTagAfterMode1,
	})
	legacyFrames := loginAuthoritativeOp2BeforeCommit(t, legacyStream, legacyCommit)

	if len(fixFrames) != 4 || len(legacyFrames) != 4 {
		t.Fatalf("actor frames before the commit: fix=%d legacy=%d, want 4/4", len(fixFrames), len(legacyFrames))
	}
	// 今天：装备 mode1 在**倒数第二**、那份残缺的外观表在最后 —— 这正是被修的顺序。
	if legacyFrames[2].mode != 1 || legacyFrames[3].mode != 0 {
		t.Fatalf("rollback actor frame 2/3 modes = %d,%d, want 1 (equipment mode1) then 0 (appearance mode0)",
			legacyFrames[2].mode, legacyFrames[3].mode)
	}
	// 逐字节：两档只是把**同样两帧**换了位置。
	if !bytes.Equal(legacyFrames[2].body, fixFrames[3].body) {
		t.Fatal("rollback equipment mode1 body differs from the fix tier equipment mode1 body")
	}
	if !bytes.Equal(legacyFrames[3].body, fixFrames[2].body) {
		t.Fatal("rollback appearance mode0 body differs from the fix tier appearance mode0 body")
	}
	fixTail := fixStream[fixFrames[3].end:]
	legacyTail := legacyStream[legacyFrames[3].end:]
	if !bytes.Equal(fixTail, legacyTail) {
		t.Fatalf("the stream after the two swapped frames differs:\n fix=%x\n legacy=%x", fixTail, legacyTail)
	}
	if !bytes.Equal(fixStream[:fixFrames[2].offset], legacyStream[:legacyFrames[2].offset]) {
		t.Fatal("the stream before the two swapped frames differs")
	}
	if len(fixStream) != len(legacyStream) {
		t.Fatalf("fix tier stream = %d bytes, rollback tier = %d bytes; the reorder must not change the length",
			len(fixStream), len(legacyStream))
	}

	log := sceneReadyReplayTestLog(t, service, logPath)
	for _, want := range []string{
		"game-login-authoritative-before-commit-legacy-order",
		"frames_before_typed_op24=" + loginAuthoritativeBeforeCommitFramesLegacy,
		"last_actor_carrier_before_typed_op24=class0_op2_mode0_scene_object_list_appearance_table_only",
		"nametag_mode0_position=after_equipment_mode1",
		"switch=" + loginAuthoritativeBeforeCommitNameTagAfterMode1,
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("rollback order event is missing %q\n---- log ----\n%s", want, log)
		}
	}
	if strings.Contains(log, "game-login-authoritative-before-commit-ordered") {
		t.Fatal("rollback tier must not emit the fix-tier order event")
	}
}

// TestLoginAuthoritativeBeforeCommitKeepsRoundsTwoAndThreeTiers 是 2x2 矩阵：
// 第 2/3 轮的开关写回退字面量时，它们各自的档位行为必须原样保留，而本轮的顺序改动
// 仍然只是"那两帧互换位置"。
func TestLoginAuthoritativeBeforeCommitKeepsRoundsTwoAndThreeTiers(t *testing.T) {
	for _, tc := range []struct {
		name              string
		tier              string
		roundsTwoThreeOff bool
		wantLastMode      byte
		wantContainers    int
		wantRebuildFrames int
	}{
		{"fix tier with rounds 2-3 at their defaults", "", false, 1, 2, 2},
		{"fix tier with rounds 2-3 rolled back", "", true, 1, 1, 0},
		{"rollback tier with rounds 2-3 at their defaults", loginAuthoritativeBeforeCommitNameTagAfterMode1, false, 0, 2, 2},
		{"rollback tier with rounds 2-3 rolled back", loginAuthoritativeBeforeCommitNameTagAfterMode1, true, 0, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream, commitOffset, _, _, _ := loginAuthoritativeDrive(t, loginAuthoritativeDriveOptions{
				tier:              tc.tier,
				roundsTwoThreeOff: tc.roundsTwoThreeOff,
				throughSceneAck:   true,
			})
			frames := loginAuthoritativeOp2BeforeCommit(t, stream, commitOffset)
			if len(frames) == 0 {
				t.Fatal("no actor carrier before the typed op24 commit")
			}
			if got := frames[len(frames)-1].mode; got != tc.wantLastMode {
				t.Fatalf("last actor carrier before the commit = mode %d, want mode %d", got, tc.wantLastMode)
			}
			// 第 2 轮档位：场景就绪点之后再补一份 list 3 容器（默认档 2 份，回退档 1 份）。
			_, containers, _ := sceneReadyReplayEquipmentContainers(t, stream)
			if len(containers) != tc.wantContainers {
				t.Fatalf("list-3 containers in the stream = %d, want %d (round 2 tier)", len(containers), tc.wantContainers)
			}
			// 第 3 轮档位：ack 之后补发的 actor 世代帧数（默认档 2，回退档 0）。
			rebuild := 0
			for _, frame := range loginAuthoritativeSplitAll(t, stream) {
				if frame.msgID == uint16(dnfenum.CmdPacketSetUDPIPPort) && frame.offset > commitOffset {
					rebuild++
				}
			}
			if rebuild != tc.wantRebuildFrames {
				t.Fatalf("actor frames after the commit = %d, want %d (round 3 tier)", rebuild, tc.wantRebuildFrames)
			}
		})
	}
}

// TestLoginAuthoritativeBeforeCommitLeavesTheDungeonPathByteForByte 钉"进副本那条路径没被碰"：
// 两档把同一条副本下行流各跑一遍，必须**逐字节相同**。
func TestLoginAuthoritativeBeforeCommitLeavesTheDungeonPathByteForByte(t *testing.T) {
	dungeonStream := func(t *testing.T, tier string) []byte {
		t.Helper()
		t.Setenv(loginAuthoritativeBeforeCommitEnv, tier)
		service, runtimeState := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{disableTutorial: true})
		service.characterStats = testCharacterStatTable(t)
		connection := &bufferConn{}
		channel := channelcatalogChannelForAuthoritativeTest()
		session := &gameSession{
			conn:                connection,
			channel:             channel,
			residentChannel:     channel,
			selectedCharacterID: 99,
		}
		if err := service.sendCurrentPostStartMapPlayerPlacement(
			session,
			runtimeState,
			runtimeState.Session.Snapshot().Scene,
			"r4_dungeon_path_unchanged",
		); err != nil {
			t.Fatal(err)
		}
		return append([]byte(nil), connection.write.Bytes()...)
	}
	fix := dungeonStream(t, "")
	legacy := dungeonStream(t, loginAuthoritativeBeforeCommitNameTagAfterMode1)
	if !bytes.Equal(fix, legacy) {
		t.Fatalf("dungeon path bytes differ across tiers (fix=%d legacy=%d)", len(fix), len(legacy))
	}
	if len(fix) == 0 {
		t.Fatal("dungeon path fixture wrote nothing")
	}
}

// TestLoginAuthoritativeBeforeCommitHookIsLoginRouteOnly 是结构判据：
// 这次顺序改动的唯一动作（`loginAuthoritativeBeforeCommitNameTagReplay`）只允许出现在
// 登录玩家状态文件里被调用 —— 进副本/回城那条路径不可能间接受影响。
func TestLoginAuthoritativeBeforeCommitHookIsLoginRouteOnly(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(currentFile)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	const hook = "loginAuthoritativeBeforeCommitNameTagReplay"
	callers := make([]string, 0, 2)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Name.Name == hook {
				continue
			}
			ast.Inspect(function, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != hook {
					return true
				}
				callers = append(callers, name+":"+function.Name.Name)
				return true
			})
		}
	}
	if len(callers) == 0 {
		t.Fatal("the ordering hook has no production caller")
	}
	for _, caller := range callers {
		if !strings.HasPrefix(caller, "initial_town_player_state.go:") {
			t.Fatalf("ordering hook called from %s, want only initial_town_player_state.go", caller)
		}
	}
}

func channelcatalogChannelForAuthoritativeTest() channelcatalog.Channel {
	return channelcatalog.Channel{ServerID: 1, ID: 16, Type: 1, Name: "ch.16", Port: 10016}
}

// loginAuthoritativeEventCount 数一条事件在日志里出现的次数（判"恰好一条"）。
func loginAuthoritativeEventCount(log string, event string) int {
	count := 0
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "EVENT kind="+event+" ") {
			count++
		}
	}
	return count
}

// TestLoginAuthoritativeBeforeCommitEmitsExactlyOneOrderEvent 钉"每次进城恰好一条"。
func TestLoginAuthoritativeBeforeCommitEmitsExactlyOneOrderEvent(t *testing.T) {
	_, _, logPath, service, _ := loginAuthoritativeDrive(t, loginAuthoritativeDriveOptions{throughSceneAck: true})
	log := sceneReadyReplayTestLog(t, service, logPath)
	if got := loginAuthoritativeEventCount(log, "game-login-authoritative-before-commit-ordered"); got != 1 {
		t.Fatalf("fix-tier order events = %d, want 1\n---- log ----\n%s", got, log)
	}
	if got := loginAuthoritativeEventCount(log, "game-login-authoritative-before-commit-legacy-order"); got != 0 {
		t.Fatalf("legacy order event appeared in the fix tier: %d", got)
	}
	if !strings.Contains(log, "nametag_slot30_prerequisite=worn_slot30_item_2747069") {
		t.Fatalf("order event lost the name-tag slot-30 prerequisite evidence\n---- log ----\n%s", log)
	}
	// mode0 外观表的行数来自真实仓储（夹具里没有名字牌 stats ⇒ 14 行外观表）。
	if !strings.Contains(log, "mode0_equipment_appearance_rows="+strconv.Itoa(currentActorMode0AppearanceSlotCount)) {
		t.Fatalf("order event appearance row count is not the repository-backed table\n---- log ----\n%s", log)
	}
	if !strings.Contains(log, "mode0_equipment_appearance_source="+currentActorMode0AppearanceBodySource) {
		t.Fatalf("order event appearance source missing\n---- log ----\n%s", log)
	}
	// 把这一行原样打出来：它就是报告里"确切期望行"的来源（`-v` 可见）。
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "EVENT kind=game-login-authoritative-before-commit-ordered") {
			t.Logf("fix-tier order event: %s", line)
		}
	}
	// 这份 mode0 载体的形状判据必须在事件里。
	for _, want := range []string{"mode0_carrier=class0_op2_mode0_scene_object_list", "mode0_row_bytes=19"} {
		if !strings.Contains(log, want) {
			t.Fatalf("order event is missing %q", want)
		}
	}
}
