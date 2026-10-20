package launcherhub

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientInteropHarness(t *testing.T) {
	receipt := os.Getenv("DNF90_HUB_INTEROP_RECEIPT")
	if receipt == "" {
		t.Skip("interop not requested")
	}
	h, key := fixture(t)
	game := t.TempDir()
	state := t.TempDir()
	exe := []byte("MZ-isolated-client-fixture")
	pvf := []byte("isolated-pvf-fixture")
	var registrations atomic.Int32
	h.profile.ExeSHA256 = digest(exe)
	h.profile.PvfSHA256 = digest(pvf)
	h.profile.DefaultClientDirectory = game
	source, manifest := upstreamFixture(h)
	manifest.Files = manifest.Files[:2]
	blobs := map[string][]byte{}
	for i, plain := range [][]byte{exe, pvf} {
		var b bytes.Buffer
		gz := gzip.NewWriter(&b)
		gz.Write(plain)
		gz.Close()
		blob := b.Bytes()
		f := &manifest.Files[i]
		f.Length = int64(len(plain))
		f.Sha256 = digest(plain)
		f.Compression = "gzip"
		f.BlobLength = int64(len(blob))
		f.BlobSha256 = digest(blob)
		blobs[f.BlobSha256] = blob
	}
	signed := upstreamBytes(t, key, manifest, false)
	author := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Method != "GET" {
			t.Error("account request reached content source")
		}
		if r.URL.Path == "/channels/stable/manifest.signed.json" {
			w.Write(signed)
			return
		}
		if blob, ok := blobs[strings.TrimPrefix(r.URL.Path, "/blobs/")]; ok {
			http.ServeContent(w, r, "blob", time.Time{}, bytes.NewReader(blob))
			return
		}
		http.NotFound(w, r)
	}))
	defer author.Close()
	source.Endpoint = author.URL
	source.CertificateSHA256 = digest(author.Certificate().Raw)
	h.state.Sources = []Source{source}
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/account/register" && r.Method == "POST" {
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["account"] != "fixture-user" || body["password"] != "fixture-password" || !digestPattern.MatchString(body["device"]) {
				t.Error("account contract mismatch")
				http.Error(w, "bad", 400)
				return
			}
			registrations.Add(1)
			reply(w, 200, map[string]bool{"ok": true})
			return
		}
		h.PublicHandler().ServeHTTP(w, r)
	}))
	defer api.Close()
	h.profile.ApiURL = api.URL
	h.profile.CertificateSHA256 = digest(api.Certificate().Raw)
	h.state.Published.Profile = h.profile
	candidate, _, _, e := h.inspect(t.Context(), source.ID, "stable")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = h.publish(candidate.ID); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(h.profile)
	profile := filepath.Join(state, "profile.json")
	os.WriteFile(profile, raw, 0600)
	config, _ := json.Marshal(map[string]string{"profile": profile, "state": state, "game": game})
	if e = os.WriteFile(receipt, config, 0600); e != nil {
		t.Fatal(e)
	}
	defer os.Remove(receipt)
	deadline := time.After(3 * time.Minute)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("interop timed out")
		case <-ticker.C:
			if _, e := os.Stat(receipt + ".done"); e == nil {
				if registrations.Load() != 1 {
					t.Fatal("registration request not received")
				}
				return
			}
		}
	}
}
