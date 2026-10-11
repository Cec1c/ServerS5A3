package dnfbridge

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
)

// 本轮钉子：读 op19 尾字节 `body[26]`（`TrailingState0`）作为**玩家请求标志位**。
//
//	tail == 1 ⇒ 玩家亲手拖拽 ⇒ 绕过 `worn_slot_reassert` 那一族门禁，按厂商口径真·脱下
//	tail == 0 ⇒ 走现有门禁（窗口 / occurrence / burst），**行为逐字节不变**
//
// 线上现状：客户端注入尚未开启（所有包 tail == 0）⇒ 本次部署**不改变任何线上行为**；
// 第 2 条用例就是这句话的证明。

// flaggedBody 把现场形状 b 的尾字节改成 flag。
func flaggedBody(t *testing.T, body []byte, tail0 byte) []byte {
	t.Helper()
	out := append([]byte(nil), body...)
	if len(out) < 28 {
		t.Fatalf("body too short: %d", len(out))
	}
	out[26] = tail0
	return out
}

// TestOp19PlayerRequestFlagReadsExactlyOne 钉住"只认恰好 1"。
func TestOp19PlayerRequestFlagReadsExactlyOne(t *testing.T) {
	service := &Service{}
	base := loginWearAnchorLiveSlot17Body()
	t.Setenv(op19PlayerFlagEnv, "")
	for _, tail := range []byte{0, 3, 255} {
		if service.op19PlayerRequestFlagged(flaggedBody(t, base, tail)) {
			t.Fatalf("tail=%d must NOT be treated as a player request", tail)
		}
	}
	if !service.op19PlayerRequestFlagged(flaggedBody(t, base, 1)) {
		t.Fatal("tail=1 must be treated as a player request")
	}
	if !service.op19PlayerRequestFlagged(flaggedBody(t, base, op19CurrentUIPlayerFlag)) {
		t.Fatal("the bounded current UI marker must be accepted for an equipment move")
	}
	// 回退档：完全忽略该字节 ⇒ 逐字节等于本轮之前。
	t.Setenv(op19PlayerFlagEnv, "legacy")
	if service.op19PlayerRequestFlagged(flaggedBody(t, base, 1)) {
		t.Fatal("the rollback tier must ignore the flag byte entirely")
	}
}

// TestOp19PlayerRequestFlagTierTable 钉住开关的两档。
func TestOp19PlayerRequestFlagTierTable(t *testing.T) {
	for _, on := range []string{"", "   ", "on", "flag", "read", "anything"} {
		t.Setenv(op19PlayerFlagEnv, on)
		if !op19PlayerFlagEnabled() {
			t.Fatalf("literal %q must keep the flag reader on (default = fix)", on)
		}
	}
	for _, off := range []string{"off", "0", "false", "no", "legacy", "disable", "disabled", "none", "OFF"} {
		t.Setenv(op19PlayerFlagEnv, off)
		if op19PlayerFlagEnabled() {
			t.Fatalf("literal %q must ignore the flag byte (byte-for-byte today)", off)
		}
	}
}

// TestOp19PlayerRequestFlagTailZeroIsByteForByteToday 是**"这次部署没有引入行为变化"的证明**：
// tail==0 的包走原来的窗口/occurrence 通道，返回值与判据一字不差。
func TestOp19PlayerRequestFlagTailZeroIsByteForByteToday(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(wornSlotClaimRetryTierEnv, "")
	t.Setenv(op19PlayerFlagEnv, "")
	service := &Service{}
	session := &gameSession{connID: "flag-zero", selectedCharacterID: 6, equipLoginWearWindowOpenedAt: service.gameplayNow().Add(-3_000_000_000)}
	// 窗口内第一条 ⇒ 吞（1）；同槽第二条 ⇒ 放行（2）—— 与本轮之前完全一致。
	if got := service.noteWornSlotClaimOccurrence(session, loginWearAnchorLiveSlot17Body()); got != 1 {
		t.Fatalf("tail==0 first in-window claim = %d, want 1 (unchanged)", got)
	}
	if got := service.noteWornSlotClaimOccurrence(session, loginWearAnchorLiveSlot17Body()); got != 2 {
		t.Fatalf("tail==0 second claim = %d, want 2 (unchanged)", got)
	}
	// 带标志的包**不参与**这套账本 ⇒ 它不会打乱 tail==0 包的 occurrence（见下一用例）。
}

// TestOp19PlayerRequestFlagBypassesTheReassertGate 是本轮主用例：
// 同一件、同一槽，尾字节 1 ⇒ 真落库 unequip、装备进背包、耐久 48/48 跟着走；
// 尾字节 0 ⇒ 仍按复述吞掉（门禁不变）。
func TestOp19PlayerRequestFlagBypassesTheReassertGate(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(wornSlotClaimRetryTierEnv, "")
	t.Setenv(op19PlayerFlagEnv, "")
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	body := loginWearAnchorLiveSlot17Body()

	// ① tail == 1：玩家亲手发起 ⇒ 一次就脱下。
	flagged := runLoginWearOp19PlayerFlag(t, fixture, flaggedBody(t, body, 1))
	if !strings.Contains(flagged.result.Reason, "equipMove=unequip") {
		t.Fatalf("flagged reason = %q, want a committed unequip", flagged.result.Reason)
	}
	if id := fixture.wornItemID(t, loginWearAnchorArmorSlot17); id != 0 {
		t.Fatalf("after the flagged move slot 17 = %d, want empty", id)
	}
	record, ok, err := fixture.group.Inventory.Load(context.Background(), fixture.characterKey)
	if err != nil || !ok {
		t.Fatalf("inventory load ok=%t err=%v", ok, err)
	}
	found := false
	for _, stack := range record.Slots {
		if stack.ItemID != loginWearAnchorArmorItemID17 {
			continue
		}
		found = true
		if stack.Extra["durability"] != "48" || stack.Extra["max_durability"] != "48" {
			t.Fatalf("unequipped piece durability extras = %+v, want 48/48", stack.Extra)
		}
	}
	if !found {
		t.Fatal("the flagged move must leave the piece in the bag")
	}

	// ② tail == 0（同形状）：仍被复述门禁吞掉，装备留在身上。
	second := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	plain := runLoginWearOp19PlayerFlag(t, second, body)
	if !strings.Contains(plain.result.Reason, "noopGate=") {
		t.Fatalf("tail==0 reason = %q, want the unchanged noop gate", plain.result.Reason)
	}
	if id := second.wornItemID(t, loginWearAnchorArmorSlot17); id != loginWearAnchorArmorItemID17 {
		t.Fatalf("tail==0 must keep slot 17 worn, got %d", id)
	}
}

// TestOp19PlayerRequestFlagOtherMovesUnaffected 钉住"别的 op19 一律不受影响"：
// 金币行、背包互换、宠物搬运用的都是 tail==0 的包，本开关读不到它们。
func TestOp19PlayerRequestFlagOtherMovesUnaffected(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(op19PlayerFlagEnv, "")
	service := &Service{}
	// 金币行：list0 槽 0（钱包行），tail 0 ⇒ 不是玩家请求。
	gold := make([]byte, 28)
	binary.LittleEndian.PutUint32(gold[7:11], 1)
	gold[11] = currentSocketListEquipment
	if service.op19PlayerRequestFlagged(gold) {
		t.Fatal("the gold wallet row must never be read as a player request")
	}
	// 真搬运（穿上：身份写在源端点）tail 0 ⇒ 不是玩家请求（它本来就不进门禁）。
	binary.LittleEndian.PutUint32(gold[3:7], uint32(loginWearAnchorArmorItemID17))
	binary.LittleEndian.PutUint16(gold[12:14], uint16(loginWearAnchorArmorSlot17))
	if service.op19PlayerRequestFlagged(gold) {
		t.Fatal("a real equip move with tail=0 must not be read as a player request")
	}
	// 同一形状但打了标志 ⇒ 是玩家请求（客户端只会在严格形状上打）。
	if !service.op19PlayerRequestFlagged(flaggedBody(t, gold, 1)) {
		t.Fatal("the same shape with tail=1 must be read as a player request")
	}
}

// runLoginWearOp19PlayerFlag 走真实入口（inventory handler），并按 game_aligned.go 的线上填法
// 透传 PlayerRequestFlag（= 尾字节 == 1）与 occurrence（带标志时桥侧直接给 2 = 放行）。
func runLoginWearOp19PlayerFlag(t *testing.T, fixture *loginWearAnchorFixture, body []byte) loginWearRound {
	t.Helper()
	request, err := dnfinventory.DecodeMoveItemspaceRequest(body)
	if err != nil {
		t.Fatalf("decode error = %v", err)
	}
	flagged := request.TrailingState0 == 1
	occurrence := 0
	if flagged {
		occurrence = 2
	}
	fixture.session.equipWornSlotClaimOccurrence = occurrence
	fixture.buffer.write.Reset()
	result, err := dnfinventory.NewHandler().Handle(context.Background(), alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMoveItemspace),
		Body:                body,
		AccountID:           "dnf:10011",
		SelectedCharacterID: fixture.characterID,
		Repositories:        fixture.group,
		EquipmentPlacement:  func(context.Context, alignedcmd.EquipmentPlacementRequest) error { return nil },
		// 与 game_aligned.go 同一个填法：带标志时不打开登录复述窗口、不吸收。
		LoginWearWindowOpen:     false,
		WornSlotClaimOccurrence: occurrence,
		PlayerRequestFlag:       flagged,
	})
	if err != nil {
		t.Fatalf("handle op19 error = %v", err)
	}
	if err := fixture.service.sendAlignedUpperResponses(fixture.session, result); err != nil {
		t.Fatalf("sendAlignedUpperResponses error = %v", err)
	}
	return loginWearRound{result: result}
}
