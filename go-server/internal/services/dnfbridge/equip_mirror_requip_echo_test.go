package dnfbridge

import (
	"context"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
)

// 本轮（甲 + 乙）的钉子：
//
//	甲 = 镜像回穿抑制：我们刚把那件真落库脱下，客户端在窗口内又把同一件穿回去 ⇒ 判
//	     ClientEcho noop（gate=mirror_requip_echo），现场间隔 0.595/0.765/2.412 s；
//	     实测下一档是 57.4 s，所以 4.0 s 的窗口不会误伤"玩家真的想再穿上"。
//	乙 = 有界窗口：窗口（最近一次场景就绪 ack + 15 s）内 = 复述（吞）；
//	     窗口外单发 = 按厂商口径执行真脱下（不再要求拖两次）；批延续仍吞。

// TestMirrorReequipEchoSwallowsTheClientsImmediateReequip 是甲的主用例：
// 先记下"我们要把这件从这一格脱下"（桥侧在判定 apply 时记），紧接着同件同槽的
// 形状 c 必须被抑制；超窗口的必须放行。
func TestMirrorReequipEchoSwallowsTheClientsImmediateReequip(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(mirrorReequipEchoSwitchEnv, "")
	t.Setenv(mirrorReequipEchoWindowEnv, "")
	service := &Service{}
	now := service.gameplayNow()
	claim := loginWearAnchorLiveSlot17Body() // 形状 b：slot=17 item=100270515
	mirror := loginWearBridgeBody(0, loginWearAnchorBagSlot, int32(loginWearAnchorArmorItemID17), currentSocketListEquipment, loginWearAnchorArmorSlot17, 0)

	// ① 判定为 apply 之后的记录 + 0.7 s 内的镜像 ⇒ 命中
	session := &gameSession{connID: "mirror-echo"}
	service.noteWornSlotClaimWillUnequip(session, claim)
	session.mirrorReequipAt = now.Add(-700 * time.Millisecond)
	if !service.mirrorReequipEchoForOp19(session, mirror) {
		t.Fatal("a mirror re-equip 0.7 s after our unequip must be suppressed")
	}
	if !session.mirrorReequipAt.IsZero() {
		t.Fatal("the record must be consumed by the hit")
	}

	// ② 超出窗口（实测下一档 57.4 s）⇒ 放行
	session2 := &gameSession{connID: "mirror-echo-late"}
	service.noteWornSlotClaimWillUnequip(session2, claim)
	session2.mirrorReequipAt = now.Add(-60 * time.Second)
	if service.mirrorReequipEchoForOp19(session2, mirror) {
		t.Fatal("a re-equip 60 s later is a real action, must not be suppressed")
	}

	// ③ 换了装备槽 / 换了物品 ⇒ 不命中
	session3 := &gameSession{connID: "mirror-echo-other-slot"}
	service.noteWornSlotClaimWillUnequip(session3, claim)
	session3.mirrorReequipAt = now.Add(-500 * time.Millisecond)
	otherSlot := loginWearBridgeBody(0, loginWearAnchorBagSlot, int32(loginWearAnchorArmorItemID17), currentSocketListEquipment, 18, 0)
	if service.mirrorReequipEchoForOp19(session3, otherSlot) {
		t.Fatal("a mirror packet naming another worn slot must not be suppressed")
	}

	// ④ 开关回退档 ⇒ 一律不抑制
	t.Setenv(mirrorReequipEchoSwitchEnv, "off")
	session4 := &gameSession{connID: "mirror-echo-off"}
	service.noteWornSlotClaimWillUnequip(session4, claim)
	session4.mirrorReequipAt = now.Add(-700 * time.Millisecond)
	if service.mirrorReequipEchoForOp19(session4, mirror) {
		t.Fatal("the off tier must not suppress anything")
	}
}

// TestMirrorReequipEchoWindowTable 钉住窗口开关的默认值与边界。
func TestMirrorReequipEchoWindowTable(t *testing.T) {
	cases := map[string]time.Duration{
		"":      mirrorReequipEchoWindowMS * time.Millisecond,
		"   ":   mirrorReequipEchoWindowMS * time.Millisecond,
		"abc":   mirrorReequipEchoWindowMS * time.Millisecond,
		"0":     mirrorReequipEchoWindowMS * time.Millisecond,
		"-5":    mirrorReequipEchoWindowMS * time.Millisecond,
		"60001": mirrorReequipEchoWindowMS * time.Millisecond,
		"1500":  1500 * time.Millisecond,
		"60000": 60000 * time.Millisecond,
	}
	for input, want := range cases {
		t.Setenv(mirrorReequipEchoWindowEnv, input)
		if got := mirrorReequipEchoWindow(); got != want {
			t.Fatalf("mirrorReequipEchoWindow(%q) = %v, want %v", input, got, want)
		}
	}
	for _, off := range []string{"off", "0", "false", "no", "legacy", "disable", "disabled", "none", "OFF"} {
		t.Setenv(mirrorReequipEchoSwitchEnv, off)
		if mirrorReequipEchoEnabled() {
			t.Fatalf("switch %q must disable the mirror gate", off)
		}
	}
	for _, on := range []string{"", "on", "1", "anything"} {
		t.Setenv(mirrorReequipEchoSwitchEnv, on)
		if !mirrorReequipEchoEnabled() {
			t.Fatalf("switch %q must keep the mirror gate on", on)
		}
	}
}

// TestWornSlotClaimWindowOutsideAppliesOnTheFirstDrag 是乙的主用例：
// 场景就绪 ack 之后很久（窗口外）的**单发**形状 b ⇒ 第一次就按厂商口径真脱下。
func TestWornSlotClaimWindowOutsideAppliesOnTheFirstDrag(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(wornSlotClaimRetryTierEnv, "")
	service := &Service{}
	session := &gameSession{connID: "window-outside", selectedCharacterID: 6}
	// ack 在 47 s 之前（现场：ack 10:34:44.710 → 手动拖 10:35:31.948 = +47.2 s）
	session.equipLoginWearWindowOpenedAt = service.gameplayNow().Add(-47 * time.Second)
	if got := service.noteWornSlotClaimOccurrence(session, loginWearAnchorLiveSlot17Body()); got != 2 {
		t.Fatalf("a lone claim outside the window must apply (2), got %d", got)
	}
	// 窗口内（ack +3 s，现场自动批 +2.934 s）⇒ 吞
	session2 := &gameSession{connID: "window-inside", selectedCharacterID: 6}
	session2.equipLoginWearWindowOpenedAt = service.gameplayNow().Add(-3 * time.Second)
	if got := service.noteWornSlotClaimOccurrence(session2, loginWearAnchorLiveSlot17Body()); got != 1 {
		t.Fatalf("a claim inside the window must be swallowed (1), got %d", got)
	}
	// 窗口内的第二条（第二道保险）⇒ 放行
	if got := service.noteWornSlotClaimOccurrence(session2, loginWearAnchorLiveSlot17Body()); got != 2 {
		t.Fatalf("the second claim inside the window must apply (2), got %d", got)
	}
}

// TestWornSlotClaimBurstContinuationStaysSwallowedOutsideTheWindow 钉住批延续兜底：
// 同一批（同落点、<0.5 s、换槽）即使落在窗口外也必须继续吞 —— 现场有一批落在 ack +287.5 s。
func TestWornSlotClaimBurstContinuationStaysSwallowedOutsideTheWindow(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(wornSlotClaimRetryTierEnv, "")
	service := &Service{}
	session := &gameSession{connID: "burst-outside", selectedCharacterID: 6}
	session.equipLoginWearWindowOpenedAt = service.gameplayNow().Add(-300 * time.Second)

	// 批的第一条落在窗口外 ⇒ 目前会放行（残余风险，见报告 §13）；这里只钉"后续条"。
	first := service.noteWornSlotClaimOccurrence(session, loginWearAnchorLiveSlot17Body())
	second := service.noteWornSlotClaimOccurrence(session, loginWearBridgeBody(0, loginWearAnchorBagSlot, 0, currentSocketListEquipment, 16, 100120544))
	if first != 2 {
		t.Fatalf("burst first packet outside the window = %d, want 2 (documented residual risk)", first)
	}
	if second != 1 {
		t.Fatalf("burst continuation must stay swallowed (1), got %d", second)
	}
}

// TestMirrorReequipEchoStopsTheLiveOscillation 是本轮的端到端主用例（= 操作者验收路径）：
// 同一件、同一槽：先按窗口外单发真落库脱下，紧接着的同件同槽镜像包必须判
// gate=mirror_requip_echo 的 noop，且**不再**出现 equipMove=equip。
func TestMirrorReequipEchoStopsTheLiveOscillation(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(wornSlotClaimRetryTierEnv, "")
	t.Setenv(mirrorReequipEchoSwitchEnv, "")
	t.Setenv(mirrorReequipEchoWindowEnv, "")
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)

	// ① 手动拖（窗口外单发）⇒ 真落库脱下
	unequip := runLoginWearOp19WithClaim(t, fixture, loginWearAnchorLiveSlot17Body(), 2)
	if !strings.Contains(unequip.result.Reason, "equipMove=unequip") {
		t.Fatalf("unequip reason = %q", unequip.result.Reason)
	}
	if id := fixture.wornItemID(t, loginWearAnchorArmorSlot17); id != 0 {
		t.Fatalf("slot 17 = %d, want empty after the unequip", id)
	}

	// ② 客户端 0.7 s 后的镜像回穿（形状 c，同件同槽）⇒ 必须 noop，物品留在背包
	mirror := loginWearBridgeBody(0, loginWearAnchorBagSlot, int32(loginWearAnchorArmorItemID17), currentSocketListEquipment, loginWearAnchorArmorSlot17, 0)
	fixture.session.mirrorReequipItemID = int32(loginWearAnchorArmorItemID17)
	fixture.session.mirrorReequipSlot = loginWearAnchorArmorSlot17
	fixture.session.mirrorReequipAt = fixture.service.gameplayNow().Add(-700 * time.Millisecond)
	echo := runLoginWearOp19MirrorEcho(t, fixture, mirror)
	if !strings.Contains(echo.result.Reason, "equipMove=noop") ||
		!strings.Contains(echo.result.Reason, "noopGate=mirror_requip_echo") {
		t.Fatalf("mirror packet reason = %q, want the mirror_requip_echo noop", echo.result.Reason)
	}
	if id := fixture.wornItemID(t, loginWearAnchorArmorSlot17); id != 0 {
		t.Fatalf("after the mirror noop slot 17 = %d, want still empty (the piece must stay in the bag)", id)
	}
	if _, inBag := fixture.bagSlotHolding(t, loginWearAnchorArmorItemID17); !inBag {
		t.Fatal("the piece must still be in the bag after the mirror noop")
	}
}

// runLoginWearOp19MirrorEcho 与 runLoginWearOp19WithClaim 同形，只透传 MirrorReequipEcho
// （= game_aligned.go 线上真实填法）。
func runLoginWearOp19MirrorEcho(t *testing.T, fixture *loginWearAnchorFixture, body []byte) loginWearRound {
	t.Helper()
	fixture.buffer.write.Reset()
	result, err := dnfinventory.NewHandler().Handle(context.Background(), alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMoveItemspace),
		Body:                body,
		AccountID:           "dnf:10011",
		SelectedCharacterID: fixture.characterID,
		Repositories:        fixture.group,
		EquipmentPlacement:  func(context.Context, alignedcmd.EquipmentPlacementRequest) error { return nil },
		MirrorReequipEcho:   true,
	})
	if err != nil {
		t.Fatalf("handle op19 error = %v", err)
	}
	if err := fixture.service.sendAlignedUpperResponses(fixture.session, result); err != nil {
		t.Fatalf("sendAlignedUpperResponses error = %v", err)
	}
	return loginWearRound{result: result}
}
