package launcherhub

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTokenRotationRevokesRememberedSession(t *testing.T) {
	h, _ := fixture(t)
	cookie, _ := auth(t, h, true)
	before := h.state.Published.Sequence
	c := h.cfg
	c.AdminTokenSHA256 = digest([]byte("a-new-independent-token-for-this-test"))
	restarted, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	if w := call(restarted.AdminHandler(), "GET", "/admin/api/state", "", cookie, "", ""); w.Code != 401 {
		t.Fatal("old cookie survived token rotation")
	}
	if w := call(restarted.AdminHandler(), "POST", "/admin/api/login", `{"token":"`+testToken+`"}`, nil, "", ""); w.Code != 401 {
		t.Fatal("old token survived rotation")
	}
	if restarted.state.Published.Sequence != before {
		t.Fatal("rotation changed publication sequence")
	}
	restarted, err = New(c)
	if err != nil {
		t.Fatal(err)
	}
	if w := call(restarted.AdminHandler(), "GET", "/admin/api/state", "", cookie, "", ""); w.Code != 401 {
		t.Fatal("revoked session reappeared after restart")
	}
}

func TestGMRelativeAssetsBridgeAndLogout(t *testing.T) {
	h, _ := fixture(t)
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "dfo_gm_session", Value: "isolated-cookie"})
			w.Write([]byte(`{"ok":true}`))
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<html><head><title>GM</title></head><body><script src="js/core.js"></script></body></html>`))
		case "/js/core.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.Write([]byte("window.gmFixtureReady=true;"))
		default:
			t.Errorf("unexpected GM upstream path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer gm.Close()
	h.cfg.GMUpstream = gm.URL
	h.cfg.GMPassword = "isolated-gm-password"
	cookie, _ := auth(t, h, true)
	w := call(h.AdminHandler(), "GET", "/gm/", "", cookie, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `<script src="/admin/gm-session.js"></script></head>`) {
		t.Fatal("GM session adapter was not injected", w.Code)
	}
	w = call(h.AdminHandler(), "GET", "/gm/js/core.js", "", cookie, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "gmFixtureReady") {
		t.Fatal("relative GM asset not mapped", w.Code)
	}
	w = call(h.AdminHandler(), "POST", "/api/auth/logout", "{}", cookie, "", "https://admin.example.test")
	if w.Code != 200 || len(w.Result().Cookies()) != 1 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("GM logout did not remove hub cookie", w.Code)
	}
	if w = call(h.AdminHandler(), "GET", "/api/accounts", "", cookie, "", ""); w.Code != 401 {
		t.Fatal("GM logout retained API access")
	}
	restarted, err := New(h.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if w = call(restarted.AdminHandler(), "GET", "/admin/api/state", "", cookie, "", ""); w.Code != 401 {
		t.Fatal("GM logout revocation was not persisted")
	}
}

func bundleFixture(t *testing.T, extra string) []byte {
	t.Helper()
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	names := []string{"DNF90.exe", "DNF90.Core.exe", "flutter_windows.dll", "data/app.so", "data/icudtl.dat"}
	if extra != "" {
		names = append(names, extra)
	}
	for _, name := range names {
		entry, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		entry.Write([]byte("isolated non-executable bundle fixture " + name))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestBundlePublicationRangeAndRejection(t *testing.T) {
	h, _ := fixture(t)
	cookie, csrf := auth(t, h, true)
	upload := func(version string, raw []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://admin.example.test/admin/api/bundle/"+version, bytes.NewReader(raw))
		r.AddCookie(cookie)
		r.Header.Set("Origin", "https://admin.example.test")
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Content-Type", "application/zip")
		w := httptest.NewRecorder()
		h.AdminHandler().ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"../escape", "dnf90.EXE", "data/CON.txt"} {
		if w := upload("0.2.1", bundleFixture(t, path)); w.Code != 400 || h.state.Published.Launcher != nil {
			t.Fatalf("unsafe bundle published: %s (%d)", path, w.Code)
		}
	}
	raw := bundleFixture(t, "")
	w := upload("0.2.1", raw)
	var bundle Bundle
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &bundle) != nil || bundle.SHA256 != digest(raw) || bundle.Size != int64(len(raw)) {
		t.Fatal("bundle publication failed", w.Code, w.Body.String())
	}
	sequence := h.state.Published.Sequence
	for _, version := range []string{"0.2.1", "0.2.0"} {
		if w = upload(version, raw); w.Code != 409 || h.state.Published.Sequence != sequence {
			t.Fatal("non-increasing bundle version accepted", version, w.Code)
		}
	}
	r := httptest.NewRequest("GET", bundle.URL, nil)
	r.Header.Set("Range", "bytes=5-24")
	w = httptest.NewRecorder()
	h.PublicHandler().ServeHTTP(w, r)
	if w.Code != 206 || !bytes.Equal(w.Body.Bytes(), raw[5:25]) || w.Header().Get("ETag") != `"`+bundle.SHA256+`"` {
		t.Fatal("public bundle Range/ETag failed", w.Code)
	}
	restarted, err := New(h.cfg)
	if err != nil || restarted.state.Published.Launcher.SHA256 != bundle.SHA256 {
		t.Fatal("bundle publication lost on restart", err)
	}
}
