package dnfbridge

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
)

// 本轮（方案 C）：把"脱下装备"那条链路的复述闸门从"一律吞掉"改成
// **同一个装备槽在同一个登录纪元里只吞第 1 条、第 2 条起按厂商口径执行成真·脱下**。
//
// 现场依据与厂商对照见 equip_worn_slot_claim_occurrence.go 的长注；本文件钉四件事：
//  1. 逐槽出现序号账本 + epoch 边界（换角色/重登/场景就绪必须清零）；
//  2. 那行唯一的可证伪抓手 `equip-move/worn-slot-reassert slot=… occurrence=… decision=… epoch=…`；
//  3. 域侧行为：第 1 条 noop（装备留在身上）、第 2 条真落库卸下（装备进背包）；
//  4. 两代回退档：`all` = 今天默认（全吞）、`off` = 今天写 off 的行为（一律真卸下）。

// TestWornSlotClaimOccurrenceCountsPerSlotPerEpoch 钉住账本与 epoch 边界。
//
// epoch = 连接（登录/选角）+ 场景就绪轮次。**连续换角色时第 2 个角色的第 1 条必须算第 1 条**
// —— 否则就会把 09-28"每次重登被卸下"那条老症状带回来。
func TestWornSlotClaimOccurrenceCountsPerSlotPerEpoch(t *testing.T) {
	logger, logPath := newPeerEndpointTestLogger(t)
	service := &Service{packetLog: logger}
	t.Setenv(wornSlotClaimRetryTierEnv, "")
	session := &gameSession{connID: "epoch-test", selectedCharacterID: 6}

	slot17 := loginWearAnchorLiveSlot17Body()
	slot18 := loginWearBridgeBody(0, 21, 0, currentSocketListEquipment, 18, 100220519)

	// 返回值是**域侧契约**（1 = 吞、2 = 放行），不是原始计数；原始计数只在日志的
	// `occurrence=` 字段里（下面逐条断言）。
	// 本会话从未收到场景就绪 ack ⇒ 保守按"窗口内复述"：第 1 条吞，第 2 条起放行。
	for _, want := range []int{1, 2, 2} {
		if got := service.noteWornSlotClaimOccurrence(session, slot17); got != want {
			t.Fatalf("slot 17 decision = %d, want %d", got, want)
		}
	}
	if got := service.noteWornSlotClaimOccurrence(session, slot18); got != 1 {
		t.Fatalf("slot 18 decision = %d, want 1 (每槽独立)", got)
	}

	// 新一轮（场景就绪 ack 自增轮次）⇒ 计数清零。
	session.equipLoginWearReassertRound++
	if got := service.noteWornSlotClaimOccurrence(session, slot17); got != 1 {
		t.Fatalf("decision after a new scene round = %d, want 1", got)
	}

	// 新连接（重登 / 换角色）⇒ 清零。
	session.connID = "epoch-test-second-character"
	if got := service.noteWornSlotClaimOccurrence(session, slot17); got != 1 {
		t.Fatalf("decision on a new connection = %d, want 1", got)
	}

	// 非该族形状（真搬运把身份写在源端点）⇒ 0 = 与今天逐字节一致。
	realMove := loginWearBridgeBody(0, 21, int32(loginWearAnchorArmorItemID17), currentSocketListEquipment, loginWearAnchorArmorSlot17, 0)
	if got := service.noteWornSlotClaimOccurrence(session, realMove); got != 0 {
		t.Fatalf("a real move must not be counted, got %d", got)
	}

	// 那行可证伪抓手：slot / occurrence / decision / reason / epoch 都要出现，
	// 并且**epoch 换轮/换连接后序号回到 1**（老症状的防线）都能从日志里直接读出来。
	logged := readPeerEndpointTestLog(t, logger, logPath)
	for _, want := range []string{
		"equip-move/worn-slot-reassert",
		"slot=17 occurrence=1 decision=swallow reason=window",
		"slot=17 occurrence=2 decision=apply-vendor reason=occurrence",
		"slot=17 occurrence=3 decision=apply-vendor reason=occurrence",
		"slot=18 occurrence=1 decision=swallow",
		"slot=17 occurrence=1 decision=swallow reason=window epoch=epoch-test#1",
		"slot=17 occurrence=1 decision=swallow reason=window epoch=epoch-test-second-character#1",
		"retry_tier=retry",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log is missing %q\n%s", want, logged)
		}
	}
	if strings.Contains(logged, "slot=21") {
		t.Fatalf("a real move was logged as a worn-slot claim:\n%s", logged)
	}
}

// TestWornSlotClaimRetryTierTable 钉住三个档位的取值语义。
func TestWornSlotClaimRetryTierTable(t *testing.T) {
	cases := map[string]string{
		"":            wornSlotClaimTierRetry,
		"   ":         wornSlotClaimTierRetry,
		"whatever":    wornSlotClaimTierRetry,
		"on":          wornSlotClaimTierRetry,
		"retry":       wornSlotClaimTierRetry,
		"RETRY":       wornSlotClaimTierRetry,
		"all":         wornSlotClaimTierSwallowAll,
		"swallow":     wornSlotClaimTierSwallowAll,
		"swallow_all": wornSlotClaimTierSwallowAll,
		"off":         wornSlotClaimTierOff,
		"0":           wornSlotClaimTierOff,
		"legacy":      wornSlotClaimTierOff,
		"OFF":         wornSlotClaimTierOff,
		"disabled":    wornSlotClaimTierOff,
	}
	for input, want := range cases {
		t.Setenv(wornSlotClaimRetryTierEnv, input)
		if got := wornSlotClaimRetryTier(); got != want {
			t.Fatalf("wornSlotClaimRetryTier(%q) = %q, want %q", input, got, want)
		}
	}

	// 非重试档必须**不计数**（返回 0）：all = 保持今天的默认行为，off = 闸门本身惰性。
	service := &Service{}
	session := &gameSession{connID: "tier-test"}
	body := loginWearAnchorLiveSlot17Body()
	for _, tier := range []string{"all", "off"} {
		t.Setenv(wornSlotClaimRetryTierEnv, tier)
		if got := service.noteWornSlotClaimOccurrence(session, body); got != 0 {
			t.Fatalf("tier %q must not count, got %d", tier, got)
		}
	}
}

// TestWornSlotClaimRetryStopsTheLoginWearAbsorption 钉住桥侧：第 1 条仍被登录窗口吸收，
// 第 2 条（重试）不再被吸收 —— 决定权交回域侧。
func TestWornSlotClaimRetryStopsTheLoginWearAbsorption(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(wornSlotClaimRetryTierEnv, "")
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	fixture.openWindowAgo(t, time.Second)
	body := loginWearAnchorLiveSlot17Body()

	fixture.session.equipWornSlotClaimOccurrence = 1
	if !fixture.service.equipLoginWearFamilyWindowOpenForOp19(fixture.session, body) {
		t.Fatal("the first claim must still be absorbed by the login-wear layers")
	}
	fixture.session.equipWornSlotClaimOccurrence = 2
	if fixture.service.equipLoginWearFamilyWindowOpenForOp19(fixture.session, body) {
		t.Fatal("a retry (occurrence >= 2) must not be absorbed any more")
	}
}

// TestWornSlotClaimRetryExecutesTheVendorsUnequip 是本轮的主用例（= 操作者验收路径）：
// 同一个槽连发两条同形包 ⇒ 第 1 条 noop（装备留在身上），第 2 条真落库卸下（装备进背包）。
func TestWornSlotClaimRetryExecutesTheVendorsUnequip(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(wornSlotClaimRetryTierEnv, "")
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	body := loginWearAnchorLiveSlot17Body()

	first := runLoginWearOp19WithClaim(t, fixture, body, 1)
	if !strings.Contains(first.result.Reason, "noopGate=") {
		t.Fatalf("first claim reason = %q, want a noop gate", first.result.Reason)
	}
	if id := fixture.wornItemID(t, loginWearAnchorArmorSlot17); id != loginWearAnchorArmorItemID17 {
		t.Fatalf("after the first claim slot 17 = %d, want %d still worn", id, loginWearAnchorArmorItemID17)
	}
	if _, inBag := fixture.bagSlotHolding(t, loginWearAnchorArmorItemID17); inBag {
		t.Fatal("the first claim must not move the piece into the bag")
	}

	second := runLoginWearOp19WithClaim(t, fixture, body, 2)
	if !strings.Contains(second.result.Reason, "equipMove=unequip") || !strings.Contains(second.result.Reason, "changed=true") {
		t.Fatalf("retry reason = %q, want a committed unequip (vendor reversed move)", second.result.Reason)
	}
	if id := fixture.wornItemID(t, loginWearAnchorArmorSlot17); id != 0 {
		t.Fatalf("after the retry slot 17 = %d, want empty", id)
	}
	if _, inBag := fixture.bagSlotHolding(t, loginWearAnchorArmorItemID17); !inBag {
		t.Fatal("after the retry the piece must be in the bag")
	}
	// 耐久必须跟着走（脱下路径不许把它弄丢）。
	record, ok, err := fixture.group.Inventory.Load(context.Background(), fixture.characterKey)
	if err != nil || !ok {
		t.Fatalf("inventory load ok=%t err=%v", ok, err)
	}
	for _, stack := range record.Slots {
		if stack.ItemID != loginWearAnchorArmorItemID17 {
			continue
		}
		if stack.Extra["durability"] != "48" || stack.Extra["max_durability"] != "48" {
			t.Fatalf("unequipped piece durability extras = %+v, want 48/48", stack.Extra)
		}
		return
	}
	t.Fatal("the unequipped piece is missing from the bag")
}

// TestWornSlotClaimRetryTierAllKeepsTodaysDefault 钉住第一代回退档：
// `DNFBRIDGE_EQUIP_MOVE_WORN_SLOT_REASSERT=all` ⇒ 与今天（09-28 起）的默认行为一致：
// 无论第几条都吞掉，装备永远留在身上。
func TestWornSlotClaimRetryTierAllKeepsTodaysDefault(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(wornSlotClaimRetryTierEnv, "all")
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	body := loginWearAnchorLiveSlot17Body()

	for attempt := 0; attempt < 3; attempt++ {
		round := runLoginWearOp19WithClaim(t, fixture, body, 0) // all 档桥侧不计数
		if !strings.Contains(round.result.Reason, "noopGate=") {
			t.Fatalf("attempt %d reason = %q, want a noop gate", attempt, round.result.Reason)
		}
	}
	if id := fixture.wornItemID(t, loginWearAnchorArmorSlot17); id != loginWearAnchorArmorItemID17 {
		t.Fatalf("slot 17 = %d, want %d still worn under the all tier", id, loginWearAnchorArmorItemID17)
	}
}

// TestWornSlotClaimRetryTierOffKeepsTodaysOffBytes 钉住第二代回退档：
// `DNFBRIDGE_EQUIP_MOVE_WORN_SLOT_REASSERT=off` ⇒ 闸门整体惰性 = 今天写 off 的行为
// （第 1 条就被反转端点兜底执行成真卸下）。
func TestWornSlotClaimRetryTierOffKeepsTodaysOffBytes(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(equipWornSlotReassertSwitchEnv, "off")
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	body := loginWearAnchorLiveSlot17Body()

	round := runLoginWearOp19WithClaim(t, fixture, body, 0)
	if !strings.Contains(round.result.Reason, "equipMove=unequip") {
		t.Fatalf("off tier reason = %q, want today's off behaviour (a committed unequip)", round.result.Reason)
	}
	if id := fixture.wornItemID(t, loginWearAnchorArmorSlot17); id != 0 {
		t.Fatalf("off tier slot 17 = %d, want empty", id)
	}
}

// runLoginWearOp19WithClaim 与 anchor 夹具的 runOp19 同形，只多透传一个
// `WornSlotClaimOccurrence`（= game_aligned.go 线上真实填法）。
func runLoginWearOp19WithClaim(t *testing.T, fixture *loginWearAnchorFixture, body []byte, occurrence int) loginWearRound {
	t.Helper()
	fixture.session.equipWornSlotClaimOccurrence = occurrence
	fixture.buffer.write.Reset()
	windowOpen := fixture.service.equipLoginWearFamilyWindowOpenForOp19(fixture.session, body)
	if occurrence > 1 {
		windowOpen = false
	}
	result, err := dnfinventory.NewHandler().Handle(context.Background(), alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMoveItemspace),
		Body:                body,
		AccountID:           "dnf:10011",
		SelectedCharacterID: fixture.characterID,
		Repositories:        fixture.group,
		EquipmentPlacement:  func(context.Context, alignedcmd.EquipmentPlacementRequest) error { return nil },
		// 与 game_aligned.go 同一个填法。
		LoginWearWindowOpen:          windowOpen,
		LoginWearReassertNoopRefresh: fixture.service.equipLoginWearReassertNoopRefreshRequested(),
		WornSlotClaimOccurrence:      occurrence,
	})
	if err != nil {
		t.Fatalf("handle op19 error = %v", err)
	}
	if !result.Handled || !result.ResponseAllowed {
		t.Fatalf("op19 result = %+v", result)
	}
	if err := fixture.service.sendAlignedUpperResponses(fixture.session, result); err != nil {
		t.Fatalf("sendAlignedUpperResponses error = %v", err)
	}
	return loginWearRound{windowOpen: windowOpen, result: result, stream: append([]byte(nil), fixture.buffer.write.Bytes()...)}
}

// 只读保险：本文件不碰任何线上文件；测试日志是 t.TempDir 下的临时文件。
var _ = os.Getenv
