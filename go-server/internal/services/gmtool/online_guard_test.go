package gmtool

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// J0 在线守卫的单元面：开关解析、探针三态、安全降级。
// 端到端面（真实写路由 + 真实仓储）在 api_mailbox_delete_test.go 的 (a)/(b)/(c) 三条。

func TestRequireOfflineFromEnvOnlyLiteralOn(t *testing.T) {
	for _, value := range []string{"on", "ON", "On", " on ", "\ton\n"} {
		if !RequireOfflineFromEnv(value) {
			t.Fatalf("RequireOfflineFromEnv(%q) = false, want true", value)
		}
	}
	// 未设置/空/其它任何值（含 true/1/yes）都必须是默认档 false —— 逐字节今天的行为。
	for _, value := range []string{"", " ", "true", "TRUE", "True", "1", "yes", "y", "off", "0", "no", "enable", "onn", "on,off"} {
		if RequireOfflineFromEnv(value) {
			t.Fatalf("RequireOfflineFromEnv(%q) = true, want false（只有显式 on 才启用）", value)
		}
	}
}

func TestRequireOfflineFromEnvironmentReadsProcessEnv(t *testing.T) {
	t.Setenv(requireOfflineEnv, "on")
	if !RequireOfflineFromEnvironment() {
		t.Fatalf("%s=on 时必须启用", requireOfflineEnv)
	}
	t.Setenv(requireOfflineEnv, "true")
	if RequireOfflineFromEnvironment() {
		t.Fatalf("%s=true 必须取默认档 false", requireOfflineEnv)
	}
	t.Setenv(requireOfflineEnv, "")
	if RequireOfflineFromEnvironment() {
		t.Fatalf("%s 为空必须取默认档 false", requireOfflineEnv)
	}
}

func TestProbeCharacterIDUint16Range(t *testing.T) {
	cases := []struct {
		panelID int
		want    uint16
		ok      bool
	}{
		{0, 0, false},
		{-1, 0, false},
		{1, 1, true},
		{65535, 65535, true},
		{65536, 0, false},
	}
	for _, testCase := range cases {
		got, ok := probeCharacterID(testCase.panelID)
		if ok != testCase.ok || got != testCase.want {
			t.Fatalf("probeCharacterID(%d) = (%d,%t), want (%d,%t)",
				testCase.panelID, got, ok, testCase.want, testCase.ok)
		}
	}
}

// 默认档（开关未设 = Config 零值）：守卫直接放行，不探针、不写响应。
func TestOnlineGuardIsOffByDefault(t *testing.T) {
	probed := false
	server := &Server{deps: Deps{Online: func(uint16) bool { probed = true; return true }}}
	if server.cfg.RequireOffline {
		t.Fatal("Config 零值必须是 false（默认档不检查在线）")
	}
	recorder := httptest.NewRecorder()
	if !server.requireOffline(recorder, "unit-test", 1) {
		t.Fatal("默认档必须放行")
	}
	if probed {
		t.Fatal("默认档不该调用在线探针")
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("默认档不该写任何响应体: %q", recorder.Body.String())
	}
}

func TestOnlineGuardAllowsOfflineCharacterWithoutWriting(t *testing.T) {
	events := 0
	server := &Server{
		cfg:  Config{RequireOffline: true, Logger: func(string, ...any) { events++ }},
		deps: Deps{Online: func(uint16) bool { return false }},
	}
	recorder := httptest.NewRecorder()
	if !server.requireOffline(recorder, "unit-test", 7) {
		t.Fatal("离线角色必须放行")
	}
	if recorder.Body.Len() != 0 || events != 0 {
		t.Fatalf("放行不该写响应/日志: body=%q events=%d", recorder.Body.String(), events)
	}
}

// 在线角色被拒也必须恒 HTTP 200 + success:false —— 本仓没有第二个业务失败码。
func TestOnlineGuardRejectsOnlineCharacterWithHttp200(t *testing.T) {
	kinds := make([]string, 0, 1)
	server := &Server{
		cfg:  Config{RequireOffline: true, Logger: func(kind string, _ ...any) { kinds = append(kinds, kind) }},
		deps: Deps{Online: func(characterID uint16) bool { return characterID == 7 }},
	}
	recorder := httptest.NewRecorder()
	if server.requireOffline(recorder, "unit-test", 7) {
		t.Fatal("在线角色必须被拒绝")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200（绝不引入 409 之类的新失败码）", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"success":false`) {
		t.Fatalf("body = %s, want success:false", recorder.Body.String())
	}
	if len(kinds) != 1 || kinds[0] != "gm-online-guard-rejected" {
		t.Fatalf("事件 = %v, want [gm-online-guard-rejected]", kinds)
	}
}

// Deps.Online == nil：安全降级为"未知/不在线"，放行 + 一条具名日志（不 panic、不拒绝一切）。
func TestOnlineGuardDegradesWhenProbeIsMissing(t *testing.T) {
	fields := map[string]any{}
	server := &Server{
		cfg: Config{RequireOffline: true, Logger: func(kind string, kv ...any) {
			fields["kind"] = kind
			for i := 0; i+1 < len(kv); i += 2 {
				fields[strings.TrimSpace(kv[i].(string))] = kv[i+1]
			}
		}},
	}
	recorder := httptest.NewRecorder()
	if !server.requireOffline(recorder, "unit-test", 7) {
		t.Fatal("没有探针时必须放行（视为未知/不在线）")
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("降级放行不该写响应: %q", recorder.Body.String())
	}
	if fields["kind"] != "gm-online-guard-probe-missing" || fields["reason"] != "probe_not_injected" {
		t.Fatalf("降级事件字段 = %+v", fields)
	}
}

// 面板主键超出对局会话表的 uint16 范围：同样降级放行（会话表里不可能有这样的角色）。
func TestOnlineGuardDegradesForOutOfRangePanelID(t *testing.T) {
	probed := false
	server := &Server{
		cfg:  Config{RequireOffline: true},
		deps: Deps{Online: func(uint16) bool { probed = true; return true }},
	}
	recorder := httptest.NewRecorder()
	if !server.requireOffline(recorder, "unit-test", 70000) {
		t.Fatal("超出 uint16 的面板主键必须降级放行")
	}
	if probed {
		t.Fatal("超出范围不该调用探针")
	}
}

// 守卫只碰目标角色：同账号其它角色在线不影响本次判定（P4 的账号级口径要单独实现）。
func TestOnlineGuardOnlyAsksAboutTheTargetCharacter(t *testing.T) {
	asked := make([]uint16, 0, 1)
	server := &Server{
		cfg:  Config{RequireOffline: true},
		deps: Deps{Online: func(characterID uint16) bool { asked = append(asked, characterID); return false }},
	}
	recorder := httptest.NewRecorder()
	if !server.requireOffline(recorder, "unit-test", 42) {
		t.Fatal("探针说不在线时必须放行")
	}
	if len(asked) != 1 || asked[0] != 42 {
		t.Fatalf("探针被问的角色 = %v, want [42]", asked)
	}
}

// newGmtoolWriteHarness 的默认档必须与生产默认一致：cfg.RequireOffline 零值，
// 且没注入探针时也不会影响既有 J5 行为（见 api_mailbox_delete_test.go 的 (a)）。
func TestWriteHarnessDefaultsToTheProductionDefaultProfile(t *testing.T) {
	harness := newGmtoolWriteHarness(t, map[string]dnfrepo.MailRecord{
		"1": {MailID: "1", Title: "干净邮件"},
	})
	if harness.server.cfg.RequireOffline {
		t.Fatal("测试台默认档必须是 RequireOffline=false")
	}
	if harness.server.deps.Online != nil {
		t.Fatal("测试台默认不注入探针（要注入的用例显式注入）")
	}
}
