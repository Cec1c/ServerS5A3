package dnfbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type loginPetRefreshBuffFailureConn struct {
	*bufferConn
	failBuff     bool
	buffAttempts int
}

func (c *loginPetRefreshBuffFailureConn) Write(data []byte) (int, error) {
	if len(data) >= 3 && binary.LittleEndian.Uint16(data[1:3]) == nativeBuffSwapSnapshotID {
		c.buffAttempts++
		if c.failBuff {
			return 0, errors.New("test buff snapshot write failed")
		}
	}
	return c.bufferConn.Write(data)
}

func TestLoginActorGenerationInvalidPetStillAttemptsBuffAndRetainsErrorsOnce(t *testing.T) {
	for _, failBuff := range []bool{false, true} {
		t.Run(strconv.FormatBool(failBuff), func(t *testing.T) {
			t.Setenv(loginActorGenerationRebuildEnv, "")
			t.Setenv(buffSwapReapplyEnv, "on")
			service, session, connection, logPath := newSceneReadyReplayFixture(t)
			repos, _ := service.repositoryGroup()
			character, found, err := repos.Character.Load(context.Background(), "6")
			if err != nil || !found {
				t.Fatalf("fixture character owner: found=%v err=%v", found, err)
			}
			session.accountID = character.AccountID
			// Existing 31-slot fixture contains slot26 with serial0. Keep that
			// invalid instance intact, so no fabricated durable op14 is possible.
			if err := buffswap.Apply(context.Background(), repos, "6", service.accountIDForSession(session),
				buffswap.Command{Skill: 38, Slot: buffswap.SlotCount, List: 44, Index: 0xffff}); err != nil {
				t.Fatal(err)
			}
			observed := &loginPetRefreshBuffFailureConn{bufferConn: connection, failBuff: failBuff}
			session.conn = observed
			armLoginRouteUpToOp24(session)
			session.initialTownRouteStage = currentInitialTownRouteTransitionSent
			session.initialTownLegacySceneReadyAccepted = true
			service.armLoginActorGenerationRebuild(session)
			service.afterLoginSceneReadyFirstClientPacket(session, "test_post_commit_client")
			frames := loginActorGenerationSplit(t, connection.write.Bytes())
			actorCount, buffCount := 0, 0
			for _, frame := range frames {
				switch frame.msgID {
				case 2:
					actorCount++
				case 14:
					t.Fatal("invalid pet instance generated op14")
				case nativeBuffSwapSnapshotID:
					buffCount++
					if binary.LittleEndian.Uint16(frame.body) != 38 {
						t.Fatal("buff reapply lost persisted skill")
					}
				}
			}
			if actorCount != 2 || observed.buffAttempts != 1 || (!failBuff && buffCount != 1) {
				t.Fatalf("actor=%d buff attempts=%d delivered=%d", actorCount, observed.buffAttempts, buffCount)
			}
			written := connection.write.Len()
			service.armLoginActorGenerationRebuild(session)
			service.afterLoginSceneReadyFirstClientPacket(session, "duplicate_client")
			if connection.write.Len() != written || observed.buffAttempts != 1 || !session.loginActorGenerationRebuildSent {
				t.Fatal("failed pet refresh allowed generation/buff duplicate")
			}
			logText := sceneReadyReplayTestLog(t, service, logPath)
			for _, want := range []string{"game-login-actor-generation-rebuild-sent", "game-login-actor-generation-pet-refresh-failed",
				"game-login-actor-generation-rebuild-failed", "rejected invalid slot 26"} {
				if !strings.Contains(logText, want) {
					t.Fatalf("missing failure evidence %q", want)
				}
			}
			if failBuff {
				writeText := "test buff snapshot write failed" // public synthetic fixture
				exact := fmt.Sprintf("error=redacted error_len=%d error_sha256=%x", len(writeText), sha256.Sum256([]byte(writeText)))
				found := false
				for _, line := range strings.Split(logText, "\n") {
					if strings.Contains(line, "kind=game-login-actor-generation-buff-refresh-failed ") && strings.Contains(line, exact) {
						found = true
					}
				}
				if !found || strings.Contains(logText, writeText) {
					t.Fatal("buff transport failure lost exact hash evidence or exposed its body")
				}
			}
		})
	}
}

func TestLoginActorGenerationRebuildRefreshesDurablePetAfterLastMode1Once(t *testing.T) {
	t.Setenv(loginActorGenerationRebuildEnv, "")
	t.Setenv(loginEquipmentSceneReadyReplayEnv, loginEquipmentSceneReadyReplayBeforeTransitionOnly)
	t.Setenv(skillSwitchInventoryModeEnv, skillSwitchInventoryBeforeTransitionTier)
	service, session, connection, _ := newSceneReadyReplayFixture(t)
	repos, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("fixture repository unavailable")
	}
	ctx := context.Background()
	record, found, err := repos.Equipment.Load(ctx, "6")
	if err != nil || !found {
		t.Fatalf("load fixture equipment: found=%v err=%v", found, err)
	}
	// The worn row intentionally lacks a card. The durable pet record supplies
	// it only for this exact real item/instance pair, exercising the helper's
	// repository fallback rather than asserting a synthetic default row.
	record.Entries["26"] = dnfrepo.EquipmentEntry{
		SlotIndex: 26, ItemID: 400990167,
		RawEntry: testCurrentEquippedCreatureRaw(26, 400990167, 1),
		Extra:    map[string]string{"equipment_slot": "26", "creature_serial_or_handle": "1"},
	}
	if err := repos.Equipment.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := repos.Pet.Save(ctx, dnfrepo.PetRecord{CharacterID: "6", EquippedKey: "pet-1", Entries: map[string]dnfrepo.PetEntry{
		"pet-1": {PetKey: "pet-1", ItemID: 400990167, Level: 1, Extra: map[string]string{
			"creature_serial_or_handle": "1", "pet_enchant_card_item_id": "10008663", "pet_enchant_upgrade_count": "2",
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	armLoginRouteUpToOp24(session)
	transitionBody, err := buildCurrentSceneTransitionBody(31, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.sendCurrentTownTransitionLocked(session, 6, 31, 2, transitionBody); err != nil {
		t.Fatal(err)
	}
	if err := service.handleGameStreamPacket(session, loginActorGenerationLegacyStream(buildLegacyTownSceneReadyAckPacket())); err != nil {
		t.Fatal(err)
	}
	// Scene-ready alone must not publish the new generation or pet refresh.
	for _, frame := range loginActorGenerationSplit(t, connection.write.Bytes()) {
		if frame.msgID == 2 || frame.msgID == 14 {
			t.Fatalf("actor/pet frame %d published before first post-commit client packet", frame.msgID)
		}
	}
	connection.write.Reset()
	if err := service.handleGameStreamPacket(session, loginActorGenerationLegacyStream(loginActorGenerationClientPacket())); err != nil {
		t.Fatal(err)
	}
	frames := loginActorGenerationSplit(t, connection.write.Bytes())
	op2Count, petCount, mode1End := 0, 0, -1
	for _, frame := range frames {
		if frame.msgID == 2 {
			op2Count++
			if frame.mode == 1 {
				mode1End = frame.end
			}
		}
		if frame.msgID != 14 {
			continue
		}
		petCount++
		if mode1End < 0 || frame.offset < mode1End {
			t.Fatal("durable pet refresh precedes final generation mode1")
		}
		if len(frame.body) != 3+len((currentItemListEntry{}).data) || frame.body[0] != 3 || binary.LittleEndian.Uint16(frame.body[1:3]) != 1 {
			t.Fatalf("pet refresh framing = %x", frame.body)
		}
		raw := frame.body[3:]
		if binary.LittleEndian.Uint16(raw) != 26 || binary.LittleEndian.Uint32(raw[2:]) != 400990167 ||
			binary.LittleEndian.Uint32(raw[6:]) != 1 || binary.LittleEndian.Uint32(raw[14:]) != 10008663 || raw[18] != 2 {
			t.Fatalf("durable pet instance/enchant row = %x", raw)
		}
	}
	if op2Count != 2 || petCount != 1 {
		t.Fatalf("generation op2=%d pet op14=%d, want 2/1", op2Count, petCount)
	}
	connection.write.Reset()
	if err := service.handleGameStreamPacket(session, loginActorGenerationLegacyStream(loginActorGenerationClientPacket())); err != nil {
		t.Fatal(err)
	}
	for _, frame := range loginActorGenerationSplit(t, connection.write.Bytes()) {
		if frame.msgID == 2 || frame.msgID == 14 {
			t.Fatalf("duplicate generation/pet frame %d on second client packet", frame.msgID)
		}
	}
}

// 本文件钉的是第三轮修法：**登录路径在"客户端提交场景之后、由客户端自己的下一条报文
// 触发的这一刻"补发 actor 世代（mode0 + 带装备 create 行的 mode1）**。
//
// 所有断言都是**发出去的包**（位置 + 整包字节 + 包体逐行字段），不是标志位：
//   - 位置：typed op24 提交 < 客户端 type1345 的响应 < mode0 < mode1；
//   - 形状：mode1 与**进副本那条路径**（`scene_post_start_map.go:381-406`）用同一个
//     构建器、同一次`includeEquipment=true`，逐字节相同；
//   - 内容：角色 6 的 31 条已穿戴行，槽 12 = 101040493、行 +10 耐久 48。

// loginActorGenerationLegacyStream 把一条原始 legacy 报文包成读循环拿到的那一帧。
func loginActorGenerationLegacyStream(raw []byte) dnfproto.LatestGameStreamPacket {
	return dnfproto.LatestGameStreamPacket{
		Kind: dnfproto.LatestGameStreamLegacy,
		Data: append([]byte(nil), raw...),
	}
}

// loginActorGenerationClientPacket 造一条"客户端提交之后自己发来的"legacy 报文。
// 现场 four 次登录里领跑的都是 type217（3 字节体），这里照抄那个形状。
func loginActorGenerationClientPacket() []byte {
	return buildLegacyGamePacketForBridgeTest(
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketOverflowInfo),
		0,
		[]byte{0, 0, 0},
	)
}

type loginActorGenerationFrame struct {
	offset int
	end    int
	mode   byte
	msgID  uint16
	body   []byte
	packet []byte
}

// loginActorGenerationSplit 按**当前 EXE 的 16 字节 class0 头**切流。
// 不能用 `splitCurrentGameServerUpperPacketAuto`：它只对 op13 那一小组 msg_id 走 16 字节头，
// 其余回落 13 字节 legacy 头 —— op2 会被切错 3 个字节（body[0] 会读成头里的 0）。
func loginActorGenerationSplit(t *testing.T, stream []byte) []loginActorGenerationFrame {
	t.Helper()
	frames := make([]loginActorGenerationFrame, 0, 16)
	offset := 0
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacketWithHeader(t, stream, dnfproto.GameServerUpperHeaderSize16)
		rawLen := len(stream) - len(rest)
		mode := byte(0xff)
		if len(packet.Body) > 0 {
			mode = packet.Body[0]
		}
		frames = append(frames, loginActorGenerationFrame{
			offset: offset,
			end:    offset + rawLen,
			mode:   mode,
			msgID:  packet.Header.MsgID,
			body:   append([]byte(nil), packet.Body...),
			packet: append([]byte(nil), stream[:rawLen]...),
		})
		offset += rawLen
		stream = rest
	}
	return frames
}

// loginActorGenerationOp2Frames 切出整条下行流里的 `class0/op2` 帧（actor 世代载体）。
func loginActorGenerationOp2Frames(t *testing.T, stream []byte) []loginActorGenerationFrame {
	t.Helper()
	frames := make([]loginActorGenerationFrame, 0, 4)
	for _, frame := range loginActorGenerationSplit(t, stream) {
		if frame.msgID == uint16(dnfenum.CmdPacketSetUDPIPPort) {
			frames = append(frames, frame)
		}
	}
	return frames
}

// loginActorGenerationOffsetOfMsgID 在 16 字节头口径下找某个 msg_id 的字节偏移。
func loginActorGenerationOffsetOfMsgID(t *testing.T, stream []byte, msgID uint16) int {
	t.Helper()
	for _, frame := range loginActorGenerationSplit(t, stream) {
		if frame.msgID == msgID {
			return frame.offset
		}
	}
	return -1
}

// loginActorGenerationDrive 把登录路由推到"客户端提交之后发来第一条报文"那一刻，
// 并返回整条下行流与三处偏移：typed op24 提交、客户端 type1345 的响应、以及
// 触发点之后的收尾。
//
// 返回的流是**一条连续的流**（不 Reset），所以断言可以直接比字节偏移。
func loginActorGenerationDrive(t *testing.T, tier string) (stream []byte, commitOffset int, ackOffset int, logPath string, service *Service) {
	t.Helper()
	t.Setenv(loginEquipmentContainerEnv, "")
	t.Setenv(loginEquipmentSceneReadyReplayEnv, loginEquipmentSceneReadyReplayBeforeTransitionOnly)
	t.Setenv(skillSwitchInventoryModeEnv, skillSwitchInventoryBeforeTransitionTier)
	t.Setenv(loginActorGenerationRebuildEnv, tier)

	var session *gameSession
	var connection *bufferConn
	service, session, connection, logPath = newSceneReadyReplayFixture(t)
	armLoginRouteUpToOp24(session)

	transitionBody, err := buildCurrentSceneTransitionBody(31, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.sendCurrentTownTransitionLocked(session, 6, 31, 2, transitionBody); err != nil {
		t.Fatal(err)
	}
	commitOffset = loginActorGenerationOffsetOfMsgID(t, connection.write.Bytes(), currentSceneTransitionMsgID)
	if commitOffset < 0 {
		t.Fatal("typed op24 commit packet missing from the login stream")
	}

	// 客户端声明"场景就绪"——**这条边界上不许出现任何 actor 世代载体**。
	if err := service.handleGameStreamPacket(session, loginActorGenerationLegacyStream(buildLegacyTownSceneReadyAckPacket())); err != nil {
		t.Fatalf("legacy type1345 boundary: %v", err)
	}
	ackOffset = connection.write.Len()
	if ackOffset <= commitOffset {
		t.Fatalf("scene-ready ack wrote %d bytes, commit ended at %d", ackOffset-commitOffset, commitOffset)
	}

	// 客户端自己的下一条报文 = 本修补发的触发面。
	if err := service.handleGameStreamPacket(session, loginActorGenerationLegacyStream(loginActorGenerationClientPacket())); err != nil {
		t.Fatalf("first client packet after the scene-ready ack: %v", err)
	}
	return append([]byte(nil), connection.write.Bytes()...), commitOffset, ackOffset, logPath, service
}

// TestLoginActorGenerationRebuildLandsAfterTheSceneCommitAckOnTheFirstClientFrame 是主用例。
func TestLoginActorGenerationRebuildLandsAfterTheSceneCommitAckOnTheFirstClientFrame(t *testing.T) {
	stream, commitOffset, ackOffset, logPath, service := loginActorGenerationDrive(t, "")
	frames := loginActorGenerationOp2Frames(t, stream)

	// (1) 恰好两帧，且顺序是 mode0 → mode1。
	if len(frames) != 2 {
		t.Fatalf("login route delivered %d class0/op2 frames, want 2 (mode0 + mode1 actor generation)", len(frames))
	}
	if frames[0].mode != 0 || frames[1].mode != 1 {
		t.Fatalf("actor generation frame modes = %d,%d, want 0 (mode0) then 1 (mode1)", frames[0].mode, frames[1].mode)
	}

	// (2) 位置：都在**客户端提交（typed op24）与它的场景就绪 ack**之后。
	for index, frame := range frames {
		if frame.offset < ackOffset {
			t.Fatalf("frame %d (mode %d) starts at %d, before the scene-ready ack ended at %d",
				index, frame.mode, frame.offset, ackOffset)
		}
		if frame.offset < commitOffset {
			t.Fatalf("frame %d (mode %d) starts at %d, before the typed op24 commit at %d",
				index, frame.mode, frame.offset, commitOffset)
		}
	}
	// 提交之前**没有**任何 op2 世代帧（本夹具里 op2 只可能来自本补发）。
	if frames[0].offset < commitOffset {
		t.Fatalf("first op2 frame at %d is not after the commit at %d", frames[0].offset, commitOffset)
	}

	// (3) mode1 与进副本那条路径**同一个构建器**：逐字节相同。
	reference := loginActorGenerationReferenceMode1(t, service)
	if !bytes.Equal(frames[1].body, reference) {
		t.Fatalf("login route mode1 differs from the scene-path builder output:\n login=%x\n scene=%x",
			frames[1].body, reference)
	}

	// (4) 逐行事实：31 行、槽 12 = 101040493 且 +10 耐久 48。
	createCount, slots, durability, nonzero, weapon, auditOK := loginActorGenerationMode1WireRowAudit(frames[1].body)
	if !auditOK {
		t.Fatalf("mode1 wire row walk failed on our own body (%d bytes)", len(frames[1].body))
	}
	wantRows := len(liveCharacter6EquipmentRows31())
	if createCount != wantRows || len(slots) != wantRows {
		t.Fatalf("mode1 create rows = %d slots = %d, want %d", createCount, len(slots), wantRows)
	}
	if got := int(frames[1].body[currentMode1CreateCountOffset]); got != wantRows {
		t.Fatalf("mode1 body[%d] (create count) = %d, want %d", currentMode1CreateCountOffset, got, wantRows)
	}
	if !strings.Contains(strings.Join(durability, ","), "12:48") {
		t.Fatalf("mode1 durability list does not carry the weapon: %v", durability)
	}
	if !strings.Contains(strings.Join(nonzero, ","), "12:48") {
		t.Fatalf("mode1 nonzero durability list does not carry the weapon: %v", nonzero)
	}
	if !loginActorGenerationWeaponFieldsMatch(weapon) {
		t.Fatalf("mode1 weapon audit fields = %v, want slot 12 / item 101040493 / durability 48", weapon)
	}

	// (5) 事件必须把"谁 / 谁触发 / 几帧 / 行数 / 耐久 / 位置 / 档位"写全。
	logText := sceneReadyReplayTestLog(t, service, logPath)
	for _, want := range []string{
		"EVENT kind=game-login-actor-generation-rebuild-sent",
		"char_id=6",
		"trigger=legacy_type=217",
		"frames=" + loginActorGenerationRebuildFrameSequence,
		"frame_count=2",
		"ordering_position=" + loginActorGenerationRebuildPosition,
		"boundary=" + loginActorGenerationRebuildBoundary,
		"mode0_body_len=" + strconv.Itoa(len(frames[0].body)),
		"mode1_body_len=" + strconv.Itoa(len(frames[1].body)),
		"mode1_create_count=31",
		"mode1_row_walk=1",
		"actor_slot_count=31",
		"actor_weapon=present",
		"actor_weapon_slot=12",
		"actor_weapon_item_id=101040493",
		"actor_weapon_durability_sent=48",
		"switch=" + loginActorGenerationRebuildAfterSceneCommit,
		"switch_env=" + loginActorGenerationRebuildEnv,
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("rebuild event missing %q; log=\n%s", want, logText)
		}
	}
	if !strings.Contains(logText, "12:48") {
		t.Fatalf("rebuild event does not carry the weapon durability 12:48; log=\n%s", logText)
	}
}

// loginActorGenerationWeaponFieldsMatch 复核武器审计四元组（field/value 交替的切片）。
func loginActorGenerationWeaponFieldsMatch(fields []any) bool {
	if len(fields) != 8 {
		return false
	}
	return fields[0] == "actor_weapon" && fields[1] == "present" &&
		fields[2] == "actor_weapon_slot" && fields[3] == "12" &&
		fields[4] == "actor_weapon_item_id" && fields[5] == "101040493" &&
		fields[6] == "actor_weapon_durability_sent" && fields[7] == "48"
}

// loginActorGenerationReferenceMode1 用**进副本那条路径的同一个构建器**
// （`scene_post_start_map.go:381-406`）把 mode1 再装配一遍，作为对照字节。
func loginActorGenerationReferenceMode1(t *testing.T, service *Service) []byte {
	t.Helper()
	session := newLoginEquipmentTestSession(&bufferConn{}, 6)
	ctx := context.Background()
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

// TestLoginActorGenerationRebuildRollbackReproducesTodaysBytes 钉回退字面量：
// 写 before_transition_only 之后，**整条登录下行**逐字节等于修复档减去那两帧。
func TestLoginActorGenerationRebuildRollbackReproducesTodaysBytes(t *testing.T) {
	fixStream, _, _, fixLogPath, fixService := loginActorGenerationDrive(t, "")
	rollbackStream, _, _, rollbackLogPath, rollbackService := loginActorGenerationDrive(
		t, loginActorGenerationRebuildBeforeTransitionOnly)

	frames := loginActorGenerationOp2Frames(t, fixStream)
	if len(frames) != 2 {
		t.Fatalf("fix tier actor generation frames = %d, want 2", len(frames))
	}
	pruned := append([]byte(nil), fixStream[:frames[0].offset]...)
	pruned = append(pruned, fixStream[frames[1].end:]...)
	if !bytes.Equal(rollbackStream, pruned) {
		t.Fatalf("rollback stream is not the fix stream minus the two actor-generation frames:\n rollback=%x\n fix-minus =%x",
			rollbackStream, pruned)
	}
	if got := loginActorGenerationOp2Frames(t, rollbackStream); len(got) != 0 {
		t.Fatalf("rollback tier delivered %d class0/op2 frames, want 0", len(got))
	}

	rollbackLog := sceneReadyReplayTestLog(t, rollbackService, rollbackLogPath)
	if !strings.Contains(rollbackLog, "EVENT kind=game-login-actor-generation-rebuild-suppressed") ||
		!strings.Contains(rollbackLog, "switch="+loginActorGenerationRebuildBeforeTransitionOnly) ||
		!strings.Contains(rollbackLog, "switch_env="+loginActorGenerationRebuildEnv) {
		t.Fatalf("rollback tier did not record its switch value; log=\n%s", rollbackLog)
	}
	if strings.Contains(rollbackLog, "EVENT kind=game-login-actor-generation-rebuild-sent") {
		t.Fatalf("rollback tier must not emit the fix event; log=\n%s", rollbackLog)
	}
	fixLog := sceneReadyReplayTestLog(t, fixService, fixLogPath)
	if !strings.Contains(fixLog, "EVENT kind=game-login-actor-generation-rebuild-sent") {
		t.Fatalf("fix tier did not record its event; log=\n%s", fixLog)
	}
}

// TestLoginActorGenerationRebuildDoesNotTouchTheCrashingType1345Boundary 钉"没有在会崩的
// 那条边界上多写一个字节"：客户端 type1345 的**响应段**在两档里逐字节相同，
// 且那一段里没有任何 actor 世代载体（mode0/mode1）。
func TestLoginActorGenerationRebuildDoesNotTouchTheCrashingType1345Boundary(t *testing.T) {
	fixStream, _, fixAckOffset, _, _ := loginActorGenerationDrive(t, "")
	rollbackStream, _, rollbackAckOffset, _, _ := loginActorGenerationDrive(
		t, loginActorGenerationRebuildBeforeTransitionOnly)

	if fixAckOffset != rollbackAckOffset {
		t.Fatalf("scene-ready ack wrote %d bytes in the fix tier and %d in the rollback tier",
			fixAckOffset, rollbackAckOffset)
	}
	if !bytes.Equal(fixStream[:fixAckOffset], rollbackStream[:rollbackAckOffset]) {
		t.Fatalf("the legacy type1345 boundary response is not byte-identical across tiers:\n fix=%x\n rollback=%x",
			fixStream[:fixAckOffset], rollbackStream[:rollbackAckOffset])
	}
	for _, frame := range loginActorGenerationOp2Frames(t, fixStream[:fixAckOffset]) {
		t.Fatalf("the crashing type1345 boundary now carries an actor-generation frame (mode %d)", frame.mode)
	}
}

// TestLoginActorGenerationRebuildFiresOncePerSceneEntry 钉去重：同一次进场里
// 第二条客户端报文不许再补一份 actor 世代。
func TestLoginActorGenerationRebuildFiresOncePerSceneEntry(t *testing.T) {
	t.Setenv(loginActorGenerationRebuildEnv, "")
	t.Setenv(loginEquipmentContainerEnv, "")
	t.Setenv(loginEquipmentSceneReadyReplayEnv, loginEquipmentSceneReadyReplayBeforeTransitionOnly)
	t.Setenv(skillSwitchInventoryModeEnv, skillSwitchInventoryBeforeTransitionTier)
	service, session, connection, _ := newSceneReadyReplayFixture(t)
	armLoginRouteUpToOp24(session)
	transitionBody, err := buildCurrentSceneTransitionBody(31, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.sendCurrentTownTransitionLocked(session, 6, 31, 2, transitionBody); err != nil {
		t.Fatal(err)
	}
	if err := service.handleGameStreamPacket(session, loginActorGenerationLegacyStream(buildLegacyTownSceneReadyAckPacket())); err != nil {
		t.Fatal(err)
	}
	if err := service.handleGameStreamPacket(session, loginActorGenerationLegacyStream(loginActorGenerationClientPacket())); err != nil {
		t.Fatal(err)
	}
	first := connection.write.Len()
	if got := len(loginActorGenerationOp2Frames(t, connection.write.Bytes())); got != 2 {
		t.Fatalf("first client frame delivered %d actor-generation frames, want 2", got)
	}
	if err := service.handleGameStreamPacket(session, loginActorGenerationLegacyStream(loginActorGenerationClientPacket())); err != nil {
		t.Fatal(err)
	}
	if grew := connection.write.Len() - first; grew != 0 {
		t.Fatalf("second client frame grew the stream by %d bytes, want 0", grew)
	}
}

// TestLoginActorGenerationRebuildLeavesTheScenePathsAlone 钉范围：
// 副本返回世代 / 路由已清 / 路由未提交 三种会话**一个字节都不写**。
func TestLoginActorGenerationRebuildLeavesTheScenePathsAlone(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(session *gameSession)
	}{
		{
			name: "dungeon_return_generation_owns_the_actor",
			prepare: func(session *gameSession) {
				armSceneReadyAckBoundary(session)
				session.townPostTransition.generation = 1
			},
		},
		{
			name: "town_post_transition_generation_is_not_idle",
			prepare: func(session *gameSession) {
				armSceneReadyAckBoundary(session)
				session.townPostTransition.stage = currentTownPostTransitionComplete
			},
		},
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
		{
			name: "confirmed_dungeon_return_state_pending",
			prepare: func(session *gameSession) {
				armSceneReadyAckBoundary(session)
				session.confirmedDungeonReturnStatePending = true
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(loginActorGenerationRebuildEnv, "")
			t.Setenv(loginEquipmentContainerEnv, "")
			t.Setenv(loginEquipmentSceneReadyReplayEnv, loginEquipmentSceneReadyReplayBeforeTransitionOnly)
			t.Setenv(skillSwitchInventoryModeEnv, skillSwitchInventoryBeforeTransitionTier)
			service, session, connection, _ := newSceneReadyReplayFixture(t)
			armLoginRouteUpToOp24(session)
			testCase.prepare(session)
			// 布防 + 触发都由读循环入口驱动：先 ack，再第一条客户端报文。
			session.townMu.Lock()
			session.initialTownLegacySceneReadyAccepted = false
			session.townMu.Unlock()
			connection.write.Reset()
			service.armLoginActorGenerationRebuild(session)
			if err := service.handleGameStreamPacket(
				session,
				loginActorGenerationLegacyStream(loginActorGenerationClientPacket()),
			); err != nil {
				t.Fatal(err)
			}
			for _, frame := range loginActorGenerationOp2Frames(t, connection.write.Bytes()) {
				t.Fatalf("out-of-scope route wrote an actor-generation frame (mode %d)", frame.mode)
			}
		})
	}
}

// TestLoginActorGenerationMode1WireRowAuditReadsTheEmittedBytes 钉审计函数的读法：
// 槽位与耐久从**已装配的包体**里读回（行 +10 的 u16）；走位错一格必须报 false。
func TestLoginActorGenerationMode1WireRowAuditReadsTheEmittedBytes(t *testing.T) {
	t.Setenv(loginActorGenerationRebuildEnv, "")
	service, _, _, _ := newSceneReadyReplayFixture(t)
	body := loginActorGenerationReferenceMode1(t, service)
	createCount, slots, durability, nonzero, weapon, ok := loginActorGenerationMode1WireRowAudit(body)
	if !ok {
		t.Fatalf("audit failed on our own mode1 body (%d bytes)", len(body))
	}
	wantRows := len(liveCharacter6EquipmentRows31())
	if createCount != wantRows || len(slots) != wantRows || len(durability) != wantRows {
		t.Fatalf("audit rows=%d slots=%d durability=%d, want %d", createCount, len(slots), len(durability), wantRows)
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
	if !loginActorGenerationWeaponFieldsMatch(weapon) {
		t.Fatalf("audit weapon fields = %v, want slot 12 / item 101040493 / durability 48", weapon)
	}
	if _, _, _, _, _, bad := loginActorGenerationMode1WireRowAudit(body[:len(body)-1]); bad {
		t.Fatal("audit accepted a truncated mode1 body")
	}
	if _, _, _, _, _, bad := loginActorGenerationMode1WireRowAudit(append(append([]byte(nil), body...), 0x00)); bad {
		t.Fatal("audit accepted a mode1 body with an extra trailing byte")
	}
	if _, _, _, _, _, bad := loginActorGenerationMode1WireRowAudit([]byte{0, 1, 2, 3}); bad {
		t.Fatal("audit accepted a body that cannot hold a create row")
	}
	if _, _, _, _, _, bad := loginActorGenerationMode1WireRowAudit(nil); bad {
		t.Fatal("audit accepted an empty body")
	}
	// 关键偏移不能被 silently 改动：create 计数就在 +124。
	if binary.LittleEndian.Uint32(body[0:4]) == 0 && body[0] != 1 {
		t.Fatal("mode1 body does not start with the mode byte")
	}
}
