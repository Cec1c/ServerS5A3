package gmtool

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// P3 路由面：新增 3 条路由的登录门禁 + 「不遮住既有路由」探针。
//
// 与 P2 的 TestMailboxDeleteRouteRequiresSessionAndIsNotTheFallback 同一强度：
// 未登录必须 401 + loginRequired；带会话必须命中自己的 handler（不是 401、
// 也不是 /api/ 兜底）。既有路由的注册顺序在 routes() 里没有被动过，这里再钉一次。
// ---------------------------------------------------------------------------

func httpPostJSON(t *testing.T, handler http.Handler, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return httpDoJSON(t, handler, "", http.MethodPost, target, body)
}

func httpPostJSONWithSession(t *testing.T, handler http.Handler, token, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return httpDoJSON(t, handler, token, http.MethodPost, target, body)
}

func httpGetWithSession(t *testing.T, handler http.Handler, token, target string) *httptest.ResponseRecorder {
	t.Helper()
	return httpDoJSON(t, handler, token, http.MethodGet, target, nil)
}

func httpDoJSON(t *testing.T, handler http.Handler, token, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	request := httptest.NewRequest(method, target, reader)
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// p3RouteProbe 是一条要探测的路由（走真实 mux，路径变量由 ServeMux 填）。
type p3RouteProbe struct {
	name   string
	method string
	target string
	body   any
}

func p3RouteProbes() []p3RouteProbe {
	return []p3RouteProbe{
		{"SP 绝对值设置", http.MethodPost, "/api/characters/1/sp/set", map[string]any{"bonus_sp": 1, "bonus_tp": 1}},
		{"晶块读取", http.MethodGet, "/api/characters/1/account-cubes", nil},
		{"晶块写入", http.MethodPost, "/api/characters/1/account-cubes", map[string]any{"selection": 0, "count": 1}},
	}
}

func TestP3AddedRoutesRequireSession(t *testing.T) {
	harness := newP3CubeHarness(t, p3CubeFixture{})
	handler := harness.server.routes() // 注册冲突会在这里 panic（Go 1.22 ServeMux 语义）
	for _, probe := range p3RouteProbes() {
		recorder := httpDoJSON(t, handler, "", probe.method, probe.target, probe.body)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401（body=%s）", probe.name, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "\"loginRequired\":true") {
			t.Fatalf("%s: body = %s, want loginRequired:true", probe.name, recorder.Body.String())
		}
	}
	token, err := harness.server.startSession()
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	for _, probe := range p3RouteProbes() {
		recorder := httpDoJSON(t, handler, token, probe.method, probe.target, probe.body)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: 带会话 status = %d（body=%s）", probe.name, recorder.Code, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), "该功能未移植到 90CN 面板") {
			t.Fatalf("%s: 落到了 /api/ 兜底: %s", probe.name, recorder.Body.String())
		}
	}
}

// 既有路由必须仍然命中自己（新路由不能把它们遮住）。
//
// 只探**不依赖 s.db** 的既有路由（与 P1 的 existingReadonlyProbes 同一取舍）：
// `POST /api/accounts/{id}/currency` 的前半段要查启动器账号表，单测没有数据库；
// 它的业务分支由 api_account_cargo_gold_test.go 直接打 applyAccountCurrency 覆盖。
func TestP3RoutesDoNotShadowExistingRoutes(t *testing.T) {
	harness := newP3CubeHarness(t, p3CubeFixture{})
	handler := harness.server.routes()
	token, err := harness.server.startSession()
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	probes := []p3RouteProbe{
		{"既有增量 /sp", http.MethodPost, "/api/characters/1/sp", map[string]any{"sp": 0, "tp": 0}},
		{"既有 /sptp", http.MethodGet, "/api/characters/1/sptp", nil},
		{"既有角色背包", http.MethodGet, "/api/characters/1/items", nil},
		{"既有物品检索", http.MethodGet, "/api/items/search?q=1", nil},
	}
	for _, probe := range probes {
		recorder := httpDoJSON(t, handler, token, probe.method, probe.target, probe.body)
		if recorder.Code == http.StatusNotFound {
			t.Fatalf("%s: %s -> 404，既有路由被新路由遮住了", probe.name, probe.target)
		}
		if strings.Contains(recorder.Body.String(), "该功能未移植到 90CN 面板") {
			t.Fatalf("%s: %s 落到了兜底: %s", probe.name, probe.target, recorder.Body.String())
		}
	}
}
