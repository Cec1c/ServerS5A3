package dnfbridge

import (
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// 尾随刷新延迟与连点窗口**拆开**之后：窗口可以调大到盖住操作者真实的 0.45–3 s 间隔，
// 而最终外观仍然"收手就出现"。
//
// 现场依据（2026-09-28）：窗口 200 ms 时 `game-avatar-appearance-coalesced` 在
// packet_log 与 dnf90.out 里都是 **0 次** —— 也就是那条"N 连穿压成 2 个 mode0"的修复
// 在操作者手上**从来没生效过**（他的穿脱间隔 0.45–3 s，全部落在窗口之外）；
// 而每一次 mode0 都会让客户端整题重建本地角色（`local-actor-create`）。
func TestAvatarAppearanceTrailingDelayIsIndependentOfTheCoalesceWindow(t *testing.T) {
	service, session, conn := newAvatarAppearanceCoalesceTestService(t)
	// 窗口 3 s（盖住连点），尾随 80 ms（收手立刻看到最终外观）。
	service.options.avatarAppearanceCoalesceWindowMillis = 3000
	service.options.avatarAppearanceTrailingMillis = 80
	if got := service.avatarAppearanceCoalesceWindow(); got != 3*time.Second {
		t.Fatalf("coalesce window=%s want 3s", got)
	}
	if got := service.avatarAppearanceTrailingDelay(); got != 80*time.Millisecond {
		t.Fatalf("trailing delay=%s want 80ms (must NOT follow the window)", got)
	}
	if !currentTownSelfActorAppearanceReady(session) {
		t.Fatal("fixture session is not appearance-ready")
	}

	const clicks = 4
	for click := 0; click < clicks; click++ {
		if err := service.sendAlignedPostAction(session, avatarAppearanceTestResult(), alignedcmd.PostActionRefreshSelectedActorAppearance); err != nil {
			t.Fatalf("appearance refresh #%d: %v", click+1, err)
		}
	}
	if got := countGameServerUpperPackets(t, conn.write.Bytes()); got != 1 {
		t.Fatalf("a %d-click burst sent %d mode0 packets immediately, want only the leading one", clicks, got)
	}

	// 尾随必须在 80 ms 量级到达，**不能**等满 3 s 的窗口。
	start := time.Now()
	waitForAvatarAppearancePackets(t, conn, 2)
	elapsed := time.Since(start)
	if got := countGameServerUpperPackets(t, conn.write.Bytes()); got != 2 {
		t.Fatalf("a %d-click burst sent %d mode0 packets in total, want leading+trailing = 2", clicks, got)
	}
	if elapsed >= time.Second {
		t.Fatalf("trailing refresh took %s, want ≈80ms — the window must not delay the final render", elapsed)
	}
	service.stopAvatarAppearanceCoalesce(session, "test_teardown")
}

// 未配置 DNFBRIDGE_EQUIP_APPEARANCE_TRAILING_MS 时必须**逐字节等于改动前**：
// 尾随延迟 = 连点窗口（options 零值 = 内置默认 200 ms）。
func TestAvatarAppearanceTrailingDelayDefaultsToTheWindow(t *testing.T) {
	service, _, _ := newAvatarAppearanceCoalesceTestService(t)
	service.options.avatarAppearanceCoalesceWindowMillis = 450
	if got, want := service.avatarAppearanceTrailingDelay(), 450*time.Millisecond; got != want {
		t.Fatalf("default trailing delay=%s want=%s (must equal the window)", got, want)
	}
	// options 零值 ⇒ 内置默认窗口（200 ms），与线上默认一致。
	zero := &Service{}
	if got, want := zero.avatarAppearanceTrailingDelay(), currentAvatarAppearanceCoalesceWindow; got != want {
		t.Fatalf("zero-value trailing delay=%s want=%s", got, want)
	}
	// 显式配置时必须覆盖窗口。
	service.options.avatarAppearanceTrailingMillis = 120
	if got, want := service.avatarAppearanceTrailingDelay(), 120*time.Millisecond; got != want {
		t.Fatalf("configured trailing delay=%s want=%s", got, want)
	}
	// 启动行回显口径：诊断用的 `equip_appearance_trailing_ms` 必须跟着走
	// （它是操作者调 DNFBRIDGE_EQUIP_APPEARANCE_TRAILING_MS 之后唯一能确认生效的地方）。
	if got := service.equipAppearanceTrailingMillis(); got != 120 {
		t.Fatalf("startup echo equip_appearance_trailing_ms=%d want=120", got)
	}
	if got := service.equipAppearanceCoalesceWindowMillis(); got != 450 {
		t.Fatalf("startup echo equip_appearance_coalesce_ms=%d want=450", got)
	}
	// 零值 Service（所有启动路径的零值口径）⇒ 两个回显都必须是内置默认 200 ms，
	// 也就是"未配置 = 改动前"。
	zeroSvc := &Service{}
	if got := zeroSvc.equipAppearanceTrailingMillis(); got != currentAvatarAppearanceCoalesceWindow.Milliseconds() {
		t.Fatalf("zero-value trailing echo=%d want=%d", got, currentAvatarAppearanceCoalesceWindow.Milliseconds())
	}
	if got := zeroSvc.equipAppearanceCoalesceWindowMillis(); got != currentAvatarAppearanceCoalesceWindow.Milliseconds() {
		t.Fatalf("zero-value window echo=%d want=%d", got, currentAvatarAppearanceCoalesceWindow.Milliseconds())
	}
}

// 开关归一化：两个量各有各的上限 —— 窗口最多 5 s（"隔多久还算同一次连点"），
// 尾随延迟最多 1 s（"收手之后最多多久一定看到最终外观"）。
func TestNormalizeAvatarAppearanceTrailingMillis(t *testing.T) {
	windowTests := []struct {
		raw  string
		want int
	}{
		{raw: "", want: 0},
		{raw: "   ", want: 0},
		{raw: "abc", want: 0},
		{raw: "0", want: 0},
		{raw: "-5", want: 0},
		{raw: "250", want: 250},
		{raw: " 3000 ", want: 3000},
		{raw: "5000", want: 5000},
		{raw: "5001", want: 0},
		{raw: "999999999", want: 0},
	}
	for _, test := range windowTests {
		if got := normalizeAvatarAppearanceCoalesceWindowMillis(test.raw); got != test.want {
			t.Fatalf("window normalize(%q)=%d want=%d", test.raw, got, test.want)
		}
	}
	trailingTests := []struct {
		raw  string
		want int
	}{
		{raw: "", want: 0},
		{raw: "abc", want: 0},
		{raw: "0", want: 0},
		{raw: "-1", want: 0},
		{raw: "80", want: 80},
		{raw: " 1000 ", want: 1000},
		{raw: "1001", want: 0},
		{raw: "3000", want: 0},
	}
	for _, test := range trailingTests {
		if got := normalizeAvatarAppearanceTrailingMillis(test.raw); got != test.want {
			t.Fatalf("trailing normalize(%q)=%d want=%d", test.raw, got, test.want)
		}
	}
}
