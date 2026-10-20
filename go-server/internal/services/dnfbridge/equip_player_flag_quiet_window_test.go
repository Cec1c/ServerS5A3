package dnfbridge

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
)

// 本轮钉子：op19「玩家请求标志位」的**静默窗**（把"误伤自动突发"的最坏后果从 5 件压到 1 件）。
//
//	tail==1 且其后 quiet_ms 内**没有第二条 op19** ⇒ 按厂商口径执行（玩家亲手发起）
//	tail==1 且 quiet_ms 内**来了第二条** ⇒ 挂起那条按门禁路径吞掉（它属于一串突发）
//	tail==0                                   ⇒ 一个字节都不变（即时处理、不挂起）
//
// 四条边界（操作者点名）：① 超时 ≠ 连接错误 ② 结算后清读截止（会话本来没有截止）
// ③ 挂起期间来新包 ⇒ 先结挂起再处理新包、不丢包 ④ 无 goroutine/定时器 ⇒ 不泄漏不 panic。
//
// 断言口径说明：本文件用**捕获注册表**直接读"桥侧最终透传给域层的请求"
// （仓库既有先例：pet_pvf_catalog_test.go 的捕获 handler）。这样"走了哪条路"是
// **可证伪的字段断言**（PlayerRequestFlag / WornSlotClaimOccurrence / body[26]），
// 不依赖 fixture 的仓储；真正落库那一侧由 §18 的
// `TestOp19PlayerRequestFlagBypassesTheReassertGate`（handler 级、真落库 + 耐久）覆盖。

type bridgeOp19CaptureHandler struct {
	calls   int
	request alignedcmd.Request
	body    []byte
}

func (*bridgeOp19CaptureHandler) Domain() dnfenum.AlignedDomain {
	// 与生产注册表里 op19 的归属保持一致（直接问真 handler，避免抄一个可能过期的常量）。
	return dnfinventory.NewHandler().Domain()
}

func (h *bridgeOp19CaptureHandler) Handle(_ context.Context, request alignedcmd.Request) (alignedcmd.Result, error) {
	h.calls++
	h.request = request
	h.body = append([]byte(nil), request.Body...)
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: false,
		Operation:       "capture_op19",
		Reason:          "test capture",
	}, nil
}

func installOp19Capture(t *testing.T) *bridgeOp19CaptureHandler {
	t.Helper()
	capture := &bridgeOp19CaptureHandler{}
	previous := gameAlignedRegistry
	gameAlignedRegistry = alignedcmd.NewRegistry(capture)
	t.Cleanup(func() { gameAlignedRegistry = previous })
	return capture
}

// TestOp19QuietWindowEnvTable 钉住开关的归一化（含回退字面量 0）。
func TestOp19QuietWindowEnvTable(t *testing.T) {
	cases := map[string]time.Duration{
		"":     400 * time.Millisecond,
		"   ":  400 * time.Millisecond,
		"abc":  400 * time.Millisecond,
		"0":    0,
		"-5":   0,
		"1":    time.Millisecond,
		"400":  400 * time.Millisecond,
		"5000": 5000 * time.Millisecond,
		"9000": 5000 * time.Millisecond, // 上限收敛
	}
	for input, want := range cases {
		t.Setenv(op19PlayerFlagQuietEnv, input)
		if got := op19PlayerFlagQuietWindow(); got != want {
			t.Fatalf("quietWindow(%q) = %v, want %v", input, got, want)
		}
	}
}

// TestOp19QuietWindowDeadlineIsNotAConnectionError 是**边界①**：
// 只有"确实有挂起 + 错误是超时"才判成我们的截止；真正的断线/EOF 一律不是（不误断玩家）。
func TestOp19QuietWindowDeadlineIsNotAConnectionError(t *testing.T) {
	pending := &gameSession{connID: "quiet-deadline", op19QuietPending: []byte{1, 2, 3}}
	idle := &gameSession{connID: "quiet-idle"}

	// timeoutNetError 复用 service_test.go 里的同包声明（它已实现 net.Error）。
	timeoutErr := timeoutNetError{}
	if !isOp19QuietReadDeadline(pending, timeoutErr) {
		t.Fatal("a timeout with a pending packet must be recognised as our own deadline")
	}
	if !isOp19QuietReadDeadline(pending, os.ErrDeadlineExceeded) {
		t.Fatal("os.ErrDeadlineExceeded with a pending packet must be recognised as our own deadline")
	}
	if isOp19QuietReadDeadline(idle, timeoutErr) {
		t.Fatal("without a pending packet a timeout must NOT be treated as our deadline")
	}
	for _, err := range []error{io.EOF, net.ErrClosed, syscall.ECONNRESET, errors.New("boom")} {
		if isOp19QuietReadDeadline(pending, err) {
			t.Fatalf("a genuine read error (%v) must never be treated as our quiet-window deadline", err)
		}
	}
	// 边界②：会话本来没有读截止；结算后必须回到"没有截止"（清掉），不能留下残余。
	clearOp19QuietReadDeadline(nil) // 不 panic
	armOp19QuietReadDeadline(nil, 400*time.Millisecond)
}

// TestOp19QuietWindowPendsThenExecutesAlone 是**主用例正面侧（要求①）**：
// 单发 tail==1 先挂起（不回包、不落库）；静默窗内没有第二条 ⇒ 结算成**玩家请求**
// ⇒ 透传给域层的请求必须带 PlayerRequestFlag=true + occurrence=2（= 绕过门禁）。
func TestOp19QuietWindowPendsThenExecutesAlone(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(wornSlotClaimRetryTierEnv, "")
	t.Setenv(op19PlayerFlagEnv, "")
	t.Setenv(op19PlayerFlagQuietEnv, "")
	capture := installOp19Capture(t)
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	body := flaggedBody(t, loginWearAnchorLiveSlot17Body(), 1)

	fixture.buffer.write.Reset()
	handled, err := fixture.service.handleAlignedGameCommand(fixture.session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMoveItemspace), body)
	if err != nil || !handled {
		t.Fatalf("flagged packet: handled=%t err=%v", handled, err)
	}
	if !fixture.session.hasOp19QuietPending() {
		t.Fatal("the flagged packet must be pended while the quiet window is open")
	}
	if capture.calls != 0 {
		t.Fatalf("a pended packet must not be routed to the domain yet, calls=%d", capture.calls)
	}
	if got := fixture.buffer.write.Len(); got != 0 {
		t.Fatalf("a pended packet must not answer yet, wrote %d bytes", got)
	}

	// 静默窗到期（读循环的超时分支）⇒ 结算为玩家请求。
	fixture.service.resolveOp19QuietPending(fixture.session, true)
	if fixture.session.hasOp19QuietPending() {
		t.Fatal("the pending must be consumed by the resolve")
	}
	if capture.calls != 1 {
		t.Fatalf("the resolve must route exactly one request, calls=%d", capture.calls)
	}
	if !capture.request.PlayerRequestFlag {
		t.Fatal("the resolved player request must carry PlayerRequestFlag=true (otherwise the gate absorbs it)")
	}
	if capture.request.WornSlotClaimOccurrence != 2 {
		t.Fatalf("occurrence = %d, want 2 (gate bypass)", capture.request.WornSlotClaimOccurrence)
	}
	if len(capture.body) < 27 || capture.body[26] != 1 {
		t.Fatalf("the resolved body must keep the flag byte, got tail=%v", capture.body)
	}
}

// TestOp19QuietWindowRollbackLiteralKeepsSection18 是**回退档（要求③的对照）**：
// QUIET_MS=0 ⇒ 不挂起、立即透传 PlayerRequestFlag=true（= §18 那一版逐字节相同）。
func TestOp19QuietWindowRollbackLiteralKeepsSection18(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(wornSlotClaimRetryTierEnv, "")
	t.Setenv(op19PlayerFlagEnv, "")
	t.Setenv(op19PlayerFlagQuietEnv, "0")
	capture := installOp19Capture(t)
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	body := flaggedBody(t, loginWearAnchorLiveSlot17Body(), 1)

	if handled, err := fixture.service.handleAlignedGameCommand(fixture.session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMoveItemspace), body); err != nil || !handled {
		t.Fatalf("rollback tier: handled=%t err=%v", handled, err)
	}
	if fixture.session.hasOp19QuietPending() {
		t.Fatal("the rollback literal must not pend anything (immediate execute = §18)")
	}
	if capture.calls != 1 || !capture.request.PlayerRequestFlag {
		t.Fatalf("rollback tier must route immediately with the flag, calls=%d flag=%t", capture.calls, capture.request.PlayerRequestFlag)
	}
}

// TestOp19QuietWindowSecondPacketSwallowsTheFirst 是**主用例反面侧 + 边界③**：
// 静默窗内来第二条 ⇒ 挂起那条按**门禁路径**处理（尾字节清 0、不带标志、走 occurrence），
// 新包照常处理（继续挂起），一个包都不丢。
func TestOp19QuietWindowSecondPacketSwallowsTheFirst(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(wornSlotClaimRetryTierEnv, "")
	t.Setenv(op19PlayerFlagEnv, "")
	t.Setenv(op19PlayerFlagQuietEnv, "400")
	capture := installOp19Capture(t)
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	first := flaggedBody(t, loginWearAnchorLiveSlot17Body(), 1)

	if handled, err := fixture.service.handleAlignedGameCommand(fixture.session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMoveItemspace), first); err != nil || !handled {
		t.Fatalf("first flagged packet: handled=%t err=%v", handled, err)
	}
	if !fixture.session.hasOp19QuietPending() {
		t.Fatal("the first flagged packet must be pended")
	}

	// 第二条到达 ⇒ 读循环先结挂起（asPlayerRequest=false），再处理本包。
	fixture.service.resolveOp19QuietPending(fixture.session, false)
	if capture.calls != 1 {
		t.Fatalf("resolving as a burst member must route exactly one request, calls=%d", capture.calls)
	}
	if capture.request.PlayerRequestFlag {
		t.Fatal("a burst member must NOT be routed as a player request (that is what caps the loss at 1)")
	}
	if len(capture.body) < 27 || capture.body[26] != 0 {
		t.Fatal("the burst member must be replayed with the flag byte cleared (ordinary gate path)")
	}
	if capture.request.WornSlotClaimOccurrence != 1 {
		t.Fatalf("occurrence = %d, want 1 (it must go through the gate, not around it)", capture.request.WornSlotClaimOccurrence)
	}

	// 第二件（同一突发）⇒ 再次挂起；再结一次 ⇒ 仍然走门禁路径。
	second := flaggedBody(t, loginWearBridgeBody(0, loginWearAnchorBagSlot, 0, currentSocketListEquipment, 16, 100120544), 1)
	if handled, err := fixture.service.handleAlignedGameCommand(fixture.session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMoveItemspace), second); err != nil || !handled {
		t.Fatalf("second packet: handled=%t err=%v (must not be dropped)", handled, err)
	}
	if !fixture.session.hasOp19QuietPending() {
		t.Fatal("the second packet must become the new pending")
	}
	fixture.service.resolveOp19QuietPending(fixture.session, false)
	if capture.calls != 2 || capture.request.PlayerRequestFlag {
		t.Fatalf("second resolve: calls=%d flag=%t, want 2/false", capture.calls, capture.request.PlayerRequestFlag)
	}
}

// TestOp19QuietWindowLeavesTailZeroUntouched 是**要求③**：
// tail==0 的包即时透传（不挂起）、且**不带**玩家标志 ⇒ 逐字节等于今天。
func TestOp19QuietWindowLeavesTailZeroUntouched(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(wornSlotClaimRetryTierEnv, "")
	t.Setenv(op19PlayerFlagEnv, "")
	t.Setenv(op19PlayerFlagQuietEnv, "")
	capture := installOp19Capture(t)
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	plain := loginWearAnchorLiveSlot17Body() // tail == 0

	if handled, err := fixture.service.handleAlignedGameCommand(fixture.session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMoveItemspace), plain); err != nil || !handled {
		t.Fatalf("tail==0 packet: handled=%t err=%v", handled, err)
	}
	if fixture.session.hasOp19QuietPending() {
		t.Fatal("a tail==0 packet must never be pended")
	}
	if capture.calls != 1 || capture.request.PlayerRequestFlag {
		t.Fatalf("tail==0: calls=%d flag=%t, want 1/false (unchanged)", capture.calls, capture.request.PlayerRequestFlag)
	}
	if capture.request.WornSlotClaimOccurrence > 1 {
		t.Fatalf("tail==0 occurrence = %d, want the ordinary in-window count (1)", capture.request.WornSlotClaimOccurrence)
	}
}

// TestOp19QuietWindowTeardownIsLeakFree 是**边界④**：
// 挂起态只是会话上的切片 + bool，没有 goroutine/定时器 ⇒ 会话丢弃即释放；
// 重复结算、丢弃后结算一律安全（不 panic）。
func TestOp19QuietWindowTeardownIsLeakFree(t *testing.T) {
	installOp19Capture(t)
	service := &Service{}
	session := &gameSession{connID: "quiet-teardown"}
	session.pendOp19QuietBody([]byte{9, 9, 9})
	if !session.hasOp19QuietPending() {
		t.Fatal("pending expected")
	}
	service.resolveOp19QuietPending(session, true)
	service.resolveOp19QuietPending(session, true) // 第二次 = 安全 no-op
	session.dropOp19QuietPending()                 // = 会话结束
	service.resolveOp19QuietPending(session, true)
	if session.hasOp19QuietPending() {
		t.Fatal("pending must stay empty")
	}
}

// TestOp19QuietWindowResolveReasonIsReadable 钉住"一眼看出走了哪条"：
// 结算日志分别带 resolved_by=quiet-window-timeout / second-op19-in-window，且都带 quiet_ms。
func TestOp19QuietWindowResolveReasonIsReadable(t *testing.T) {
	installOp19Capture(t)
	logger, logPath := newPeerEndpointTestLogger(t)
	setDefaultTier(t)
	t.Setenv(wornSlotClaimRetryTierEnv, "")
	t.Setenv(op19PlayerFlagEnv, "")
	t.Setenv(op19PlayerFlagQuietEnv, "")
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	fixture.service.packetLog = logger

	first := flaggedBody(t, loginWearAnchorLiveSlot17Body(), 1)
	if handled, err := fixture.service.handleAlignedGameCommand(fixture.session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMoveItemspace), first); err != nil || !handled {
		t.Fatalf("first: handled=%t err=%v", handled, err)
	}
	fixture.service.resolveOp19QuietPending(fixture.session, false)
	second := flaggedBody(t, loginWearAnchorLiveSlot17Body(), 1)
	if handled, err := fixture.service.handleAlignedGameCommand(fixture.session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMoveItemspace), second); err != nil || !handled {
		t.Fatalf("second: handled=%t err=%v", handled, err)
	}
	fixture.service.resolveOp19QuietPending(fixture.session, true)

	logged := readPeerEndpointTestLog(t, logger, logPath)
	for _, want := range []string{
		"equip-move/player-request-flag",
		"decision=pend reason=quiet-window",
		"resolved_by=second-op19-in-window",
		"resolved_by=quiet-window-timeout",
		"quiet_ms=400",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log is missing %q", want)
		}
	}
}
