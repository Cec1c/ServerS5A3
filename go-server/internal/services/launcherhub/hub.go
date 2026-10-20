package launcherhub

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"embed"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed www/*
var web embed.FS

type session struct {
	Expires time.Time `json:"expires"`
	CSRF    string    `json:"csrf"`
}
type persisted struct {
	AuthFingerprint string             `json:"authFingerprint"`
	Sources         []Source           `json:"sources"`
	Sessions        map[string]session `json:"sessions"`
	Published       Bootstrap          `json:"published"`
	SourceSequences map[string]int64   `json:"sourceSequences"`
	SourceDigests   map[string]string  `json:"sourceDigests"`
}
type Hub struct {
	cfg       Config
	key       *rsa.PrivateKey
	profile   Profile
	mu        sync.Mutex
	state     persisted
	failures  map[string][]time.Time
	gmCookies map[string]*http.Cookie
	// HTTP client construction is replaceable only by in-package isolated tests.
	sourceClient func(Source) *http.Client
}

func New(c Config) (*Hub, error) {
	if c.AdminListen == "" || !digestPattern.MatchString(c.AdminTokenSHA256) || c.DataDir == "" {
		return nil, errors.New("hub requires admin listener, SHA256 token and private data directory")
	}
	pRaw, e := os.ReadFile(c.ProfileFile)
	if e != nil {
		return nil, e
	}
	var p Profile
	if e = json.Unmarshal(pRaw, &p); e != nil {
		return nil, e
	}
	if _, e = httpsURL(p.ApiURL); e != nil {
		return nil, e
	}
	if !digestPattern.MatchString(p.CertificateSHA256) || !digestPattern.MatchString(p.ExeSHA256) || !digestPattern.MatchString(p.PvfSHA256) {
		return nil, errors.New("invalid approved profile fingerprints")
	}
	raw, e := os.ReadFile(c.SigningKeyFile)
	if e != nil {
		return nil, e
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("invalid signing PEM")
	}
	parsed, e := x509.ParsePKCS8PrivateKey(block.Bytes)
	if e != nil {
		return nil, errors.New("use a PKCS8 signing key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok || key.N.BitLen() < 2048 {
		return nil, errors.New("invalid RSA signing key")
	}
	pub, e := publicKey(p.UpdatePublicKey)
	if e != nil || pub.N.Cmp(key.N) != 0 || pub.E != key.E {
		return nil, errors.New("signing key does not match the player trust root")
	}
	if c.GMUpstream != "" {
		u, e := url.Parse(c.GMUpstream)
		if e != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || c.GMPassword == "" {
			return nil, errors.New("invalid private GM upstream")
		}
	}
	if e = os.MkdirAll(c.DataDir, 0700); e != nil {
		return nil, e
	}
	h := &Hub{cfg: c, key: key, profile: p, failures: map[string][]time.Time{}, gmCookies: map[string]*http.Cookie{}, sourceClient: clientForSource}
	h.state = persisted{Sources: []Source{}, Sessions: map[string]session{}, SourceSequences: map[string]int64{}, SourceDigests: map[string]string{}, Published: Bootstrap{Product: Product, Format: 1, Sequence: 1, Profile: p, News: []News{}}}
	b, e := os.ReadFile(filepath.Join(c.DataDir, "state.json"))
	if e == nil {
		if e = json.Unmarshal(b, &h.state); e != nil {
			return nil, e
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if h.state.AuthFingerprint != strings.ToLower(c.AdminTokenSHA256) {
		h.state.Sessions = map[string]session{}
		h.state.AuthFingerprint = strings.ToLower(c.AdminTokenSHA256)
		if e = h.save(); e != nil {
			return nil, e
		}
	}
	if h.state.Sessions == nil {
		h.state.Sessions = map[string]session{}
	}
	if h.state.SourceSequences == nil {
		h.state.SourceSequences = map[string]int64{}
	}
	if h.state.SourceDigests == nil {
		h.state.SourceDigests = map[string]string{}
	}
	// A changed deployment profile must be published deliberately, not silently
	// paired with content approved for a different EXE/PVF.
	if h.state.Published.Profile.ExeSHA256 != p.ExeSHA256 || h.state.Published.Profile.PvfSHA256 != p.PvfSHA256 {
		return nil, errors.New("approved profile changed: migrate publication state explicitly")
	}
	return h, nil
}

func atomicFile(path string, b []byte) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".write-")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	return nil
}
func (h *Hub) save() error {
	raw, e := json.Marshal(h.state)
	if e != nil {
		return e
	}
	return atomicFile(filepath.Join(h.cfg.DataDir, "state.json"), raw)
}
func clientForSource(s Source) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if s.CertificateSHA256 != "" {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("missing peer certificate")
			}
			c := cs.PeerCertificates[0]
			now := time.Now()
			if now.Before(c.NotBefore) || now.After(c.NotAfter) || !strings.EqualFold(digest(c.Raw), s.CertificateSHA256) {
				return errors.New("source certificate rejected")
			}
			return nil
		}} // Trust is the configured exact leaf DER pin; no unauthenticated TLS bypass.
	}
	return &http.Client{Transport: transport, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func boundedResponse(response *http.Response, limit int64) ([]byte, error) {
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("upstream request failed")
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if e == nil && int64(len(raw)) > limit {
		e = errors.New("upstream response too large")
	}
	return raw, e
}
func (h *Hub) inspect(ctx context.Context, id, channel string) (Candidate, UpstreamManifest, string, error) {
	var source Source
	h.mu.Lock()
	for _, s := range h.state.Sources {
		if s.ID == id && s.Enabled {
			source = s
			break
		}
	}
	h.mu.Unlock()
	if source.ID == "" || channel != "stable" && channel != "preview" {
		return Candidate{}, UpstreamManifest{}, "", errors.New("unknown source/channel")
	}
	req, e := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(source.Endpoint, "/")+"/channels/"+channel+"/manifest.signed.json", nil)
	if e != nil {
		return Candidate{}, UpstreamManifest{}, "", e
	}
	req.Header.Set("Accept-Encoding", "identity")
	client := h.sourceClient(source)
	defer client.CloseIdleConnections()
	response, e := client.Do(req)
	if e != nil {
		return Candidate{}, UpstreamManifest{}, "", errors.New("source TLS/connection failed")
	}
	raw, e := boundedResponse(response, MaxManifest)
	if e != nil {
		return Candidate{}, UpstreamManifest{}, "", e
	}
	m, hash, e := verifyUpstream(raw, source, channel)
	if e != nil {
		return Candidate{}, m, "", e
	}
	candidate := Candidate{ID: digest(append([]byte(source.ID+":"+channel+":"), raw...)), Source: source, Channel: channel, Envelope: raw}
	b, e := json.Marshal(candidate)
	if e == nil {
		e = atomicFile(filepath.Join(h.cfg.DataDir, "candidates", candidate.ID+".json"), b)
	}
	return candidate, m, hash, e
}
func (h *Hub) publish(id string) (Bootstrap, error) {
	if !digestPattern.MatchString(id) {
		return Bootstrap{}, errors.New("invalid candidate")
	}
	raw, e := os.ReadFile(filepath.Join(h.cfg.DataDir, "candidates", id+".json"))
	if e != nil {
		return Bootstrap{}, errors.New("candidate unavailable")
	}
	var c Candidate
	if e = json.Unmarshal(raw, &c); e != nil {
		return Bootstrap{}, e
	}
	m, hash, e := verifyUpstream(c.Envelope, c.Source, c.Channel)
	if e != nil {
		return Bootstrap{}, e
	}
	content, e := selectContent(m, c.Source, h.profile, hash)
	if e != nil {
		return Bootstrap{}, e
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	enabled := false
	for _, s := range h.state.Sources {
		if s == c.Source && s.Enabled {
			enabled = true
		}
	}
	if !enabled {
		return Bootstrap{}, errors.New("source changed; inspect again")
	}
	key := c.Source.ID + ":" + c.Channel
	sequence := h.state.SourceSequences[key]
	if m.Sequence < sequence || m.Sequence == sequence && hash != h.state.SourceDigests[key] {
		return Bootstrap{}, errors.New("source sequence rollback/equivocation")
	}
	previous := h.state.Published
	oldHash := h.state.SourceDigests[key]
	h.state.Published.Content = content
	h.state.Published.Sequence++
	h.state.SourceSequences[key] = m.Sequence
	h.state.SourceDigests[key] = hash
	if e = h.save(); e != nil {
		h.state.Published = previous
		h.state.SourceSequences[key] = sequence
		h.state.SourceDigests[key] = oldHash
		return Bootstrap{}, e
	}
	return h.state.Published, nil
}
func (h *Hub) PublicHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery == "" && (r.Method == "GET" || r.Method == "HEAD") && h.serveBundle(w, r) {
			return
		}
		if r.URL.Path != "/launcher/v2/bootstrap.json" || r.URL.RawQuery != "" || (r.Method != "GET" && r.Method != "HEAD") {
			http.NotFound(w, r)
			return
		}
		h.mu.Lock()
		raw, e := sign(h.state.Published, h.key)
		h.mu.Unlock()
		if e != nil {
			http.Error(w, "publication unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Write(raw)
	})
}
func randomHex() string {
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}

func (h *Hub) gm(w http.ResponseWriter, r *http.Request, identity string) {
	if h.cfg.GMUpstream == "" {
		http.Error(w, "GM upstream is not configured", 503)
		return
	}
	h.mu.Lock()
	cookie := h.gmCookies[identity]
	h.mu.Unlock()
	if cookie == nil || time.Now().After(cookie.Expires) {
		raw, _ := json.Marshal(map[string]string{"password": h.cfg.GMPassword})
		req, e := http.NewRequestWithContext(r.Context(), "POST", strings.TrimRight(h.cfg.GMUpstream, "/")+"/api/auth/login", strings.NewReader(string(raw)))
		if e != nil {
			http.Error(w, "GM unavailable", 502)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, e := client.Do(req)
		if e != nil {
			http.Error(w, "GM unavailable", 502)
			return
		}
		defer resp.Body.Close()
		for _, c := range resp.Cookies() {
			if c.Name == "dfo_gm_session" {
				cookie = c
				break
			}
		}
		if cookie == nil {
			http.Error(w, "GM authentication unavailable", 502)
			return
		}
		cookie.Expires = time.Now().Add(10 * time.Hour)
		h.mu.Lock()
		h.gmCookies[identity] = cookie
		h.mu.Unlock()
	}
	upstream, _ := url.Parse(h.cfg.GMUpstream)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	original := proxy.Director
	proxy.Director = func(req *http.Request) {
		original(req)
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
		req.Header.Del("Forwarded")
		req.Header.Del("X-Forwarded-For")
		req.Header.Del("X-Player-Proxy-Key")
		req.Header.Del("Origin")
		req.Header.Del("Referer")
		req.Header.Set("Accept-Encoding", "identity")
		req.Host = upstream.Host
		req.AddCookie(cookie)
		if strings.HasPrefix(req.URL.Path, "/gm/") {
			req.URL.Path = strings.TrimPrefix(req.URL.Path, "/gm")
			req.URL.RawPath = ""
		}
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Del("Set-Cookie")
		if strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
			raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			resp.Body.Close()
			if err != nil || len(raw) >= 2<<20 {
				return errors.New("GM page unavailable")
			}
			raw = bytes.Replace(raw, []byte("</head>"), []byte(`<script src="/admin/gm-session.js"></script></head>`), 1)
			resp.Body = io.NopCloser(bytes.NewReader(raw))
			resp.ContentLength = int64(len(raw))
			resp.Header.Del("Content-Length")
			resp.Header.Del("ETag")
		}
		if resp.StatusCode == 401 {
			h.mu.Lock()
			delete(h.gmCookies, identity)
			h.mu.Unlock()
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { http.Error(w, "GM unavailable", 502) }
	proxy.ServeHTTP(w, r)
}
