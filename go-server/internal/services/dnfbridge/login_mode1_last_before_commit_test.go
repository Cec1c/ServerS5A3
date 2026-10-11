package dnfbridge

// 本文件钉住 FIX ①（`login_mode1_last_before_commit.go`）的不变量：
// **typed op24 提交之前，最后一条 class0/op2 mode=1 必须是装备承载那一帧**。
//
// 现场机制（客户端自带 trace，pid=3308）：
//   - actor 的已穿戴行数只在 `op2 mode=1` 上变化；
//   - 一条**不带**装备 create 行的 mode1 会把已穿戴清成 0 行（00:09:08.272 rows=0）；
//   - `opcode=24` 不重建（本会话 5 次 op24 之后 rows 一次都没变）。
//
// 三个用例把这条规则钉在**发出去的字节**上：
//  1. 最后一帧不带装备行 ⇒ 强制检查把本会话发过的装备 mode1 **原样**补发为最后一帧；
//  2. 最后一帧本来就带装备行 ⇒ **零字节**（= 今天逐字节，不会因为本改动多出一帧）；
//  3. 回退档（`record_only`）⇒ 即使最后一帧不带装备行也**零字节**（一键回退到今天）。

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
)

// loginMode1LastBeforeCommitFixture 造出"角色 6 / 31 个已穿戴槽"的仓储 + 会话，
// 并返回一帧**真实的**、带 31 条装备 create 行的 mode1 包体（走线上同一台构建器）。
func loginMode1LastBeforeCommitFixture(t *testing.T) (*Service, *gameSession, *bufferConn, string, []byte) {
	t.Helper()
	t.Setenv(loginMode1LastBeforeCommitEnv, "")
	service, session, connection, logPath := newSceneReadyReplayFixture(t)
	armLoginRouteUpToOp24(session)
	ctx := context.Background()
	loadedID, _, character, hasCharacter := service.selectedCharacterForEnter(ctx, session)
	if loadedID != 6 || !hasCharacter {
		t.Fatalf("fixture character load = %d has=%t, want 6/true", loadedID, hasCharacter)
	}
	mode1Body := service.buildCurrentActorBindingMode1BodyForSelectedWithEquipmentInContext(
		ctx,
		session,
		character,
		hasCharacter,
		6,
		0,
		currentTownActorOwnerContext(session),
		true,
	)
	rows, _, _, _, _, _ := loginActorGenerationMode1WireRowAudit(mode1Body)
	if rows != len(liveCharacter6EquipmentRows31()) {
		t.Fatalf("fixture mode1 create rows = %d, want %d", rows, len(liveCharacter6EquipmentRows31()))
	}
	return service, session, connection, logPath, mode1Body
}

// loginMode1WithoutEquipmentRows 造一帧**形状合法但不带装备 create 行**的 mode1
// （= 现场那条 163 字节 binding-only 帧的可判别等价物：count 字节为 0）。
func loginMode1WithoutEquipmentRows() []byte {
	body := make([]byte, currentMode1CreateRowsOffset+8)
	body[0] = 1
	body[currentMode1CreateCountOffset] = 0
	return body
}

// loginMode1Op2FramesIn 切出下行流里的 class0/op2 帧（msg_id = CmdPacketSetUDPIPPort）。
func loginMode1Op2FramesIn(t *testing.T, stream []byte) [][]byte {
	t.Helper()
	frames := make([][]byte, 0, 4)
	for _, frame := range loginActorGenerationSplit(t, stream) {
		if frame.msgID == uint16(dnfenum.CmdPacketSetUDPIPPort) {
			frames = append(frames, append([]byte(nil), frame.body...))
		}
	}
	return frames
}

// loginMode1LastFrameRows 返回下行流里**最后一条** class0/op2 包体的装备 create 行数。
func loginMode1LastFrameRows(t *testing.T, stream []byte) int {
	t.Helper()
	frames := loginMode1Op2FramesIn(t, stream)
	if len(frames) == 0 {
		t.Fatalf("no class0/op2 frame in stream")
	}
	rows, _, _, _, _, _ := loginActorGenerationMode1WireRowAudit(frames[len(frames)-1])
	return rows
}

// TestLoginMode1LastBeforeCommitReassertsTheEquipmentBearingMode1Last - 主用例：
// 最后一帧不带装备行时，提交前补发的最后一帧**逐字节等于**本会话发过的装备 mode1，
// 而且它带满 31 行、槽 12 是武器 101040493 / 耐久 48 / 实例字段与权威行一致。
func TestLoginMode1LastBeforeCommitReassertsTheEquipmentBearingMode1Last(t *testing.T) {
	service, session, connection, logPath, equipmentBody := loginMode1LastBeforeCommitFixture(t)
	service.noteLoginMode1Send(session, equipmentBody, "test_equipment_mode1")
	service.noteLoginMode1Send(session, loginMode1WithoutEquipmentRows(), "test_binding_only_mode1")
	before := len(connection.write.Bytes())

	if err := service.enforceLoginMode1LastBeforeCommit(session, "test_typed_op24", true); err != nil {
		t.Fatal(err)
	}
	stream := connection.write.Bytes()
	if len(stream) <= before {
		t.Fatalf("enforcement wrote no bytes with an equipment-less last mode1; want one re-asserted frame")
	}
	added := stream[before:]
	frames := loginMode1Op2FramesIn(t, added)
	if len(frames) != 1 {
		t.Fatalf("enforcement emitted %d op2 frames, want exactly 1", len(frames))
	}
	if !bytes.Equal(frames[0], equipmentBody) {
		t.Fatalf("re-asserted frame is not byte-identical to the recorded equipment mode1:\n got=%x\nwant=%x",
			frames[0], equipmentBody)
	}
	// 不变量本体：最后一条 op2 是装备承载的那一帧，31 行一个不少。
	if rows := loginMode1LastFrameRows(t, stream); rows != len(liveCharacter6EquipmentRows31()) {
		t.Fatalf("last op2 frame carries %d equipment rows, want %d", rows, len(liveCharacter6EquipmentRows31()))
	}
	// 槽 12：item 101040493 / 耐久 48 / 实例字段（+5..+8）与权威行一致（fixture 用 999999998）。
	rows, slots, durability, _, weapon, ok := loginActorGenerationMode1WireRowAudit(frames[0])
	if !ok || rows != len(liveCharacter6EquipmentRows31()) {
		t.Fatalf("row audit ok=%t rows=%d", ok, rows)
	}
	found := false
	for index, slot := range slots {
		if slot != "12" {
			continue
		}
		found = true
		if durability[index] != "12:48" {
			t.Fatalf("slot 12 durability = %q, want 12:48", durability[index])
		}
	}
	if !found {
		t.Fatalf("slot 12 missing from the re-asserted frame; slots=%v", slots)
	}
	if !strings.Contains(strings.Join(anySliceToStrings(weapon), ","), "101040493") {
		t.Fatalf("weapon facts missing item 101040493: %v", weapon)
	}
	// 事件：动作 + 逐帧顺序（哪一帧是最后一帧）都可 grep。
	log := sceneReadyReplayTestLog(t, service, logPath)
	if !strings.Contains(log, loginMode1LastBeforeCommitAuditEvent) ||
		!strings.Contains(log, "action=reasserted_equipment_mode1_last") {
		t.Fatalf("missing %s action=reasserted_equipment_mode1_last in log:\n%s",
			loginMode1LastBeforeCommitAuditEvent, log)
	}
	if !strings.Contains(log, "mode1_send_order=0:test_equipment_mode1:31:1,1:test_binding_only_mode1:0:0") {
		t.Fatalf("mode1_send_order does not name every mode1 with its row count:\n%s", log)
	}
	if !strings.Contains(log, "last_mode1_reasserted=1") ||
		!strings.Contains(log, "mode1_sends_before_commit=3") {
		t.Fatalf("event must record that the frame which became last is the re-asserted one:\n%s", log)
	}
}

// TestLoginMode1LastBeforeCommitKeepsBytesWhenTheLastMode1IsEquipmentBearing -
// 顺序本来就对的路由：**零字节**（新增帧数为 0 ⇒ 不会让任何已验收路径回退）。
func TestLoginMode1LastBeforeCommitKeepsBytesWhenTheLastMode1IsEquipmentBearing(t *testing.T) {
	service, session, connection, logPath, equipmentBody := loginMode1LastBeforeCommitFixture(t)
	service.noteLoginMode1Send(session, equipmentBody, "test_equipment_mode1")
	before := len(connection.write.Bytes())

	if err := service.enforceLoginMode1LastBeforeCommit(session, "test_typed_op24", true); err != nil {
		t.Fatal(err)
	}
	if got := len(connection.write.Bytes()); got != before {
		t.Fatalf("enforcement wrote %d bytes although the last mode1 was equipment-bearing; want 0",
			got-before)
	}
	log := sceneReadyReplayTestLog(t, service, logPath)
	if !strings.Contains(log, "action=kept_last_mode1_is_equipment_bearing") {
		t.Fatalf("missing kept action in log:\n%s", log)
	}
}

// TestLoginMode1LastBeforeCommitRollbackTierSendsNothing - 一键回退：即使最后一帧
// 不带装备行也**一个字节都不发**（= 今天逐字节），只留档位事件。
func TestLoginMode1LastBeforeCommitRollbackTierSendsNothing(t *testing.T) {
	service, session, connection, logPath, equipmentBody := loginMode1LastBeforeCommitFixture(t)
	t.Setenv(loginMode1LastBeforeCommitEnv, loginMode1LastBeforeCommitRecordOnly)
	service.noteLoginMode1Send(session, equipmentBody, "test_equipment_mode1")
	service.noteLoginMode1Send(session, loginMode1WithoutEquipmentRows(), "test_binding_only_mode1")
	before := len(connection.write.Bytes())

	if err := service.enforceLoginMode1LastBeforeCommit(session, "test_typed_op24", true); err != nil {
		t.Fatal(err)
	}
	if got := len(connection.write.Bytes()); got != before {
		t.Fatalf("rollback tier wrote %d bytes; want 0", got-before)
	}
	log := sceneReadyReplayTestLog(t, service, logPath)
	if !strings.Contains(log, "action=rollback_record_only_no_enforcement") ||
		!strings.Contains(log, "switch="+loginMode1LastBeforeCommitRecordOnly) {
		t.Fatalf("rollback tier event missing/mislabelled:\n%s", log)
	}
}

// anySliceToStrings 只给断言用：把事件字段里的 any 列表渲染成字符串列表。
func anySliceToStrings(values []any) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, fmt.Sprint(value))
	}
	return out
}
