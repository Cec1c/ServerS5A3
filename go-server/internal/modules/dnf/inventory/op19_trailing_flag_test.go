package inventory

import (
	"encoding/binary"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// 本轮（op19「玩家请求标志位」扫清障碍）的钉子，两侧都要有：
//   ① 作用域：`validateProvenPetEquipmentMove` **只**能被"宠物来源"的 op19 调到
//      ⇒ 装备包（源 list 0/1）永远到不了那条尾字节校验；
//   ② 保险：宠物路径上尾字节按新开关放宽（默认修复档），其余常量一个字不放。
// 现场证据（packet_log.txt 全量）：`tail=(…)` 打印 773 次，**全部 `tail=(0,0)`**，非零 0 次。

// op19Body 造一个 28 字节 op19 body（字段偏移与 request.go:250 一致）。
func op19Body(srcList byte, srcSlot int16, srcIV int32, count int32, dstList byte, dstSlot int16, dstIV int32, tail0, tail1 byte) []byte {
	body := make([]byte, 28)
	body[0] = srcList
	binary.LittleEndian.PutUint16(body[1:3], uint16(srcSlot))
	binary.LittleEndian.PutUint32(body[3:7], uint32(srcIV))
	binary.LittleEndian.PutUint32(body[7:11], uint32(count))
	body[11] = dstList
	binary.LittleEndian.PutUint16(body[12:14], uint16(dstSlot))
	binary.LittleEndian.PutUint32(body[14:18], uint32(dstIV))
	binary.LittleEndian.PutUint32(body[22:26], ^uint32(0)) // 现场尾部 ffffffff
	body[26] = tail0
	body[27] = tail1
	return body
}

func moveCommandFromBody(t *testing.T, body []byte) Command {
	t.Helper()
	parsed, err := DecodeMoveItemspaceRequest(body)
	if err != nil {
		t.Fatalf("DecodeMoveItemspaceRequest error = %v", err)
	}
	return NewMoveCommand(alignedcmd.Request{AccountID: "dnf:1", SelectedCharacterID: 77}, parsed)
}

// TestProvenPetEquipmentMoveScopeIsPetSourceOnly 是**作用域证据**本身：
// 装备"脱下/穿上"的每一种形状都进不了宠物分支 ⇒ 尾字节校验对它们不可达。
func TestProvenPetEquipmentMoveScopeIsPetSourceOnly(t *testing.T) {
	equipmentShapes := []struct {
		name    string
		srcList byte
		srcSlot int16
		srcIV   int32
		dstList byte
		dstSlot int16
		dstIV   int32
	}{
		{"drag-off (live shape b)", listTypeMain, 19, 0, listTypeEquipment, 14, 100070551},
		{"drag-off with player flag", listTypeMain, 19, 0, listTypeEquipment, 14, 100070551},
		{"drag-on (live real move)", listTypeMain, 18, 100070551, listTypeEquipment, 14, 0},
		{"avatar drag-off", listTypeAvatar, 97, 0, listTypeActorWornAlt, 3, 401500013},
	}
	for _, shape := range equipmentShapes {
		cmd := moveCommandFromBody(t, op19Body(shape.srcList, shape.srcSlot, shape.srcIV, 1, shape.dstList, shape.dstSlot, shape.dstIV, 1, 0))
		if isProvenPetEquipmentMove(cmd) {
			t.Fatalf("%s: an equipment move must never be classified as a pet equipment move (cmd=%s)", shape.name, cmd.String())
		}
		// 反证（在**回退档**下看）：**如果**作用域被放宽到装备包，这条带标志的包会被那条校验拒掉
		// —— 所以「尾字节不再校验」这个开关是必要的保险，而不是空改动。
		t.Setenv(op19TrailingFlagEnv, "strict")
		if err := validateProvenPetEquipmentMove(cmd); err == nil {
			t.Fatalf("%s: this shape must still fail the pet constants validation if it ever reached it", shape.name)
		} else if !strings.Contains(err.Error(), "tail=(1,0)") {
			t.Fatalf("%s: pet validation error = %v, want the tail=(1,0) complaint", shape.name, err)
		}
		t.Setenv(op19TrailingFlagEnv, "")
	}

	petShapes := []struct {
		name    string
		srcSlot int16
		dstList byte
		dstSlot int16
	}{
		{"creature worn slot 26", 37, listTypeEquipment, 26},
		{"creature worn slot 26 via alt endpoint", 37, listTypeActorWornAlt, 26},
		{"artifact slot 27", 140, listTypeEquipment, 27},
		{"artifact slot 29", 188, listTypeActorWornAlt, 29},
	}
	for _, shape := range petShapes {
		cmd := moveCommandFromBody(t, op19Body(listTypePet, shape.srcSlot, 0x1234, 1, shape.dstList, shape.dstSlot, 0, 0, 0))
		if !isProvenPetEquipmentMove(cmd) {
			t.Fatalf("%s: a pet equipment move must be classified as one (cmd=%s)", shape.name, cmd.String())
		}
	}
}

// TestOp19TrailingFlagPetMoveTiers 钉住新开关的两档：默认修复档放行尾字节，
// 回退档逐字节等于今天；两档都**不许**放开其它常量。
func TestOp19TrailingFlagPetMoveTiers(t *testing.T) {
	flaggedPetMove := func(tail0, tail1 byte, count int32, destinationStack int32) Command {
		body := op19Body(listTypePet, 37, 0x1234, count, listTypeEquipment, 26, 0, tail0, tail1)
		parsed, err := DecodeMoveItemspaceRequest(body)
		if err != nil {
			t.Fatal(err)
		}
		parsed.DestinationStack = destinationStack
		return NewMoveCommand(alignedcmd.Request{AccountID: "dnf:1", SelectedCharacterID: 77}, parsed)
	}

	// ① 默认（未设）= 修复档：带标志的合法宠物包放行。
	t.Setenv(op19TrailingFlagEnv, "")
	if !op19TrailingFlagTolerated() {
		t.Fatal("the default tier must tolerate the trailing flag")
	}
	if err := validateProvenPetEquipmentMove(flaggedPetMove(1, 0, 1, 0)); err != nil {
		t.Fatalf("default tier rejected a flagged pet move: %v", err)
	}
	if err := validateProvenPetEquipmentMove(flaggedPetMove(0, 1, 1, 0)); err != nil {
		t.Fatalf("default tier rejected a flagged pet move (second tail byte): %v", err)
	}

	// ② 回退档：逐字节等于今天 —— 尾字节非 0 一律拒绝，错误文本相同。
	for _, literal := range []string{"strict", "legacy", "off", "0", "false", "no", "disable", "disabled", "none", "STRICT"} {
		t.Setenv(op19TrailingFlagEnv, literal)
		if op19TrailingFlagTolerated() {
			t.Fatalf("literal %q must select the strict tier", literal)
		}
		err := validateProvenPetEquipmentMove(flaggedPetMove(1, 0, 1, 0))
		if err == nil {
			t.Fatalf("literal %q must reject a flagged pet move", literal)
		}
		if err.Error() != "invalid current-EXE pet op19 constants: count=1 destination_stack=0 tail=(1,0)" {
			t.Fatalf("literal %q error text = %q, want today's text", literal, err.Error())
		}
	}

	// ③ 两档都不放其它常量：count != 1 / destination_stack != 0 仍然拒绝。
	for _, literal := range []string{"", "strict"} {
		t.Setenv(op19TrailingFlagEnv, literal)
		if err := validateProvenPetEquipmentMove(flaggedPetMove(0, 0, 2, 0)); err == nil {
			t.Fatalf("tier %q must still reject count=2", literal)
		}
		if err := validateProvenPetEquipmentMove(flaggedPetMove(0, 0, 1, 1)); err == nil {
			t.Fatalf("tier %q must still reject destination_stack=1", literal)
		}
		badActor := flaggedPetMove(0, 0, 1, 0)
		badActor.ActorIndex = 3
		if err := validateProvenPetEquipmentMove(badActor); err == nil {
			t.Fatalf("tier %q must still reject actor index 3", literal)
		}
	}
}

// TestOp19TrailingFlagEquipmentPacketNeverReachesThePetValidator 是**端到端作用域证据**：
// 一条带标志的装备"脱下"包解码后 tail=1、且**不会被**宠物那条校验拦住（它不在宠物分支里）。
func TestOp19TrailingFlagEquipmentPacketNeverReachesThePetValidator(t *testing.T) {
	body := op19Body(listTypeMain, 19, 0, 1, listTypeEquipment, 14, 100070551, 1, 0)
	parsed, err := DecodeMoveItemspaceRequest(body)
	if err != nil {
		t.Fatalf("decode error = %v", err)
	}
	if parsed.TrailingState0 != 1 || parsed.TrailingState1 != 0 {
		t.Fatalf("decoded tail = (%d,%d), want (1,0) — body[26]/body[27] is the flag byte", parsed.TrailingState0, parsed.TrailingState1)
	}
	cmd := NewMoveCommand(alignedcmd.Request{AccountID: "dnf:1", SelectedCharacterID: 77}, parsed)
	if isProvenPetEquipmentMove(cmd) {
		t.Fatal("the equipment drag-off shape must not enter the pet equipment branch")
	}
	// 也就是说 handler.go:61-65 那段（唯一的调用点）对这条包**根本不会执行** ⇒
	// 客户端在装备包上打标志**不需要**任何服务端改动；本轮的开关只是给"客户端对所有 op19 打标"
	// 之后的宠物路径留的保险。
	if got := cmd.String(); !strings.Contains(got, "tail=(1,0)") {
		t.Fatalf("summary %q must still print the raw tail for evidence", got)
	}
}
