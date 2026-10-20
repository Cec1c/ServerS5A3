package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type localTokenHTTPFixture42 struct {
	cfg    instanceConfig
	result localAccountToken
	kind   string
	calls  int
	t      *testing.T
}

func (f *localTokenHTTPFixture42) Do(r *http.Request) (*http.Response, error) {
	f.calls++
	if r.URL.Host != "127.0.0.1:28001" || r.URL.Path != "/local/account/login" || r.Header.Get("X-Admin-Token") != f.cfg.Server.AdminToken || r.Method != http.MethodPost {
		f.t.Fatal("credential request escaped local auth contract")
	}
	var p map[string]string
	if json.NewDecoder(r.Body).Decode(&p) != nil || p["username"] != "q7a" || p["password"] != "offline-fixture-password" || len(p) != 2 {
		f.t.Fatal("credential request missing actual password or has owner shortcut")
	}
	if f.kind == "network_error" {
		return nil, errors.New("private response must not be copied")
	}
	data, _ := json.Marshal(f.result)
	status := http.StatusOK
	switch f.kind {
	case "rejected":
		status = http.StatusUnauthorized
		data = []byte("private response must not be copied")
	case "redirect":
		status = http.StatusFound
	case "trailing":
		data = append(data, []byte("{}")...)
	case "oversize":
		data = append(data, []byte(strings.Repeat(" ", 4097))...)
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
}
func localTokenFixture42(t *testing.T, cfg instanceConfig) localAccountToken {
	t.Helper()
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal("fixture entropy failed")
	}
	return localAccountToken{AccountID: "dnf:10029", Token: cfg.Protocol.GameOuterToken[:8] + hex.EncodeToString(buf), ExpiresAt: time.Now().Add(15 * time.Minute)}
}
func TestNativeAuth42LaunchConsumesOnlyThisResponseToken(t *testing.T) {
	cfg := validTestInstance()
	cfg.Server.AdminListen = "127.0.0.1:28001"
	original := cfg
	for i := 0; i < 2; i++ {
		f := &localTokenHTTPFixture42{cfg: cfg, result: localTokenFixture42(t, cfg), t: t}
		starts := 0
		err := launchWithLocalLoginToken(context.Background(), cfg, " Q7A ", "offline-fixture-password", func(ctx context.Context, cfg instanceConfig, u, p string) (localAccountToken, error) {
			return requestLocalLoginToken(ctx, cfg, u, p, f)
		}, func(account, argument string) error {
			starts++
			parts := strings.Split(argument, "?")
			if account != f.result.AccountID || len(parts) < 5 || parts[4] != f.result.Token {
				t.Fatal("launch did not consume issued owner/token")
			}
			if !reflect.DeepEqual(cfg, original) {
				t.Fatal("launch mutated global profile/config")
			}
			return nil
		})
		if err != nil || starts != 1 || f.calls != 1 {
			t.Fatal("credential launch contract failed")
		}
	}
	if !reflect.DeepEqual(cfg, original) {
		t.Fatal("profile compatibility constant persisted launch token")
	}
}
func TestNativeAuth42FailuresNeverStartClient(t *testing.T) {
	for _, kind := range []string{"wrong_password", "no_username", "no_password", "network_error", "rejected", "redirect", "trailing", "oversize", "empty_token", "malformed_token", "compatibility_constant", "wrong_prefix", "expired", "unbounded_expiry", "bad_owner", "remote_admin", "empty_admin_token", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			cfg := validTestInstance()
			cfg.Server.AdminListen = "127.0.0.1:28001"
			f := &localTokenHTTPFixture42{cfg: cfg, result: localTokenFixture42(t, cfg), kind: kind, t: t}
			username, password := "q7a", "offline-fixture-password"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "wrong_password":
				f.kind = "rejected"
			case "no_username":
				username = ""
			case "no_password":
				password = ""
			case "empty_token":
				f.result.Token = ""
			case "malformed_token":
				f.result.Token = "invalid"
			case "compatibility_constant":
				f.result.Token = cfg.Protocol.GameOuterToken
			case "wrong_prefix":
				f.result.Token = "00000000" + f.result.Token[8:]
			case "expired":
				f.result.ExpiresAt = time.Now().Add(-time.Second)
			case "unbounded_expiry":
				f.result.ExpiresAt = time.Now().Add(time.Hour)
			case "bad_owner":
				f.result.AccountID = "bad owner"
			case "remote_admin":
				cfg.Server.AdminListen = "192.0.2.1:28001"
			case "empty_admin_token":
				cfg.Server.AdminToken = ""
			case "cancelled":
				cancel()
			}
			starts := 0
			err := launchWithLocalLoginToken(ctx, cfg, username, password, func(ctx context.Context, cfg instanceConfig, u, p string) (localAccountToken, error) {
				return requestLocalLoginToken(ctx, cfg, u, p, f)
			}, func(string, string) error { starts++; return nil })
			if err == nil || starts != 0 {
				t.Fatal("credential failure reached client start")
			}
			if strings.Contains(err.Error(), "private response") || strings.Contains(err.Error(), "offline-fixture-password") {
				t.Fatal("credential failure leaked private fields")
			}
		})
	}
}
