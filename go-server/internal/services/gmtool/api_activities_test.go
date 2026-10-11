package gmtool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/onlineevent"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestActivityAPIRequiresSessionAndExplicitStatesAndRevision(t *testing.T) {
	controls, err := onlineevent.NewControls(context.Background(), memory.NewMemoryGroup().Settings, []onlineevent.ControlDefinition{{ID: 2347, Name: "online", DefaultEnabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{deps: Deps{Activities: controls}, cfg: Config{Logger: func(string, ...any) {}}, www: http.NotFoundHandler(), sessions: map[string]time.Time{}}
	handler := s.routes()
	token, err := s.startSession()
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, body string, authenticated bool) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, "/api/activities", strings.NewReader(body))
		if authenticated {
			req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
		}
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, req)
		var result map[string]any
		if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return r, result
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		r, _ := call(method, `{"revision":0,"activities":[{"id":2347,"enabled":false}]}`, false)
		if r.Code != 401 {
			t.Fatalf("unauthenticated %s returned %d", method, r.Code)
		}
	}
	for _, body := range []string{`{"activities":[{"id":2347,"enabled":false}]}`, `{"revision":0,"activities":[{"id":2347}]}`, `{"revision":0,"activities":[{"id":181,"enabled":true}]}`, `{"revision":0,"activities":[{"id":2347,"enabled":false}],"unknown":1}`, `{"revision":0,"activities":[{"id":2347,"enabled":false}]} {}`} {
		_, result := call(http.MethodPost, body, true)
		if result["success"] != false {
			t.Fatalf("invalid body accepted: %s", body)
		}
	}
	_, result := call(http.MethodPost, `{"revision":0,"activities":[{"id":2347,"enabled":false}]}`, true)
	if result["success"] != true || !controls.Snapshot().RestartRequired || !controls.Enabled(2347) {
		t.Fatalf("valid save=%v", result)
	}
	_, result = call(http.MethodGet, "", true)
	if result["success"] != true {
		t.Fatal(result)
	}
	_, result = call(http.MethodPost, `{"revision":0,"activities":[{"id":2347,"enabled":true}]}`, true)
	if result["success"] != false {
		t.Fatal("stale save accepted")
	}
}
