package dnfbridge

import (
	"testing"
	"time"
)

// 本文件钉住登录引导窗口（DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP 兜底的范围限定）：
//   - 开关关闭时，窗口既不开启也不消费（现场行为与日志保持原样）；
//   - 默认档（WINDOW=repeat）：场景就绪 ack 开启窗口，窗口在**整个时长内可重复命中**
//     —— 这是 2026-09-28 现场复发的直接修法（conn game-001200 的两条逐字节同形包，
//     第一条 noop、第二条被一次性窗口挡在门外变成真卸下）；
//   - 回退档（WINDOW=once）：逐字节回到今天 —— 本会话第一条 op19 消费掉窗口；
//   - 时长：默认 15000 ms，写 WINDOW_MS=5000 回到今天的 5s；超时后窗口自动失效。
func TestEquipLoginWearWindowLifecycle(t *testing.T) {
	session := &gameSession{}
	service := &Service{}

	t.Run("switch off keeps the window inert", func(t *testing.T) {
		t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "")
		service.openEquipLoginWearWindow(session, "test")
		if !session.equipLoginWearWindowOpenedAt.IsZero() {
			t.Fatal("window must not open while the switch is off")
		}
		if service.equipLoginWearWindowOpenForOp19(session) {
			t.Fatal("window must not be open while the switch is off")
		}
	})

	t.Run("default repeat keeps every op19 inside the window", func(t *testing.T) {
		t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
		if !loginWearReassertWindowRepeatEnabled() {
			t.Fatal("the default tier must be repeat, not once")
		}
		live := &gameSession{}
		service.openEquipLoginWearWindow(live, "test")
		if live.equipLoginWearWindowOpenedAt.IsZero() {
			t.Fatal("window must open on the scene-ready ack")
		}
		for hit := 1; hit <= 3; hit++ {
			if !service.equipLoginWearWindowOpenForOp19(live) {
				t.Fatalf("op19 #%d must stay inside the window (repeat tier): the live "+
					"game-001200 regression is the second identical packet falling out of it", hit)
			}
		}
		if !live.equipLoginWearWindowConsumed {
			t.Fatal("the window must still be marked as hit (live evidence)")
		}
	})

	t.Run("rollback once consumes the window on the first op19", func(t *testing.T) {
		t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
		t.Setenv(loginWearReassertWindowModeEnv, loginWearReassertWindowOnce)
		if loginWearReassertWindowRepeatEnabled() {
			t.Fatal("once must disable the repeat tier")
		}
		live := &gameSession{}
		service.openEquipLoginWearWindow(live, "test")
		if !service.equipLoginWearWindowOpenForOp19(live) {
			t.Fatal("the first op19 after the ack must be inside the window")
		}
		if service.equipLoginWearWindowOpenForOp19(live) {
			t.Fatal("WINDOW=once is one-shot: a second op19 in the same window must fall back")
		}
	})

	t.Run("the window expires after its deadline", func(t *testing.T) {
		t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
		stale := &gameSession{equipLoginWearWindowOpenedAt: time.Now().Add(-2 * service.loginWearReassertWindowDuration())}
		if service.equipLoginWearWindowOpenForOp19(stale) {
			t.Fatal("an expired window must not absorb an op19")
		}
		if !stale.equipLoginWearWindowConsumed {
			t.Fatal("an expired window must be consumed so it cannot reopen implicitly")
		}
	})

	t.Run("the default deadline is 15s and the rollback is 5s", func(t *testing.T) {
		t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
		t.Setenv(loginWearReassertWindowMillisEnv, "")
		if got := service.loginWearReassertWindowDuration(); got != 15*time.Second {
			t.Fatalf("default window = %s, want 15s (the fixed tier)", got)
		}
		// 现场 game-001200 的第二条同形包出现在 ack 之后约 3.1s：默认档必须收，
		// 回退档（5s）也收 —— 但 6s 时默认档仍开、回退档已关。
		six := &gameSession{equipLoginWearWindowOpenedAt: time.Now().Add(-6 * time.Second)}
		if !service.equipLoginWearWindowOpenForOp19(six) {
			t.Fatal("6s after the ack must still be inside the 15s default window")
		}
		t.Setenv(loginWearReassertWindowMillisEnv, "5000")
		if got := service.loginWearReassertWindowDuration(); got != 5*time.Second {
			t.Fatalf("WINDOW_MS=5000 window = %s, want 5s (today)", got)
		}
		sixRollback := &gameSession{equipLoginWearWindowOpenedAt: time.Now().Add(-6 * time.Second)}
		if service.equipLoginWearWindowOpenForOp19(sixRollback) {
			t.Fatal("6s after the ack must be outside the 5s rollback window")
		}
	})
}

// 两个新开关的归一化：默认必须是修复档，回退必须显式；时长接受任何正整数、非法回落默认、
// 超上限卡到上限。
func TestEquipLoginWearWindowSwitchNormalization(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want bool
	}{
		{raw: "", want: true},
		{raw: "repeat", want: true},
		{raw: "on", want: true},
		{raw: "typo", want: true},
		{raw: "  REPEAT  ", want: true},
		{raw: "once", want: false},
		{raw: "ONCE", want: false},
		{raw: "one_shot", want: false},
		{raw: "legacy", want: false},
		{raw: "off", want: false},
		{raw: "0", want: false},
		{raw: "none", want: false},
	} {
		t.Setenv(loginWearReassertWindowModeEnv, test.raw)
		if got := loginWearReassertWindowRepeatEnabled(); got != test.want {
			t.Fatalf("loginWearReassertWindowRepeatEnabled(%q) = %t, want %t", test.raw, got, test.want)
		}
	}

	for _, test := range []struct {
		raw  string
		want int
	}{
		{raw: "", want: loginWearReassertWindowDefaultMillis},
		{raw: "15000", want: 15000},
		{raw: "5000", want: 5000},
		{raw: "1", want: 1},
		{raw: "500", want: 500},
		{raw: "60000", want: loginWearReassertWindowMaxMillis},
		{raw: "60001", want: loginWearReassertWindowMaxMillis},
		{raw: "999999999", want: loginWearReassertWindowMaxMillis},
		{raw: "0", want: loginWearReassertWindowDefaultMillis},
		{raw: "-3000", want: loginWearReassertWindowDefaultMillis},
		{raw: "abc", want: loginWearReassertWindowDefaultMillis},
		{raw: "  5000  ", want: 5000},
	} {
		t.Setenv(loginWearReassertWindowMillisEnv, test.raw)
		if got := loginWearReassertWindowDurationMillis(); got != test.want {
			t.Fatalf("loginWearReassertWindowDurationMillis(%q) = %d, want %d", test.raw, got, test.want)
		}
	}

	// 启动回显（操作者调参之后唯一能确认生效的地方）。
	t.Setenv(loginWearReassertWindowModeEnv, "")
	t.Setenv(loginWearReassertWindowMillisEnv, "")
	zero := &Service{}
	if got := zero.loginWearReassertWindowMode(); got != loginWearReassertWindowRepeat {
		t.Fatalf("startup echo equip_login_wear_window = %q, want %q", got, loginWearReassertWindowRepeat)
	}
	if got := zero.loginWearReassertWindowDuration(); got != 15*time.Second {
		t.Fatalf("startup echo equip_login_wear_window_ms = %s, want 15s", got)
	}
	t.Setenv(loginWearReassertWindowModeEnv, "once")
	t.Setenv(loginWearReassertWindowMillisEnv, "5000")
	if got := zero.loginWearReassertWindowMode(); got != loginWearReassertWindowOnce {
		t.Fatalf("rollback startup echo = %q, want %q", got, loginWearReassertWindowOnce)
	}
	if got := zero.loginWearReassertWindowDuration(); got != 5*time.Second {
		t.Fatalf("rollback startup echo ms = %s, want 5s", got)
	}
	// noop 刷新开关同样默认 on、回退必须显式。
	for _, test := range []struct {
		raw  string
		want bool
	}{
		{raw: "", want: true},
		{raw: "on", want: true},
		{raw: "1", want: true},
		{raw: "typo", want: true},
		{raw: "off", want: false},
		{raw: " OFF ", want: false},
		{raw: "0", want: false},
		{raw: "disabled", want: false},
	} {
		t.Setenv(equipLoginWearReassertNoopRefreshEnv, test.raw)
		if got := equipLoginWearReassertNoopRefreshEnabled(); got != test.want {
			t.Fatalf("equipLoginWearReassertNoopRefreshEnabled(%q) = %t, want %t", test.raw, got, test.want)
		}
		if got := (&Service{}).equipLoginWearReassertNoopRefreshRequested(); got != test.want {
			t.Fatalf("service switch(%q) = %t, want %t", test.raw, got, test.want)
		}
	}
	t.Setenv(equipLoginWearReassertNoopRefreshEnv, "")
	if got := equipLoginWearReassertNoopRefreshMode(); got != "on" {
		t.Fatalf("startup echo equip_login_wear_noop_refresh = %q, want on", got)
	}
	t.Setenv(equipLoginWearReassertNoopRefreshEnv, "off")
	if got := equipLoginWearReassertNoopRefreshMode(); got != "off" {
		t.Fatalf("startup echo equip_login_wear_noop_refresh = %q, want off", got)
	}
}
