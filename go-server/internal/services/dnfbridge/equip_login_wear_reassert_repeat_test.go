package dnfbridge

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	dnfequip "longheng.io/server/internal/modules/dnf/equip"
)

// 本文件钉住 2026-09-28 18:32 CST 部署件上线后操作者报的那条症状
// 「自动卸下在选择角色后没有卸下了 ✅ 但是重新登陆还会卸下 ❌」：
//
//	会话 `game-000006`（角色 6 test78 / 武器 101040493 / 装备槽 12）
//	  10:37:12.990  legacy type=1345 场景就绪 ack ⇒ 开窗 15000 / repeat /
//	                anchor=slot_refresh / per_slot=on / per_slot_window_ms=5000
//	  10:37:14.350  :76836  A 形状 ⇒ noop noopGate=login_wear_reassert_noop（本槽开表）
//	  10:37:17.35x  槽 17 / 18 / 16 的三条同族包 ⇒ noop（family-repeat）
//	  10:37:18.86x  四条反向伴生包 ⇒ noop noopGate=identity_echo
//	  10:37:20.896  :76910  **与 :76836 逐字节同一条陈述** ⇒ 真卸下（:76912 unequip）
//	  10:38:29.707  :77169  同一条陈述再一次 ⇒ 真卸下（:77171）
//
// 两条真卸下的差值分别是 +6.546 s 与 +75.357 s，都超过该槽的 5000 ms 每槽窗口 ⇒
// 每槽闸门把 `LoginWearWindowOpen` 关掉 ⇒ 域侧三条登录闸门惰性 ⇒ 反转端点兜底真落库。
//
// 下面所有 hex 都是 live 原文（`01130029000000` 13 字节头之后的 28 字节 body），
// 一个字段都没改；两条 ACK 也是 live 原文的 11 字节 body。
const (
	// loginWearRepeatRelogAssertHex 是 :76836 / :76910 / :77169 三条**相同**的 op19 body：
	// src=(list0,10,IV=0) count=1 dst=(list3,12,IV=101040493)。
	loginWearRepeatRelogAssertHex = "000a000000000001000000030c006dc1050600000000ffffffff0000"
	// loginWearRepeatRelogCompanionHex 是 :76890 那条反向伴生包（identity_echo 那一族）：
	// src=(list0,10,IV=101040493) dst=(list3,12,IV=0)。
	loginWearRepeatRelogCompanionHex = "000a006dc1050601000000030c000000000000000000ffffffff0000"

	// loginWearRepeatRelogFirstAbsorbDelay 是开窗 → 第一次吸收的现场时距（10:37:12.990→:14.350）。
	loginWearRepeatRelogFirstAbsorbDelay = 1360 * time.Millisecond
	// loginWearRepeatRelogRepeatDelay 是第一次吸收 → :76910 那次真卸下的现场时距。
	// 它比 loginWearReassertEchoIntervalMillis（5000 ms）长 1.55 s —— 这就是缺口本身。
	loginWearRepeatRelogRepeatDelay = 6546 * time.Millisecond
	// loginWearRepeatRelogThirdDelay 是第一次吸收 → :77169 那次真卸下的现场时距。
	loginWearRepeatRelogThirdDelay = 75357 * time.Millisecond

	// loginWearRepeatNoopAckHex 是 live 里 noop 分支的 msg_id=19 应答体（moveValue=0），
	// 原文 `…01000a0000000000030c00`（:76838 / :76926 / :77185 都是这一条）。
	loginWearRepeatNoopAckHex = "01000a0000000000030c00"
	// loginWearRepeatUnequipAckHex 是 live 里真落库卸下的应答体（moveValue=1），
	// 原文 `0113001b000000e65daef0560000000001000a0001000000030c00` 的 body（:76912）。
	loginWearRepeatUnequipAckHex = "01000a0001000000030c00"
)

func loginWearRepeatBody(t *testing.T, hexBody string) []byte {
	t.Helper()
	body, err := hex.DecodeString(hexBody)
	if err != nil {
		t.Fatalf("live body %q is not hex: %v", hexBody, err)
	}
	if len(body) != 28 {
		t.Fatalf("live body %q decodes to %d bytes, want the 28-byte op19 layout", hexBody, len(body))
	}
	return body
}

// loginWearRepeatAck 解一条 live 应答的 11 字节 body（msg_id=19 的上行包里 16 字节头之后）。
func loginWearRepeatAck(t *testing.T, hexBody string) []byte {
	t.Helper()
	body, err := hex.DecodeString(hexBody)
	if err != nil {
		t.Fatalf("live ACK body %q is not hex: %v", hexBody, err)
	}
	if len(body) != 11 {
		t.Fatalf("live ACK body %q decodes to %d bytes, want the 11-byte msg_id=19 body", hexBody, len(body))
	}
	return body
}

// ageSlotEpoch 把该槽的每槽窗口起点推到 age 之前（**只动时钟，不动陈述**）：
// 这样用例证明的是"救回这条包的是陈述相同，不是时钟没过期"。
func (f *loginWearAnchorFixture) ageSlotEpoch(t *testing.T, slot int16, age time.Duration) {
	t.Helper()
	epoch, seen := f.session.equipLoginWearReassertSlots[slot]
	if !seen || !epoch.absorbed {
		t.Fatalf("slot %d has no per-slot re-assert epoch to age", slot)
	}
	if !epoch.statementRecorded {
		t.Fatalf("slot %d epoch recorded no statement", slot)
	}
	epoch.absorbedAt = f.service.gameplayNow().Add(-age)
	f.session.equipLoginWearReassertSlots[slot] = epoch
}

// TestEquipLoginWearRelogRepeatStatementKeepsTheWeapon 是本轮的现场回归本身：
// 与第一次吸收**逐字节同一条陈述**的重复，在该槽每槽窗口过期后（现场 +6.546 s
// 与 +75.357 s 两次）仍然必须判 noop、装备行一个字节不动、ACK 与第一条逐字节相同。
func TestEquipLoginWearRelogRepeatStatementKeepsTheWeapon(t *testing.T) {
	setDefaultTier(t)
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	// 现场开窗（10:37:12.990）之后 1.36 s 收到第一条。
	fixture.service.openEquipLoginWearWindow(fixture.session, "live")
	fixture.session.equipLoginWearWindowOpenedAt = fixture.service.gameplayNow().Add(-loginWearRepeatRelogFirstAbsorbDelay)

	body := loginWearRepeatBody(t, loginWearRepeatRelogAssertHex)
	first := fixture.runOp19(t, body)
	if !first.windowOpen || !strings.Contains(first.result.Reason, "noopGate="+dnfequip.LoginWearReassertNoopGate) {
		t.Fatalf("the first live re-assert = %q (gate=%t), want the shipped noop gate", first.result.Reason, first.windowOpen)
	}
	if got, want := firstPacketBody(t, first.stream), loginWearRepeatAck(t, loginWearRepeatNoopAckHex); !bytes.Equal(got, want) {
		t.Fatalf("first ACK body = %x, want the live noop body %x", got, want)
	}

	// 现场 :76910：距第一次吸收 6.546 s（> 每槽窗口 5000 ms），逐字节同一条陈述。
	fixture.ageSlotEpoch(t, loginWearAnchorWeaponSlot, loginWearRepeatRelogRepeatDelay)
	second := fixture.runOp19(t, body)
	if !second.windowOpen {
		t.Fatal("the byte-identical repeat fell out of the gate at +6.546 s: game_aligned.go would " +
			"hand it to the reversal fallback, which is exactly the live 10:37:20.972190107Z unequip")
	}
	if !strings.Contains(second.result.Reason, "noopGate="+dnfequip.LoginWearReassertNoopGate) {
		t.Fatalf("repeat reason = %q, want the shipped noop gate", second.result.Reason)
	}
	if got, want := firstPacketBody(t, second.stream), loginWearRepeatAck(t, loginWearRepeatNoopAckHex); !bytes.Equal(got, want) {
		t.Fatalf("repeat ACK body = %x, want the live noop body %x (the operator's byte-level check)", got, want)
	}
	if id := fixture.wornItemID(t, loginWearAnchorWeaponSlot); id != loginWearAnchorWeaponItemID {
		t.Fatalf("slot 12 item = %d, want the beamsword %d still worn", id, loginWearAnchorWeaponItemID)
	}
	if slot, inBag := fixture.bagSlotHolding(t, loginWearAnchorWeaponItemID); inBag {
		t.Fatalf("the weapon left the worn slot and landed in bag %s (the live bug)", slot)
	}

	// 现场 :77169：距第一次吸收 75.357 s（会话窗口 15 s 与每槽窗口 5000 ms 都过期），
	// 同一条陈述再一次 —— 靠锚点 + 重复两层一起吸收。
	fixture.session.equipLoginWearWindowOpenedAt = fixture.service.gameplayNow().Add(-loginWearRepeatRelogThirdDelay)
	fixture.ageSlotEpoch(t, loginWearAnchorWeaponSlot, loginWearRepeatRelogThirdDelay)
	third := fixture.runOp19(t, body)
	if !third.windowOpen {
		t.Fatal("the +75.357 s repeat (live :77169 / unequip :77171) is still handed to the owner")
	}
	if !strings.Contains(third.result.Reason, "noopGate="+dnfequip.LoginWearReassertNoopGate) {
		t.Fatalf("third reason = %q, want the shipped noop gate", third.result.Reason)
	}
	if id := fixture.wornItemID(t, loginWearAnchorWeaponSlot); id != loginWearAnchorWeaponItemID {
		t.Fatalf("slot 12 item after the +75 s repeat = %d, want %d still worn", id, loginWearAnchorWeaponItemID)
	}
	if slot, inBag := fixture.bagSlotHolding(t, loginWearAnchorWeaponItemID); inBag {
		t.Fatalf("the +75 s repeat moved the weapon into bag %s", slot)
	}
}

// TestEquipLoginWearRelogRepeatStatementRollbackReproducesTheUnequip 是本条修复的
// **证伪口径**：只把 DNFBRIDGE_EQUIP_LOGIN_REASSERT_REPEAT 写回回退字面量 gate
// （另两个已上线开关保持默认档 —— 现场那一刻生效的正是这两个默认档），同一条 live body
// 必须逐字节复现今天的现场 bug：真落库卸下、ACK body 等于 live :76912 的
// `01000a0001000000030c00`、武器落进背包 0:10。
func TestEquipLoginWearRelogRepeatStatementRollbackReproducesTheUnequip(t *testing.T) {
	setRollbackTier(t)
	// setRollbackTier 把三个字面量一起写了；本用例要的是"只回退本轮新增的这一层"，
	// 因为现场 :76910 到达时另外两层都在**默认档**下工作（会话窗口仍开着：开窗
	// 10:37:12.990 + 15 s = 10:37:27.990 > 10:37:20.896）。
	t.Setenv(loginWearReassertAnchorEnv, "")
	t.Setenv(loginWearReassertPerSlotEnv, "")

	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	fixture.service.openEquipLoginWearWindow(fixture.session, "live")
	fixture.session.equipLoginWearWindowOpenedAt = fixture.service.gameplayNow().Add(-loginWearRepeatRelogFirstAbsorbDelay)

	body := loginWearRepeatBody(t, loginWearRepeatRelogAssertHex)
	if !fixture.service.equipLoginWearFamilyWindowOpenForOp19(fixture.session, body) {
		t.Fatal("the first live re-assert must still be absorbed (the two shipped tiers are untouched)")
	}
	statement, ok := loginWearReassertStatementOf(body)
	if !ok {
		t.Fatal("the live body must decode")
	}
	// 现场 :76910 到达前的账本：该槽本会话已经吸收过一次**同一条**陈述，
	// 且距那次 6.546 s（> 每槽窗口 5000 ms）。
	fixture.session.equipLoginWearReassertSlots[loginWearAnchorWeaponSlot] = loginWearReassertSlotEpoch{
		absorbed:          true,
		absorbedAt:        fixture.service.gameplayNow().Add(-loginWearRepeatRelogRepeatDelay),
		statement:         statement,
		statementRecorded: true,
	}

	released := fixture.runOp19(t, body)
	if released.windowOpen {
		t.Fatalf("the rollback literal (%s=%s) must keep today's release: the per-slot clock is the "+
			"only exit", loginWearReassertRepeatEnv, loginWearReassertRepeatGate)
	}
	if !strings.Contains(released.result.Reason, "equipMove=unequip") {
		t.Fatalf("rollback reason = %q, want the live unequip", released.result.Reason)
	}
	if got, want := firstPacketBody(t, released.stream), loginWearRepeatAck(t, loginWearRepeatUnequipAckHex); !bytes.Equal(got, want) {
		t.Fatalf("rollback ACK body = %x, want the live unequip body %x", got, want)
	}
	if id := fixture.wornItemID(t, loginWearAnchorWeaponSlot); id != 0 {
		t.Fatalf("rollback tier must reproduce the live bug: slot 12 emptied, got item %d", id)
	}
	if _, inBag := fixture.bagSlotHolding(t, loginWearAnchorWeaponItemID); !inBag {
		t.Fatal("rollback tier must reproduce the live bug: the beamsword is in the bag")
	}
}

// TestEquipLoginWearRelogRepeatStatementSwitchMeansWhatItSays 钉住档位字面量的两端：
// 未设置/写错的字串都取修复档；只有显式 gate 及其别名才回退。
func TestEquipLoginWearRelogRepeatStatementSwitchMeansWhatItSays(t *testing.T) {
	setDefaultTier(t)
	cases := []struct {
		raw  string
		want string
	}{
		{"", loginWearReassertRepeatAbsorb},
		{loginWearReassertRepeatAbsorb, loginWearReassertRepeatAbsorb},
		{"ABSORB", loginWearReassertRepeatAbsorb},
		{"  ", loginWearReassertRepeatAbsorb},
		{"typo", loginWearReassertRepeatAbsorb},
		{loginWearReassertRepeatGate, loginWearReassertRepeatGate},
		{"gate ", loginWearReassertRepeatGate},
		{"off", loginWearReassertRepeatGate},
		{"0", loginWearReassertRepeatGate},
		{"false", loginWearReassertRepeatGate},
		{"no", loginWearReassertRepeatGate},
		{"legacy", loginWearReassertRepeatGate},
		{"disable", loginWearReassertRepeatGate},
		{"disabled", loginWearReassertRepeatGate},
		{"none", loginWearReassertRepeatGate},
	}
	for _, testCase := range cases {
		t.Setenv(loginWearReassertRepeatEnv, testCase.raw)
		if got := loginWearReassertRepeatMode(); got != testCase.want {
			t.Fatalf("%s=%q -> %s, want %s", loginWearReassertRepeatEnv, testCase.raw, got, testCase.want)
		}
		if got := (&Service{}).loginWearReassertRepeat(); got != testCase.want {
			t.Fatalf("%s=%q Service %s, want %s", loginWearReassertRepeatEnv, testCase.raw, got, testCase.want)
		}
	}
}

// TestEquipLoginWearRelogRepeatStatementNeedsTheSameStatement 钉住本闸门的**窄**：
// 只有与已吸收那条逐字段相同的重复才继续吸收；字段不同的新陈述仍然按每槽时钟放行。
// 放行之后的含义由**方向判定本身**决定（A 形状 = 复述穿戴态 ⇒ noop，见
// equip/worn_slot_reassert_gate.go），本用例 ② 钉放行、③ 钉回退相位下的旧真卸。
func TestEquipLoginWearRelogRepeatStatementNeedsTheSameStatement(t *testing.T) {
	setDefaultTier(t)
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	fixture.openWindowAgo(t, loginWearAnchorLateDelay)

	absorbed := fixture.runOp19(t, loginWearRepeatBody(t, loginWearRepeatRelogAssertHex))
	if !absorbed.windowOpen {
		t.Fatal("the first live re-assert must be absorbed")
	}
	fixture.ageSlotEpoch(t, loginWearAnchorWeaponSlot, loginWearRepeatRelogRepeatDelay)

	// ① 同一条陈述 ⇒ 继续吸收（本轮修复）。
	repeat := fixture.runOp19(t, loginWearRepeatBody(t, loginWearRepeatRelogAssertHex))
	if !repeat.windowOpen {
		t.Fatal("the identical statement must stay absorbed past the per-slot window")
	}

	// ② 另一条陈述（背包端点换成 0:16）⇒ 每槽时钟说了算 ⇒ 放行到域层。
	//    2026-09-28 第五轮：放行之后的含义由**方向判定本身**决定 —— 客户端点名的正是
	//    那一格穿着的那件、源端又是空的 ⇒ 复述穿戴态 ⇒ noop（worn_slot_reassert），
	//    与窗口无关。每槽档在这里要保证的东西（同一槽的**新陈述**不许被无限期吞掉）
	//    一个字都没变；下面的回退相位证明旧口径下这条放行样本会真卸。
	released := fixture.runOp19(t, loginWearAnchorFamilyBody(0, loginWearAnchorPlayerDropSlot, loginWearAnchorWeaponSlot, loginWearAnchorWeaponItemID))
	if released.windowOpen {
		t.Fatal("a different statement must be released after the per-slot window: otherwise the " +
			"slot layer would swallow every later statement")
	}
	if !strings.Contains(released.result.Reason, "noopGate="+dnfequip.WornSlotReassertGate) {
		t.Fatalf("released reason = %q, want the window-independent %s judgement to own it",
			released.result.Reason, dnfequip.WornSlotReassertGate)
	}
	if id := fixture.wornItemID(t, loginWearAnchorWeaponSlot); id != loginWearAnchorWeaponItemID {
		t.Fatalf("slot 12 item = %d, want %d still worn", id, loginWearAnchorWeaponItemID)
	}

	// ③ 回退相位：方向判定写回回退字面量 ⇒ 同一条放行样本逐字节回到今天的真卸。
	t.Setenv(equipWornSlotReassertSwitchEnv, "off")
	rollback := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	rollback.openWindowAgo(t, loginWearAnchorLateDelay)
	rollbackAbsorbed := rollback.runOp19(t, loginWearRepeatBody(t, loginWearRepeatRelogAssertHex))
	if !rollbackAbsorbed.windowOpen {
		t.Fatal("the rollback phase must first absorb the same statement")
	}
	rollback.ageSlotEpoch(t, loginWearAnchorWeaponSlot, loginWearRepeatRelogRepeatDelay)
	today := rollback.runOp19(t, loginWearAnchorFamilyBody(0, loginWearAnchorPlayerDropSlot, loginWearAnchorWeaponSlot, loginWearAnchorWeaponItemID))
	if today.windowOpen || !strings.Contains(today.result.Reason, "equipMove=unequip") {
		t.Fatalf("rollback phase = %q (window=%t), want today's real unequip", today.result.Reason, today.windowOpen)
	}
	if id := rollback.wornItemID(t, loginWearAnchorWeaponSlot); id != 0 {
		t.Fatalf("rollback phase: slot 12 item = %d, want today's unequip to have gone through", id)
	}
}

// TestEquipLoginWearRelogCompanionKeepsItsOwnGate 钉住同族另外两条闸门不被本档改变：
// 反向伴生包（srcIV=物品、dstIV=0）永远走 identity_echo，与本档无关；形状外的 op19
// （普通背包搬运 / 厂商 B 形状）连本闸门都碰不到。
func TestEquipLoginWearRelogCompanionKeepsItsOwnGate(t *testing.T) {
	setTier := setDefaultTier
	setTier(t)
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	fixture.openWindowAgo(t, loginWearAnchorLateDelay)

	// 反向伴生包不吃窗口 ⇒ 仍然落到域侧 identity_echo（判据一个字没动）。
	if fixture.service.equipLoginWearFamilyWindowOpenForOp19(fixture.session, loginWearRepeatBody(t, loginWearRepeatRelogCompanionHex)) {
		t.Fatal("the reverse companion must keep falling to its own identity_echo gate")
	}
	// 形状外的两条也不吃窗口。
	if fixture.service.equipLoginWearFamilyWindowOpenForOp19(fixture.session, loginWearBridgeOrdinaryBody()) {
		t.Fatal("an ordinary bag op19 must never be treated as a login wear re-assert")
	}
	if fixture.service.equipLoginWearFamilyWindowOpenForOp19(fixture.session, loginWearBridgeEchoBody()) {
		t.Fatal("the vendor echo shape must never be treated as a login wear re-assert")
	}
	// 该族仍然拿得到窗口 —— 说明上面三条都没把它吃掉。
	if !fixture.service.equipLoginWearFamilyWindowOpenForOp19(fixture.session, loginWearRepeatBody(t, loginWearRepeatRelogAssertHex)) {
		t.Fatal("the family shape must still be absorbed after the three shape-scoped packets")
	}
}
