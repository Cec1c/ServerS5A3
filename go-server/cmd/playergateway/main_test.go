package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlayerProxyBoundary(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Player-IP") != "192.0.2.5" || r.Header.Get("X-Player-Proxy-Key") != "server-secret" {
			t.Error("spoofed identity")
		}
		w.WriteHeader(204)
	}))
	defer up.Close()
	h := handler(config{Upstream: up.URL, ProxySecret: "server-secret", ReleaseDir: t.TempDir()})
	for _, path := range []string{"/api/game/admin", "/api/player/admin", "/manage/v1/accounts", "/debug/reload", "/launcher/../gateway.json", "/api/patch/file", "/api/account/login?x=1"} {
		r := httptest.NewRequest("POST", path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("exposed %s", path)
		}
	}
	r := httptest.NewRequest("POST", "/api/account/login", strings.NewReader("{}"))
	r.RemoteAddr = "192.0.2.5:3456"
	r.Header.Set("X-Player-IP", "forged")
	r.Header.Set("X-Player-Proxy-Key", "forged")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
}

func TestPortableDistributionReadOnlyBoundary(t *testing.T) {
	dir := t.TempDir()
	name := "DNF90-Portable-0.2.0.exe"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("MZ portable fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	h := handler(config{Upstream: "http://127.0.0.1:1", ReleaseDir: dir})
	r := httptest.NewRequest("GET", "/launcher/"+name, nil)
	r.Header.Set("Range", "bytes=0-1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != "MZ" {
		t.Fatal("portable Range download failed", w.Code)
	}
	for _, path := range []string{"/launcher/" + name + "?download=1", "/launcher/" + name + ".key", "/launcher/DNF90-Portable-../gateway.json", "/launcher/other.exe", "/admin/", "/gm/"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatal("unexpected public file/management route", path, w.Code)
		}
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/launcher/"+name, strings.NewReader("replace")))
	if w.Code != 404 {
		t.Fatal("public distribution allows writes")
	}
}
