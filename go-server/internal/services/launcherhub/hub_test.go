package launcherhub

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testToken = "test-admin-token-that-is-long-and-isolated"

func fixture(t *testing.T) (*Hub, *rsa.PrivateKey) {
	t.Helper()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	pub, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	priv, _ := x509.MarshalPKCS8PrivateKey(key)
	root := t.TempDir()
	profile := Profile{ApiURL: "https://launcher.example.test", CertificateSHA256: strings.Repeat("a", 64), UpdatePublicKey: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})), ExeSHA256: strings.Repeat("b", 64), PvfSHA256: strings.Repeat("c", 64), GameHost: "192.0.2.1", BootstrapPort: 7001}
	b, _ := json.Marshal(profile)
	os.WriteFile(filepath.Join(root, "profile.json"), b, 0600)
	os.WriteFile(filepath.Join(root, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}), 0600)
	h, e := New(Config{AdminListen: "127.0.0.1:8444", AdminTokenSHA256: digest([]byte(testToken)), DataDir: filepath.Join(root, "state"), SigningKeyFile: filepath.Join(root, "key.pem"), ProfileFile: filepath.Join(root, "profile.json")})
	if e != nil {
		t.Fatal(e)
	}
	return h, key
}
func call(h http.Handler, method, path, body string, cookie *http.Cookie, csrf, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://admin.example.test"+path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.10:3000"
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("Origin", origin)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func auth(t *testing.T, h *Hub, remember bool) (*http.Cookie, string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"token": testToken, "remember": remember})
	w := call(h.AdminHandler(), "POST", "/admin/api/login", string(b), nil, "", "https://admin.example.test")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var data struct {
		CSRF string `json:"csrf"`
	}
	json.Unmarshal(w.Body.Bytes(), &data)
	return w.Result().Cookies()[0], data.CSRF
}
func TestAdminRememberAndRevocation(t *testing.T) {
	h, _ := fixture(t)
	cookie, csrf := auth(t, h, true)
	if cookie.MaxAge != 30*24*3600 || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe remembered cookie", cookie)
	}
	raw, _ := os.ReadFile(filepath.Join(h.cfg.DataDir, "state.json"))
	if strings.Contains(string(raw), testToken) || strings.Contains(string(raw), cookie.Value) {
		t.Fatal("persisted reusable credential")
	}
	restarted, e := New(h.cfg)
	if e != nil {
		t.Fatal(e)
	}
	if w := call(restarted.AdminHandler(), "GET", "/admin/api/state", "", cookie, "", ""); w.Code != 200 {
		t.Fatal("remember did not survive restart")
	}
	if w := call(restarted.AdminHandler(), "POST", "/admin/api/logout", "{}", cookie, csrf, "https://admin.example.test"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	restarted, e = New(h.cfg)
	if e != nil {
		t.Fatal(e)
	}
	if w := call(restarted.AdminHandler(), "GET", "/admin/api/state", "", cookie, "", ""); w.Code != 401 {
		t.Fatal("revoked cookie accepted")
	}
	sessionCookie, _ := auth(t, restarted, false)
	if sessionCookie.MaxAge != 0 {
		t.Fatal("session choice not respected")
	}
}
func TestAdminAuthenticationCSRFAndRateLimit(t *testing.T) {
	h, _ := fixture(t)
	if w := call(h.AdminHandler(), "GET", "/admin/api/state", "", nil, "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	cookie, csrf := auth(t, h, true)
	for _, test := range []struct{ origin, csrf string }{{"https://evil.test", csrf}, {"https://admin.example.test", ""}} {
		if w := call(h.AdminHandler(), "POST", "/admin/api/logout", "{}", cookie, test.csrf, test.origin); w.Code != 403 {
			t.Fatal("CSRF accepted")
		}
	}
	for i := 0; i < 8; i++ {
		if w := call(h.AdminHandler(), "POST", "/admin/api/login", `{"token":"bad"}`, nil, "", ""); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	if w := call(h.AdminHandler(), "POST", "/admin/api/login", `{"token":"bad"}`, nil, "", ""); w.Code != 429 {
		t.Fatal("not rate limited")
	}
	h.mu.Lock()
	s := h.state.Sessions[digest([]byte(cookie.Value))]
	s.Expires = time.Now().Add(-time.Second)
	h.state.Sessions[digest([]byte(cookie.Value))] = s
	h.mu.Unlock()
	if w := call(h.AdminHandler(), "GET", "/admin/api/state", "", cookie, "", ""); w.Code != 401 {
		t.Fatal("expired session accepted")
	}
}
func upstreamBytes(t *testing.T, key *rsa.PrivateKey, m UpstreamManifest, pss bool) []byte {
	t.Helper()
	raw, _ := json.Marshal(m)
	hash := sha256.Sum256(raw)
	var sig []byte
	var e error
	if pss {
		sig, e = rsa.SignPSS(rand.Reader, key, crypto.SHA256, hash[:], nil)
	} else {
		sig, e = rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	}
	if e != nil {
		t.Fatal(e)
	}
	env, _ := json.Marshal(Envelope{base64.StdEncoding.EncodeToString(raw), base64.StdEncoding.EncodeToString(sig)})
	return env
}
func upstreamFixture(h *Hub) (Source, UpstreamManifest) {
	s := Source{ID: "author", Name: "Author", Endpoint: "https://source.example.test", PublicKey: h.profile.UpdatePublicKey, Enabled: true}
	m := UpstreamManifest{Format: 1, Product: "YKCN90", Channel: "stable", Version: "test-release", Sequence: 10, Files: []File{
		{Path: "client/DNF.exe", Length: 10, Sha256: h.profile.ExeSHA256, Downloadable: true},
		{Path: "client/Script.pvf", Length: 11, Sha256: h.profile.PvfSHA256, Downloadable: true},
		{Path: "client/dstr.dat", Length: 12, Sha256: strings.Repeat("d", 64), Downloadable: true},
		{Path: "client/ImagePacks2/test.NPK", Length: 13, Sha256: strings.Repeat("e", 64), Downloadable: true},
		{Path: "YKCN90Launcher.exe", Length: 14, Sha256: strings.Repeat("f", 64), Downloadable: true},
		{Path: "YKCN90Updater.exe", Length: 15, Sha256: strings.Repeat("f", 64), Downloadable: true},
		{Path: "client/connection.json", Length: 16, Sha256: strings.Repeat("f", 64), Downloadable: true},
	}}
	return s, m
}
func TestSignatureAdaptersAndManagedContent(t *testing.T) {
	h, key := fixture(t)
	s, m := upstreamFixture(h)
	raw := upstreamBytes(t, key, m, false)
	verified, hash, e := verifyUpstream(raw, s, "stable")
	if e != nil {
		t.Fatal(e)
	}
	c, e := selectContent(verified, s, h.profile, hash)
	if e != nil || len(c.Files) != 4 {
		t.Fatal("managed content selection", e)
	}
	for _, f := range c.Files {
		if strings.Contains(f.Path, "Launcher") || strings.Contains(f.Path, "Updater") || strings.Contains(f.Path, "connection") {
			t.Fatal("foreign launcher/config admitted")
		}
	}
	if _, _, e = verifyUpstream(upstreamBytes(t, key, m, true), s, "stable"); e == nil {
		t.Fatal("PSS accepted as author PKCS1v15")
	}
	m.Files[0].Sha256 = strings.Repeat("0", 64)
	if _, e = selectContent(m, s, h.profile, hash); e == nil {
		t.Fatal("unapproved compatibility accepted")
	}
}
func TestManifestPathGuards(t *testing.T) {
	h, key := fixture(t)
	s, m := upstreamFixture(h)
	for _, p := range []string{"../DNF.exe", "client/../DNF.exe", "client/C:foo", "client/CON.txt", "client/x. ", "client\\DNF.exe", "/absolute"} {
		m.Files[0].Path = p
		if _, _, e := verifyUpstream(upstreamBytes(t, key, m, false), s, "stable"); e == nil {
			t.Fatalf("accepted %s", p)
		}
	}
	m.Files[0].Path = "CLIENT/script.PVF"
	if _, _, e := verifyUpstream(upstreamBytes(t, key, m, false), s, "stable"); e == nil {
		t.Fatal("case-colliding path accepted")
	}
}
func TestInspectPublishReplayAndPublicBoundary(t *testing.T) {
	h, key := fixture(t)
	s, m := upstreamFixture(h)
	manifest := upstreamBytes(t, key, m, false)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("credentials leaked to author")
		}
		w.Write(manifest)
	}))
	defer upstream.Close()
	s.Endpoint = upstream.URL
	s.CertificateSHA256 = digest(upstream.Certificate().Raw)
	h.state.Sources = []Source{s}
	c, _, _, e := h.inspect(t.Context(), s.ID, "stable")
	if e != nil {
		t.Fatal(e)
	}
	b, e := h.publish(c.ID)
	if e != nil || b.Sequence != 2 || len(b.Content.Files) != 4 {
		t.Fatal(e)
	}
	w := call(h.PublicHandler(), "GET", "/launcher/v2/bootstrap.json", "", nil, "", "")
	var env Envelope
	json.Unmarshal(w.Body.Bytes(), &env)
	p, _ := base64.StdEncoding.DecodeString(env.Payload)
	sig, _ := base64.StdEncoding.DecodeString(env.Signature)
	hash := sha256.Sum256(p)
	if e = rsa.VerifyPSS(&key.PublicKey, crypto.SHA256, hash[:], sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}); e != nil {
		t.Fatal("own publication PSS rejected", e)
	}
	if strings.Contains(string(p), testToken) || strings.Contains(string(p), "PRIVATE KEY") {
		t.Fatal("private material in public config")
	}
	for _, path := range []string{"/admin/api/state", "/gm/", "/api/accounts"} {
		if w := call(h.PublicHandler(), "GET", path, "", nil, "", ""); w.Code != 404 {
			t.Fatal("admin exposed publicly")
		}
	}
	m.Sequence = 9
	manifest = upstreamBytes(t, key, m, false)
	c, _, _, e = h.inspect(t.Context(), s.ID, "stable")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = h.publish(c.ID); e == nil {
		t.Fatal("rollback accepted")
	}
	m.Sequence = 10
	m.Version = "same-sequence-different-payload"
	manifest = upstreamBytes(t, key, m, false)
	c, _, _, _ = h.inspect(t.Context(), s.ID, "stable")
	if _, e = h.publish(c.ID); e == nil {
		t.Fatal("equivocation accepted")
	}
}
func TestGMRequiresHubSessionAndNeverForwardsAdminToken(t *testing.T) {
	h, _ := fixture(t)
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "dfo_gm_session", Value: "private-gm-cookie"})
			w.Write([]byte(`{"ok":true}`))
			return
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "dfo_gm_session=private-gm-cookie" {
			t.Error("untrusted credentials forwarded")
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer gm.Close()
	h.cfg.GMUpstream = gm.URL
	h.cfg.GMPassword = "fixture-password"
	if w := call(h.AdminHandler(), "GET", "/api/accounts", "", nil, "", ""); w.Code != 401 {
		t.Fatal("GM unprotected")
	}
	cookie, _ := auth(t, h, true)
	if w := call(h.AdminHandler(), "GET", "/api/accounts", "", cookie, "", ""); w.Code != 200 || len(w.Result().Cookies()) != 0 {
		t.Fatal("GM proxy failed", w.Code)
	}
	if w := call(h.AdminHandler(), "POST", "/api/accounts/1/currency", "{}", cookie, "", ""); w.Code != 403 {
		t.Fatal("originless GM write accepted")
	}
}
