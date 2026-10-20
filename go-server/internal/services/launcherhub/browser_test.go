package launcherhub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Optional, isolated headless-browser harness. All credentials, sources and files
// are test fixtures; this does not bind a production port or use a real database.
func TestBrowserHarness(t *testing.T) {
	path := os.Getenv("DNF90_HUB_BROWSER_RECEIPT")
	if path == "" {
		t.Skip("browser harness not requested")
	}
	h, key := fixture(t)
	source, manifest := upstreamFixture(h)
	raw := upstreamBytes(t, key, manifest, false)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(raw) }))
	defer upstream.Close()
	source.Endpoint = upstream.URL
	source.CertificateSHA256 = digest(upstream.Certificate().Raw)
	source.Name = "隔离测试内容源"
	h.state.Sources = []Source{source}
	h.state.Published.News = []News{{Title: "欢迎回到阿拉德", Body: "来自测试后台的公告，用于验证签名发布流程。", Category: "综合", Date: "10 / 13"}}
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "dfo_gm_session", Value: "fixture-gm-session"})
			w.Write([]byte(`{"ok":true}`))
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<html><head><title>Isolated GM adapter fixture</title></head><body><button id="gm-logout">退出</button><script src="js/core.js"></script></body></html>`))
		case "/js/core.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.Write([]byte(`document.body.dataset.ready='yes';document.getElementById('gm-logout').onclick=()=>fetch('/api/auth/logout',{method:'POST'});`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer gm.Close()
	h.cfg.GMUpstream = gm.URL
	h.cfg.GMPassword = "isolated-fixture-password"
	admin := httptest.NewServer(h.AdminHandler())
	defer admin.Close()
	receipt, _ := json.Marshal(map[string]string{"url": admin.URL})
	if e := os.WriteFile(path, receipt, 0600); e != nil {
		t.Fatal(e)
	}
	deadline := time.After(5 * time.Minute)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("browser did not complete")
		case <-ticker.C:
			if _, e := os.Stat(path + ".done"); e == nil {
				return
			}
		}
	}
}
