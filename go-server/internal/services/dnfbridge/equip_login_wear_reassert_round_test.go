package dnfbridge

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
)

// loginWearRoundDuplicateIdentityGate 是域侧"双端点同名"闸门的具名值
// （equip/owner.go:1059 的 equipmentDuplicateEndpointIdentityGate，未导出；
// 现场计数用同一个字面量）。本层不许改它的判据，这里只用它做回归断言。
const loginWearRoundDuplicateIdentityGate = "duplicate_endpoint_identity"

// 本文件钉住 2026-09-28 20:29-20:32 CST（= 2026-09-28T12:29-12:32Z）那台部署件上
// 操作者复报的「武器还是会卸下」三条现场 op19，以及第四层收口（轮次）的两侧。
//
// 现场（`/opt/dnf90/logs/packet_log.txt`，角色 6 test78 / 账号 dnf:10011）：
//
//	会话 game-000006（10:36:55.400 连接，10:37:12.990 场景就绪 ack）
//	  :76836  10:37:14.350  A 形状 src=(0,10,IV=0) dst=(3,12,IV=101040493) ⇒ noop（本槽开表）
//	  :76838  noopGate=login_wear_reassert_noop changed=false
//	  :76910  10:37:20.896  **与 :76836 逐字节相同的 body**（+6.546 s > 每槽 5000 ms）⇒ 真卸下
//	  :76912  equipMove=unequip requestA=(0,10) requestB=(3,12) item=101040493 changed=true
//	  :77169  10:38:29.707  同一条 body 再一次（+75.357 s）⇒ 真卸下（:77171）
//
//	会话 game-000009（10:38:42.230 连接 / 10:38:55.128 / 10:39:29.599 / 10:41:35.780 三次 ack）
//	  :77924  10:41:01.145  A 形状 src=(**1,142**,IV=0) dst=(3,10,IV=101600023)
//	          距本轮的 ack +91.5 s（会话窗口外）⇒ 靠每槽锚点吸收 ⇒ noop
//	  :78228  10:41:41.750  同一句，点名的空端点换成 **143**；距**新一轮** ack +5.97 s
//	          （会话窗口**之内**）却被上一轮的每槽时钟否决 ⇒ 真卸下（:78230）
//
// 三条 op19 的 body 与 item/槽位全部逐字节来自 live（41 字节整包 = 13 字节头 + 28 字节 body）。
const (
	// loginWearRoundLiveAssertSlot12HexFirst 是 :76836（该槽第一条、被吸收的那条）。
	loginWearRoundLiveAssertSlot12HexFirst = "01130029000000e643a3e61e00000a000000000001000000030c006dc1050600000000ffffffff0000"
	// loginWearRoundLiveAssertSlot12HexRepeat 是 :76910（+6.546 s 那次真卸下）。
	loginWearRoundLiveAssertSlot12HexRepeat = "01130029000000162069b12b00000a000000000001000000030c006dc1050600000000ffffffff0000"
	// loginWearRoundLiveAssertSlot12HexThird 是 :77169（+75.357 s 那次真卸下）。
	loginWearRoundLiveAssertSlot12HexThird = "011300290000007dbb31ed5a00000a000000000001000000030c006dc1050600000000ffffffff0000"

	// loginWearRoundLiveSlot10HexFirst 是 :77924（被每槽锚点吸收的那条，点名空端点 142）。
	loginWearRoundLiveSlot10HexFirst = "01130029000000671ac08b4800018e000000000001000000030a00174b0e0600000000ffffffff0000"
	// loginWearRoundLiveSlot10HexRotated 是 :78228（只把点名空端点换成 143，现场真卸下的那条）。
	loginWearRoundLiveSlot10HexRotated = "01130029000000af5825f65f00018f000000000001000000030a00174b0e0600000000ffffffff0000"

	// loginWearRoundLiveSlot10ItemID 是槽 10 那件装备（现场 DB/事件行原文 item=101600023）。
	loginWearRoundLiveSlot10ItemID int64 = 101600023
	// loginWearRoundLiveSlot10AvatarSlot / ...RotatedAvatarSlot 是现场客户端点名的两个空端点。
	loginWearRoundLiveSlot10AvatarSlot        int16 = 142
	loginWearRoundLiveSlot10AvatarSlotRotated int16 = 143

	// 现场时距（只用 live 时间戳之差，见上面的注释）。
	loginWearRoundFirstAbsorbDelay   = 1360 * time.Millisecond
	loginWearRoundSlot12RepeatDelay  = 6546 * time.Millisecond
	loginWearRoundSlot12ThirdDelay   = 75357 * time.Millisecond
	loginWearRoundSlot10LateDelay    = 91500 * time.Millisecond
	loginWearRoundSlot10RotatedDelay = 5970 * time.Millisecond
	// loginWearRoundSlot10RelaunchDelay 是"该槽上一次被吸收 → 换端点重述"的现场间隔：
	// :77924（10:41:01.145）→ :78228（10:41:41.750）= 40.605 s，远大于每槽窗口 5000 ms。
	loginWearRoundSlot10RelaunchDelay = 40605 * time.Millisecond
)

// loginWearRoundLiveFields 是 packet_log 里一条 op19 的 `src=(...) dst=(...)` 字段。
type loginWearRoundLiveFields struct {
	sourceListType           byte
	sourceSlotIndex          int16
	sourceInstanceValue      int32
	moveCount                int32
	destinationListType      byte
	destinationSlotIndex     int16
	destinationInstanceValue int32
}

// loginWearRoundLiveBody 从 live 整包 hex 里取出 28 字节 body，并用**生产解码器**
// （dnfinventory.DecodeMoveItemspaceRequest，与形状判据/陈述判据同一份）断言它逐字段
// 等于 packet_log 里那条 `src=(...) dst=(...)` —— 这样"用 live 原文编码"这件事本身
// 就被钉住了，而不是我手抄一份 body。
func loginWearRoundLiveBody(t *testing.T, packetHex string, want loginWearRoundLiveFields) []byte {
	t.Helper()
	packet, err := hex.DecodeString(packetHex)
	if err != nil {
		t.Fatalf("live packet hex %q is not hex: %v", packetHex, err)
	}
	if len(packet) != 41 {
		t.Fatalf("live packet hex %q = %d bytes, want the live 41-byte op19 packet (13-byte header + 28-byte body)",
			packetHex, len(packet))
	}
	body := packet[13:]
	request, err := dnfinventory.DecodeMoveItemspaceRequest(body)
	if err != nil {
		t.Fatalf("live body %x does not decode: %v", body, err)
	}
	got := loginWearRoundLiveFields{
		sourceListType:           request.SourceListType,
		sourceSlotIndex:          request.SourceSlotIndex,
		sourceInstanceValue:      request.SourceInstanceValue,
		moveCount:                request.MoveCount,
		destinationListType:      request.DestinationListType,
		destinationSlotIndex:     request.DestinationSlotIndex,
		destinationInstanceValue: request.DestinationInstanceValue,
	}
	if got != want {
		t.Fatalf("live packet %s decodes to %+v, want the packet_log fields %+v", packetHex, got, want)
	}
	return body
}

// loginWearRoundSlot12AssertFields / ...RepeatFields 是 :76836 / :76910 的字段（两条相同）。
func loginWearRoundSlot12AssertFields() loginWearRoundLiveFields {
	return loginWearRoundLiveFields{
		sourceListType:           0,
		sourceSlotIndex:          10,
		sourceInstanceValue:      0,
		moveCount:                1,
		destinationListType:      currentSocketListEquipment,
		destinationSlotIndex:     loginWearAnchorWeaponSlot,
		destinationInstanceValue: int32(loginWearAnchorWeaponItemID),
	}
}

// loginWearRoundSlot10Fields 是 :77924 / :78228 的字段（只有点名的空端点不同）。
func loginWearRoundSlot10Fields(avatarSlot int16) loginWearRoundLiveFields {
	return loginWearRoundLiveFields{
		sourceListType:           1,
		sourceSlotIndex:          avatarSlot,
		sourceInstanceValue:      0,
		moveCount:                1,
		destinationListType:      currentSocketListEquipment,
		destinationSlotIndex:     10,
		destinationInstanceValue: int32(loginWearRoundLiveSlot10ItemID),
	}
}

// loginWearRoundLiveWorn 是这两个现场会话的穿戴表：武器槽 12 = 101040493，槽 10 = 101600023。
var loginWearRoundLiveWorn = map[int16]int64{
	loginWearAnchorWeaponSlot: loginWearAnchorWeaponItemID,
	10:                        loginWearRoundLiveSlot10ItemID,
}

// TestEquipLoginWearRoundLiveSlot12RepeatsKeepTheWeapon 钉住 :76836 / :76910 / :77169：
// 三条 live op19（两条逐字节同一 body）在默认档下都必须判 noop、武器一直在槽 12。
func TestEquipLoginWearRoundLiveSlot12RepeatsKeepTheWeapon(t *testing.T) {
	setDefaultTier(t)
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearRoundLiveWorn)
	fixture.service.openEquipLoginWearWindow(fixture.session, "live")
	fixture.session.equipLoginWearWindowOpenedAt = fixture.service.gameplayNow().Add(-loginWearRoundFirstAbsorbDelay)

	firstBody := loginWearRoundLiveBody(t, loginWearRoundLiveAssertSlot12HexFirst, loginWearRoundSlot12AssertFields())
	repeatBody := loginWearRoundLiveBody(t, loginWearRoundLiveAssertSlot12HexRepeat, loginWearRoundSlot12AssertFields())
	thirdBody := loginWearRoundLiveBody(t, loginWearRoundLiveAssertSlot12HexThird, loginWearRoundSlot12AssertFields())

	first := fixture.runOp19(t, firstBody)
	if !first.windowOpen || !strings.Contains(first.result.Reason, "noopGate="+dnfequip.LoginWearReassertNoopGate) {
		t.Fatalf("live :76836 = %q (gate=%t), want the shipped noop gate", first.result.Reason, first.windowOpen)
	}

	// :76910 = 距第一次吸收 6.546 s（> 每槽窗口 5000 ms）的同一条 body。
	fixture.ageSlotEpoch(t, loginWearAnchorWeaponSlot, loginWearRoundSlot12RepeatDelay)
	repeat := fixture.runOp19(t, repeatBody)
	if !repeat.windowOpen {
		t.Fatal("live :76910 (byte-identical repeat at +6.546 s) fell out of the gate: game_aligned.go " +
			"would hand it to the reversal fallback, which is exactly the live 10:37:20.972190107Z unequip")
	}
	if !strings.Contains(repeat.result.Reason, "noopGate="+dnfequip.LoginWearReassertNoopGate) {
		t.Fatalf("live :76910 reason = %q, want the shipped noop gate", repeat.result.Reason)
	}

	// :77169 = +75.357 s 的同一条 body（会话窗口与每槽窗口都过期）。
	fixture.session.equipLoginWearWindowOpenedAt = fixture.service.gameplayNow().Add(-loginWearRoundSlot12ThirdDelay)
	fixture.ageSlotEpoch(t, loginWearAnchorWeaponSlot, loginWearRoundSlot12ThirdDelay)
	third := fixture.runOp19(t, thirdBody)
	if !third.windowOpen {
		t.Fatal("live :77169 (byte-identical repeat at +75.357 s) is still handed to the owner")
	}

	if id := fixture.wornItemID(t, loginWearAnchorWeaponSlot); id != loginWearAnchorWeaponItemID {
		t.Fatalf("slot 12 item = %d, want the beamsword %d still worn", id, loginWearAnchorWeaponItemID)
	}
	if slot, inBag := fixture.bagSlotHolding(t, loginWearAnchorWeaponItemID); inBag {
		t.Fatalf("the beamsword left the worn slot and landed in %s (the live bug)", slot)
	}
}

// TestEquipLoginWearRoundLiveSlot10RelaunchKeepsTheItem 是**本轮修复的现场回归本身**：
// 客户端又进了一次场景（第三次 legacy type=1345 ack）之后，把同一句
// "槽 10 上是 101600023" 里的**点名空端点**从 142 换成 143 —— 这一条在 fixes 前
// 真落库卸下（live :78230），在默认档下必须判 noop、槽 10 一个字节不动。
func TestEquipLoginWearRoundLiveSlot10RelaunchKeepsTheItem(t *testing.T) {
	setDefaultTier(t)
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearRoundLiveWorn)
	logger, logPath := newPeerEndpointTestLogger(t)
	fixture.service.packetLog = logger

	// 第 1 轮（live 10:38:55.128 的 ack）：该轮里被吸收的是武器槽 12 那条。
	fixture.service.openEquipLoginWearWindow(fixture.session, "live")
	fixture.session.equipLoginWearWindowOpenedAt = fixture.service.gameplayNow().Add(-1270 * time.Millisecond)
	if round := fixture.runOp19(t, loginWearRoundLiveBody(t, loginWearRoundLiveAssertSlot12HexFirst, loginWearRoundSlot12AssertFields())); !round.windowOpen {
		t.Fatal("the round-1 weapon-slot re-assert must be absorbed")
	}

	// 第 2 轮（live 10:39:29.599 的 ack）：槽 10 那条在 +91.5 s 到达（会话窗口外）⇒
	// 靠每槽锚点吸收 —— 这就是"窗口关闭后才到的**第一条**同族包"那一类。
	fixture.service.openEquipLoginWearWindow(fixture.session, "live")
	fixture.session.equipLoginWearWindowOpenedAt = fixture.service.gameplayNow().Add(-loginWearRoundSlot10LateDelay)
	lateBody := loginWearRoundLiveBody(t, loginWearRoundLiveSlot10HexFirst, loginWearRoundSlot10Fields(loginWearRoundLiveSlot10AvatarSlot))
	late := fixture.runOp19(t, lateBody)
	if !late.windowOpen || !strings.Contains(late.result.Reason, "noopGate="+dnfequip.LoginWearReassertNoopGate) {
		t.Fatalf("live :77924 (first family packet of its slot, +91.5 s past the window) = %q (gate=%t), "+
			"want the slot-anchor absorption", late.result.Reason, late.windowOpen)
	}

	// 第 3 轮（live 10:41:35.780 的 ack）：+5.97 s、会话窗口**之内**、点名的空端点换成 143。
	// 该槽的表是**上一轮**（+40.605 s 之前）开的，每槽窗口早过 —— 因此这一条只有本层能拦。
	rotatedBody := loginWearRoundLiveBody(t, loginWearRoundLiveSlot10HexRotated, loginWearRoundSlot10Fields(loginWearRoundLiveSlot10AvatarSlotRotated))
	rotatedStatement, ok := loginWearReassertStatementOf(rotatedBody)
	if !ok {
		t.Fatal("the live :78228 body must decode")
	}
	lateStatement, ok := loginWearReassertStatementOf(lateBody)
	if !ok {
		t.Fatal("the live :77924 body must decode")
	}
	// 逐字段说明这两条**只差点名的空端点**：第三层的"同一条陈述"判据因此对它是惰性的。
	if lateStatement == rotatedStatement {
		t.Fatal("the two live slot-10 packets must differ (that is why the repeat tier cannot see them)")
	}
	if lateStatement.sourceSlotIndex != loginWearRoundLiveSlot10AvatarSlot ||
		rotatedStatement.sourceSlotIndex != loginWearRoundLiveSlot10AvatarSlotRotated {
		t.Fatalf("live nomination slots = %d / %d, want 142 / 143",
			lateStatement.sourceSlotIndex, rotatedStatement.sourceSlotIndex)
	}
	if loginWearReassertWornIdentityOf(lateStatement) != loginWearReassertWornIdentityOf(rotatedStatement) {
		t.Fatalf("the two live slot-10 statements must share the worn-endpoint identity: %+v vs %+v",
			loginWearReassertWornIdentityOf(lateStatement), loginWearReassertWornIdentityOf(rotatedStatement))
	}

	fixture.service.openEquipLoginWearWindow(fixture.session, "live")
	fixture.session.equipLoginWearWindowOpenedAt = fixture.service.gameplayNow().Add(-loginWearRoundSlot10RotatedDelay)
	fixture.ageSlotEpoch(t, 10, loginWearRoundSlot10RelaunchDelay)
	rotated := fixture.runOp19(t, rotatedBody)
	if !rotated.windowOpen {
		t.Fatal("live :78228 (same worn-endpoint statement restated in a new scene-entry round) fell out " +
			"of the gate: game_aligned.go hands it to equip/owner.go's reversal fallback, which is " +
			"exactly the live 10:41:41.829858574Z unequip of 101600023")
	}
	if !strings.Contains(rotated.result.Reason, "noopGate="+dnfequip.LoginWearReassertNoopGate) {
		t.Fatalf("live :78228 reason = %q, want the shipped noop gate", rotated.result.Reason)
	}
	if id := fixture.wornItemID(t, 10); id != loginWearRoundLiveSlot10ItemID {
		t.Fatalf("slot 10 item = %d, want %d still worn", id, loginWearRoundLiveSlot10ItemID)
	}
	if slot, inBag := fixture.bagSlotHolding(t, loginWearRoundLiveSlot10ItemID); inBag {
		t.Fatalf("the slot-10 item left the worn slot and landed in %s (the live :78230 bug)", slot)
	}
	if id := fixture.wornItemID(t, loginWearAnchorWeaponSlot); id != loginWearAnchorWeaponItemID {
		t.Fatalf("the weapon left slot 12 in the same session: item = %d", id)
	}

	// 操作者复验的那一行：本条修复必须留下具名事件（下一次实测只需 grep 它）。
	logged := readPeerEndpointTestLog(t, logger, logPath)
	const wantEvent = "EVENT kind=game-equip-login-wear-round-reassert-absorbed"
	if !strings.Contains(logged, wantEvent) {
		t.Fatalf("packet log has no %q line:\n%s", wantEvent, logged)
	}
	for _, want := range []string{
		"reason=same_worn_endpoint_statement_restated_in_a_new_scene_entry_round",
		"round_mode=" + loginWearReassertRoundAbsorb,
		"worn_slot=10",
		"round=3",
		"absorbed_round=2",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("the round-absorption event line is missing %q:\n%s", want, logged)
		}
	}
}

// TestEquipLoginWearRoundRollbackReproducesTheLiveUnequip 是本条修复的**证伪口径**：
// 只把 DNFBRIDGE_EQUIP_LOGIN_REASSERT_ROUND 写回回退字面量 session（另三个开关保持默认档
// —— 现场那一刻生效的正是那三个默认档），live :78228 必须逐字节复现今天的现场 bug：
// 真落库卸下、装备进背包。
func TestEquipLoginWearRoundRollbackReproducesTheLiveUnequip(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(loginWearReassertRoundEnv, loginWearReassertRoundSession)
	// 2026-09-28 第五轮：轮次层的释放**不再是**这条包变成真卸下的充分条件 ——
	// 方向判定（equip/worn_slot_reassert_gate.go，默认开启、与窗口无关）现在会把它判成
	// 复述穿戴态。要复现现场 `:78228` 那次真卸，必须把方向判定也写回回退字面量。
	t.Setenv(equipWornSlotReassertSwitchEnv, "off")

	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearRoundLiveWorn)
	logger, logPath := newPeerEndpointTestLogger(t)
	fixture.service.packetLog = logger
	fixture.service.openEquipLoginWearWindow(fixture.session, "live")
	fixture.session.equipLoginWearWindowOpenedAt = fixture.service.gameplayNow().Add(-1270 * time.Millisecond)
	if round := fixture.runOp19(t, loginWearRoundLiveBody(t, loginWearRoundLiveAssertSlot12HexFirst, loginWearRoundSlot12AssertFields())); !round.windowOpen {
		t.Fatal("the round-1 weapon-slot re-assert must be absorbed in the rollback tier too")
	}

	fixture.service.openEquipLoginWearWindow(fixture.session, "live")
	fixture.session.equipLoginWearWindowOpenedAt = fixture.service.gameplayNow().Add(-loginWearRoundSlot10LateDelay)
	lateBody := loginWearRoundLiveBody(t, loginWearRoundLiveSlot10HexFirst, loginWearRoundSlot10Fields(loginWearRoundLiveSlot10AvatarSlot))
	if late := fixture.runOp19(t, lateBody); !late.windowOpen {
		t.Fatal("live :77924 must stay absorbed in the rollback tier (the slot anchor is a default tier)")
	}

	fixture.service.openEquipLoginWearWindow(fixture.session, "live")
	fixture.session.equipLoginWearWindowOpenedAt = fixture.service.gameplayNow().Add(-loginWearRoundSlot10RotatedDelay)
	fixture.ageSlotEpoch(t, 10, loginWearRoundSlot10RelaunchDelay)
	rotatedBody := loginWearRoundLiveBody(t, loginWearRoundLiveSlot10HexRotated, loginWearRoundSlot10Fields(loginWearRoundLiveSlot10AvatarSlotRotated))
	released := fixture.runOp19(t, rotatedBody)
	if released.windowOpen {
		t.Fatalf("the rollback literal (%s=%s) must keep today's release: the per-slot clock from the "+
			"previous round is the only exit", loginWearReassertRoundEnv, loginWearReassertRoundSession)
	}
	if !strings.Contains(released.result.Reason, "equipMove=unequip") {
		t.Fatalf("rollback reason = %q, want the live unequip", released.result.Reason)
	}
	if id := fixture.wornItemID(t, 10); id != 0 {
		t.Fatalf("rollback tier must reproduce the live bug: slot 10 emptied, got item %d", id)
	}
	if _, inBag := fixture.bagSlotHolding(t, loginWearRoundLiveSlot10ItemID); !inBag {
		t.Fatal("rollback tier must reproduce the live bug: 101600023 is in the bag")
	}
	// 回退档下新事件一行都不许出现（回退必须逐字节等于今天）。
	logged := readPeerEndpointTestLog(t, logger, logPath)
	if strings.Contains(logged, "game-equip-login-wear-round-reassert-absorbed") {
		t.Fatalf("the rollback literal must not emit the round-absorption event:\n%s", logged)
	}
}

// TestEquipLoginWearRoundNeedsANewSceneEntry 钉住本层的**窄**：同一轮里（没有新的场景
// 就绪 ack）把点名空端点换一格仍然是"新意图"，照旧放行到域层，轮次层不许把它一并吞掉。
//
// 2026-09-28 第五轮：放行之后的含义由**方向判定本身**决定 —— A 形状（源端空背包格 +
// 装备端点点名那一格穿着的那件）一律判 noop（equip/worn_slot_reassert_gate.go）；
// 玩家真正的脱下形状是"穿着的那件在源端装备槽"（见
// TestEquipLoginWearRoundGenuineUnequipStillUnequips 与 equip 包的
// TestWornSlotReassertJudgementKeepsTheGenuineUnequipShape）。本用例因此钉
// "放行 + 由方向判定具名"，并保留回退相位证明旧口径下这条放行样本会真卸。
func TestEquipLoginWearRoundNeedsANewSceneEntry(t *testing.T) {
	setDefaultTier(t)
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearRoundLiveWorn)

	fixture.service.openEquipLoginWearWindow(fixture.session, "live")
	fixture.session.equipLoginWearWindowOpenedAt = fixture.service.gameplayNow().Add(-loginWearRoundSlot10LateDelay)
	lateBody := loginWearRoundLiveBody(t, loginWearRoundLiveSlot10HexFirst, loginWearRoundSlot10Fields(loginWearRoundLiveSlot10AvatarSlot))
	if late := fixture.runOp19(t, lateBody); !late.windowOpen {
		t.Fatal("live :77924 must be absorbed")
	}
	// 这一格被吸收之后，同一轮里时钟一过，另一条陈述（点名端点不同）必须放行：
	// 否则这一层会把该槽之后的所有陈述都吞掉。
	fixture.ageSlotEpoch(t, 10, loginWearRepeatRelogRepeatDelay)

	playerDrop := loginWearAnchorFamilyBody(0, loginWearAnchorPlayerDropSlot, 10, loginWearRoundLiveSlot10ItemID)
	released := fixture.runOp19(t, playerDrop)
	if released.windowOpen {
		t.Fatal("without a new scene-entry round a different nomination must be released: otherwise the " +
			"round layer would swallow every later statement inside the session")
	}
	if !strings.Contains(released.result.Reason, "noopGate="+dnfequip.WornSlotReassertGate) {
		t.Fatalf("released reason = %q, want the window-independent %s judgement to own it",
			released.result.Reason, dnfequip.WornSlotReassertGate)
	}
	if id := fixture.wornItemID(t, 10); id != loginWearRoundLiveSlot10ItemID {
		t.Fatalf("slot 10 item = %d, want %d still worn", id, loginWearRoundLiveSlot10ItemID)
	}

	// 回退相位：方向判定写回回退字面量 ⇒ 同一条放行样本逐字节回到今天的真卸。
	t.Setenv(equipWornSlotReassertSwitchEnv, "off")
	rollback := newLoginWearAnchorFixture(t, "6", 6, loginWearRoundLiveWorn)
	rollback.service.openEquipLoginWearWindow(rollback.session, "live")
	rollback.session.equipLoginWearWindowOpenedAt = rollback.service.gameplayNow().Add(-loginWearRoundSlot10LateDelay)
	if got := rollback.runOp19(t, lateBody); !got.windowOpen {
		t.Fatal("the rollback phase must first absorb the same statement")
	}
	rollback.ageSlotEpoch(t, 10, loginWearRepeatRelogRepeatDelay)
	today := rollback.runOp19(t, playerDrop)
	if today.windowOpen || !strings.Contains(today.result.Reason, "equipMove=unequip") {
		t.Fatalf("rollback phase = %q (window=%t), want today's real unequip", today.result.Reason, today.windowOpen)
	}
	if id := rollback.wornItemID(t, 10); id != 0 {
		t.Fatalf("rollback phase: slot 10 item = %d, want today's unequip to have gone through", id)
	}
}

// TestEquipLoginWearRoundSwitchMeansWhatItSays 钉住档位字面量的两端：未设置/写错的字串
// 都取修复档；只有显式 session（及其别名）才回退。
func TestEquipLoginWearRoundSwitchMeansWhatItSays(t *testing.T) {
	setDefaultTier(t)
	cases := []struct {
		raw  string
		want string
	}{
		{"", loginWearReassertRoundAbsorb},
		{loginWearReassertRoundAbsorb, loginWearReassertRoundAbsorb},
		{"ABSORB", loginWearReassertRoundAbsorb},
		{"  ", loginWearReassertRoundAbsorb},
		{"typo", loginWearReassertRoundAbsorb},
		{loginWearReassertRoundSession, loginWearReassertRoundSession},
		{"session ", loginWearReassertRoundSession},
		{"off", loginWearReassertRoundSession},
		{"0", loginWearReassertRoundSession},
		{"false", loginWearReassertRoundSession},
		{"no", loginWearReassertRoundSession},
		{"legacy", loginWearReassertRoundSession},
		{"disable", loginWearReassertRoundSession},
		{"disabled", loginWearReassertRoundSession},
		{"none", loginWearReassertRoundSession},
	}
	for _, testCase := range cases {
		t.Setenv(loginWearReassertRoundEnv, testCase.raw)
		if got := loginWearReassertRoundMode(); got != testCase.want {
			t.Fatalf("%s=%q -> %s, want %s", loginWearReassertRoundEnv, testCase.raw, got, testCase.want)
		}
		if got := (&Service{}).loginWearReassertRound(); got != testCase.want {
			t.Fatalf("%s=%q Service %s, want %s", loginWearReassertRoundEnv, testCase.raw, got, testCase.want)
		}
	}
}

// TestEquipLoginWearRoundPredicateNeedsTheSameWornEndpoint 钉住判据本身的四条边界
// （单元级，不经过 op19 路由）：
//
//	同一句装备端点 + 新一轮   ⇒ 吸收（本轮修复）；
//	同一句装备端点 + 同一轮   ⇒ 不吸收（玩家的出口/每槽每轮一次）；
//	换一件物品（同槽）        ⇒ 不吸收（新意图）；
//	换一个槽                 ⇒ 不吸收（新意图）；
//	回退档（session）         ⇒ 永不吸收。
func TestEquipLoginWearRoundPredicateNeedsTheSameWornEndpoint(t *testing.T) {
	setDefaultTier(t)
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearRoundLiveWorn)
	session := fixture.session

	absorbed, ok := loginWearReassertStatementOf(loginWearRoundLiveBody(t,
		loginWearRoundLiveSlot10HexFirst, loginWearRoundSlot10Fields(loginWearRoundLiveSlot10AvatarSlot)))
	if !ok {
		t.Fatal("the live :77924 body must decode")
	}
	session.equipLoginWearReassertRound = 2
	session.noteLoginWearReassertSlotAbsorbed(10, fixture.service.gameplayNow(), absorbed)

	rotated, ok := loginWearReassertStatementOf(loginWearRoundLiveBody(t,
		loginWearRoundLiveSlot10HexRotated, loginWearRoundSlot10Fields(loginWearRoundLiveSlot10AvatarSlotRotated)))
	if !ok {
		t.Fatal("the live :78228 body must decode")
	}
	// 同一轮：不重开。
	if session.loginWearReassertSlotRoundReassertAbsorbable(10, rotated) {
		t.Fatal("the same round must not reopen the slot window (one window per slot per scene entry)")
	}
	// 新一轮：重开。
	session.equipLoginWearReassertRound = 3
	if !session.loginWearReassertSlotRoundReassertAbsorbable(10, rotated) {
		t.Fatal("the new scene-entry round must reopen the slot window")
	}
	// 换一件物品（同槽）：不重开。
	otherItem := rotated
	otherItem.destinationInstanceValue = int32(loginWearRoundLiveSlot10ItemID + 1)
	if session.loginWearReassertSlotRoundReassertAbsorbable(10, otherItem) {
		t.Fatal("a different item id in the same worn slot is a new intent, not a restatement")
	}
	// 换一个槽：不重开。
	otherSlot := rotated
	otherSlot.destinationSlotIndex = loginWearAnchorWeaponSlot
	if session.loginWearReassertSlotRoundReassertAbsorbable(10, otherSlot) {
		t.Fatal("a different worn slot is a new intent, not a restatement")
	}
	// 回退档：永不重开。注意轮次先推进一次，模拟"开关在命中当刻才写回回退字面量"。
	t.Setenv(loginWearReassertRoundEnv, loginWearReassertRoundSession)
	session.equipLoginWearReassertRound = 4
	if session.loginWearReassertSlotRoundReassertAbsorbable(10, rotated) {
		t.Fatalf("%s=%s must never reopen the slot window", loginWearReassertRoundEnv, loginWearReassertRoundSession)
	}
	// 零值安全：**没有轮次记账**的账本一律不重开（方向是"宁可少拦"，玩家的出口优先）。
	// 生产路径上不存在这种账本（每槽账本只有 noteLoginWearReassertSlotAbsorbed 会写，
	// 而它只在已经开过窗之后才会被调用）。
	t.Setenv(loginWearReassertRoundEnv, "")
	session.equipLoginWearReassertRound = 5
	noRoundLedger := loginWearReassertSlotEpoch{
		absorbed:          true,
		absorbedAt:        fixture.service.gameplayNow(),
		statement:         absorbed,
		statementRecorded: true,
	}
	session.equipLoginWearReassertSlots[10] = noRoundLedger
	if session.loginWearReassertSlotRoundReassertAbsorbable(10, rotated) {
		t.Fatal("a ledger that recorded no round must not reopen the slot window (fail open toward the player exit)")
	}
}

// TestEquipLoginWearRoundKeepsTheOtherClassesOwnGates 钉住同族另外几类不被本层改变：
// 双端点同名（duplicate_endpoint_identity）与反向伴生包（identity_echo）连本层的形状判据都过不了，
// 它们仍然落到自己的域侧闸门。
func TestEquipLoginWearRoundKeepsTheOtherClassesOwnGates(t *testing.T) {
	setDefaultTier(t)
	// 现场 :75197 的双端点同名整包（src=(0,11,IV=101040493) dst=(3,12,IV=101040493)）。
	const duplicateHex = "01130029000000baaed1b90c01000b006dc1050601000000030c006dc1050600000000ffffffff0000"
	duplicate := loginWearRoundLiveBody(t, duplicateHex, loginWearRoundLiveFields{
		sourceListType:           0,
		sourceSlotIndex:          11,
		sourceInstanceValue:      int32(loginWearAnchorWeaponItemID),
		moveCount:                1,
		destinationListType:      currentSocketListEquipment,
		destinationSlotIndex:     loginWearAnchorWeaponSlot,
		destinationInstanceValue: int32(loginWearAnchorWeaponItemID),
	})
	if loginWearReassertFamilyOp19Shape(duplicate) {
		t.Fatal("the double-ident shape has no source instance 0: it must keep its own duplicate_endpoint_identity gate")
	}

	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearRoundLiveWorn)
	fixture.openWindowAgo(t, loginWearAnchorLateDelay)
	if fixture.service.equipLoginWearFamilyWindowOpenForOp19(fixture.session, duplicate) {
		t.Fatal("the double-ident shape must never be treated as a login wear re-assert")
	}
	if fixture.service.equipLoginWearFamilyWindowOpenForOp19(fixture.session, loginWearRepeatBody(t, loginWearRepeatRelogCompanionHex)) {
		t.Fatal("the reverse companion must keep falling to its own identity_echo gate")
	}
	// 它自己的闸门照旧：同一条 live body 在域侧仍然判 noop（判据一个字没动）。
	round := fixture.runOp19(t, duplicate)
	if !strings.Contains(round.result.Reason, "noopGate="+loginWearRoundDuplicateIdentityGate) {
		t.Fatalf("double-ident reason = %q, want noopGate=%s", round.result.Reason, loginWearRoundDuplicateIdentityGate)
	}
	if id := fixture.wornItemID(t, loginWearAnchorWeaponSlot); id != loginWearAnchorWeaponItemID {
		t.Fatalf("the double-ident live body unequipped the weapon: slot 12 item = %d", id)
	}
	// 该族形状仍然拿得到本轮窗口（说明上面两条没把它吃掉）。
	if !fixture.service.equipLoginWearFamilyWindowOpenForOp19(fixture.session,
		loginWearRoundLiveBody(t, loginWearRoundLiveAssertSlot12HexFirst, loginWearRoundSlot12AssertFields())) {
		t.Fatal("the family shape must still be absorbed after the other two classes")
	}
}

// TestEquipLoginWearRoundGenuineUnequipStillUnequips 把"玩家真脱下必须还能脱下来"
// 显式钉一条端到端用例（走桥+域全链）。
//
// 形状：穿着的那件在**源端装备槽**（list 3 槽 12，实例 = 那件 item id）、目的端是背包槽
// （list 0 槽 16，实例 0）—— 与现场 `:93127` 那次手动穿上同一条口径，只是方向相反。
// 该槽在同一轮里已经吸收过一次复述，但这**不影响**真正的脱下：方向判定只对
// "源端为空的 A 形状"成立（equip/worn_slot_reassert_gate.go）。
//
// 2026-09-28 第五轮：本条原来用的是 A 形状（源端空背包格 + 装备端点点名那件），
// 那是客户端复述穿戴态的形状，现在一律判 noop；真正的脱下形状才是这里钉的东西。
func TestEquipLoginWearRoundGenuineUnequipStillUnequips(t *testing.T) {
	setDefaultTier(t)
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearRoundLiveWorn)
	fixture.service.openEquipLoginWearWindow(fixture.session, "live")
	fixture.session.equipLoginWearWindowOpenedAt = fixture.service.gameplayNow().Add(-loginWearAnchorLateDelay)

	// 该槽本轮的复述已被吸收（现场每一轮都是这样）。
	absorbed := fixture.runOp19(t, loginWearAnchorFamilyBody(0, loginWearAnchorBagSlot, loginWearAnchorWeaponSlot, loginWearAnchorWeaponItemID))
	if !absorbed.windowOpen {
		t.Fatal("the slot's round re-assert must be absorbed first")
	}
	// 玩家把武器拖到背包 0:16：同一轮、时钟已过 ⇒ 真卸下。
	fixture.ageSlotEpoch(t, loginWearAnchorWeaponSlot, loginWearRepeatRelogRepeatDelay)
	genuine := loginWearBridgeBody(currentSocketListEquipment, loginWearAnchorWeaponSlot, int32(loginWearAnchorWeaponItemID),
		loginWearFamilySourceListMain, loginWearAnchorPlayerDropSlot, 0)
	released := fixture.runOp19(t, genuine)
	if released.windowOpen {
		t.Fatal("the player's genuine unequip must reach the owner instead of being absorbed")
	}
	if !strings.Contains(released.result.Reason, "equipMove=unequip") {
		t.Fatalf("genuine unequip reason = %q, want the real unequip", released.result.Reason)
	}
	if id := fixture.wornItemID(t, loginWearAnchorWeaponSlot); id != 0 {
		t.Fatalf("slot 12 item = %d, want the weapon to have left the worn slot", id)
	}
	if _, inBag := fixture.bagSlotHolding(t, loginWearAnchorWeaponItemID); !inBag {
		t.Fatal("the genuine unequip must land the beamsword in the bag")
	}
	// 反向伴生形状（装备端点实例为 0）仍然是 identity_echo 的 noop，不落库。
	fixture2 := newLoginWearAnchorFixture(t, "6", 6, loginWearRoundLiveWorn)
	echo := fixture2.runOp19(t, loginWearBridgeBody(0, loginWearAnchorBagSlot, int32(loginWearAnchorWeaponItemID),
		currentSocketListEquipment, loginWearAnchorWeaponSlot, 0))
	if !strings.Contains(echo.result.Reason, "noopGate=identity_echo") {
		t.Fatalf("reverse identity body reason = %q, want noopGate=identity_echo", echo.result.Reason)
	}
}
