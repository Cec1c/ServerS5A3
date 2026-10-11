package dnf90

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appkit "longheng.io/server/internal/platform/app"
	"longheng.io/server/internal/platform/httpx"
)

// The disabled repository must expose an unavailable login, never a fake token.
// Uses the real composition and admin policy; no HTTP listener or SQL connection.
func TestNativeAuth42CompositionLocalLoginUnavailableFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logic.toml")
	if err := os.WriteFile(path, []byte("[repository]\nenabled = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LONGHENG_DNF_CONFIG", path)
	mux := http.NewServeMux()
	env := &appkit.Env{AdminMux: mux, AdminAuth: func(scope string, h http.HandlerFunc) http.HandlerFunc {
		return httpx.WrapAdminPolicy(func() httpx.AdminPolicy { return httpx.AdminPolicy{Token: "offline-admin-fixture"} }, nil, scope, h)
	}}
	if _, err := Components(env); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/local/account/login", strings.NewReader(`{"username":"q7a","password":"offline-fixture-password"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Admin-Token", "offline-admin-fixture")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("local credential route status=%d, want 503; no token endpoint on baseline", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `"token"`) {
		t.Fatal("missing repository issued token")
	}
	req.Header.Del("X-Admin-Token")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatal("local token route bypassed admin guard")
	}
}
