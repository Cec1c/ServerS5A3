package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func native46Instance() instanceConfig {
	cfg := validTestInstance()
	cfg.Protocol.Profile = "native-channel-profile-official-24.9"
	cfg.Client.HookCreate = false
	cfg.Server.PartyUDPRelayPortStart = 32000
	cfg.Server.PartyUDPRelayPortCount = 64
	return cfg
}

func native46Env(cfg instanceConfig) map[string]string {
	out := map[string]string{}
	for _, item := range runtimeEnvironment(cfg) {
		k, v, ok := strings.Cut(item, "=")
		if ok {
			out[k] = v
		}
	}
	return out
}

func native46WriteInstance(t *testing.T, paths projectPaths, cfg instanceConfig) {
	t.Helper()
	if err := paths.ensureDirectories(); err != nil {
		t.Fatal("synthetic directories unavailable")
	}
	data, err := marshalInstance(cfg)
	if err != nil || os.WriteFile(paths.instance, data, 0600) != nil {
		t.Fatal("synthetic instance write failed")
	}
}

func TestNormalControlNative46OfficialInstanceAndRuntimeEnvironment(t *testing.T) {
	cfg := native46Instance()
	t.Run("instance", func(t *testing.T) {
		if validateInstance(cfg) != nil {
			t.Fatal("known official native instance was rejected")
		}
	})
	t.Run("environment", func(t *testing.T) {
		t.Setenv("DNFBRIDGE_CURRENT_CLIENT_VERSION", "public-wrong-inherited-version")
		t.Setenv("DNFBRIDGE_PARTY_UDP_RELAY_MODE", "public-wrong-inherited-mode")
		t.Setenv("DNFBRIDGE_GAME_LISTEN_HOST", "0.0.0.0")
		env := native46Env(cfg)
		want := map[string]string{"DNFBRIDGE_CURRENT_CLIENT_VERSION": "official-24.9", "DNFBRIDGE_PARTY_UDP_RELAY_MODE": "embedded", "DNFBRIDGE_PARTY_UDP_RELAY_PROBE_LISTEN": "on", "DNFBRIDGE_PARTY_UDP_RELAY_SEED_FORWARD": "on", "DNFBRIDGE_PARTY_UDP_RELAY_PROBE_PORTS": "2311,2312,32000", "DNFBRIDGE_GAME_UPPER_HEADER": "server16", "DNFBRIDGE_GAME_UPPER_BODY_CODEC": "plaintext", "DNFBRIDGE_GAME_UPPER_CLIENT_BODY_CODEC": "plaintext", "DNFBRIDGE_CHANNEL_SERVER_INDEX": "1", "DNFBRIDGE_CHANNEL_ADVERTISE_SERVER_INDEX": "0", "DNFBRIDGE_DPROTO_MODE": "legacy_patch", "DNF_SCRIPT_VERSION": "59", "DNFBRIDGE_GAME_LISTEN_HOST": cfg.Server.AdvertiseIP}
		for k, v := range want {
			if env[k] != v {
				t.Errorf("native environment contract mismatch at %s; values omitted", k)
			}
		}
	})
}

func TestNormalControlNative46LoadDefaultsRemainProfileSpecific(t *testing.T) {
	for _, profile := range []string{"native-channel-profile-official-24.9", "90cn-decode-bypass-v1"} {
		t.Run(profile, func(t *testing.T) {
			cfg := native46Instance()
			cfg.Protocol.Profile = profile
			cfg.Client.HookCreate = profile == "90cn-decode-bypass-v1"
			cfg.Server.PartyUDPRelayPortStart = 0
			cfg.Server.PartyUDPRelayPortCount = 0
			paths := newProjectPaths(t.TempDir())
			native46WriteInstance(t, paths, cfg)
			loaded, err := loadInstance(paths)
			if err != nil {
				t.Fatal("profile-specific synthetic load failed")
			}
			want := 30000
			if profile == "native-channel-profile-official-24.9" {
				want = 31000
			}
			if loaded.Server.PartyUDPRelayPortStart != want || loaded.Server.PartyUDPRelayPortCount != 64 {
				t.Fatal("relay defaults changed incorrectly")
			}
			env := native46Env(loaded)
			if profile == "native-channel-profile-official-24.9" {
				if env["DNFBRIDGE_PARTY_UDP_RELAY_PROBE_PORTS"] != "2311,2312,31000" {
					t.Fatal("native default environment differs from loaded configuration")
				}
			} else {
				for _, key := range []string{"DNFBRIDGE_CURRENT_CLIENT_VERSION", "DNFBRIDGE_PARTY_UDP_RELAY_MODE", "DNFBRIDGE_PARTY_UDP_RELAY_PROBE_LISTEN", "DNFBRIDGE_PARTY_UDP_RELAY_SEED_FORWARD", "DNFBRIDGE_PARTY_UDP_RELAY_PROBE_PORTS"} {
					if _, ok := env[key]; ok {
						t.Fatal("native keys leaked into legacy defaults")
					}
				}
			}
		})
	}
}

func TestNormalControlNative46ProfileAndLocalityGuardsRemain(t *testing.T) {
	cases := []struct {
		name, want string
		change     func(*instanceConfig)
	}{
		{"unknown_profile", "protocol.profile", func(c *instanceConfig) { c.Protocol.Profile = "public-unknown-profile" }},
		{"native_hook_on", "hookCreate", func(c *instanceConfig) { c.Client.HookCreate = true }},
		{"wrong_header", "gameUpperHeader", func(c *instanceConfig) { c.Protocol.GameUpperHeader = "client7" }},
		{"wrong_server_codec", "body codecs", func(c *instanceConfig) { c.Protocol.GameUpperBodyCodec = "aes" }},
		{"wrong_client_codec", "body codecs", func(c *instanceConfig) { c.Protocol.GameUpperClientBodyCodec = "native" }},
		{"wrong_source_index", "channel indexes", func(c *instanceConfig) { c.Protocol.ChannelServerIndex = 0 }},
		{"wrong_advertise_index", "channel indexes", func(c *instanceConfig) { c.Protocol.ChannelAdvertiseServerIndex = 1 }},
		{"fixed_game_port", "initialGamePort", func(c *instanceConfig) { c.Client.InitialGamePort = 29001 }},
		{"loopback_game", "private LAN", func(c *instanceConfig) { c.Server.AdvertiseIP = "127.0.0.1" }},
		{"wildcard_game", "private LAN", func(c *instanceConfig) { c.Server.AdvertiseIP = "0.0.0.0" }},
		{"public_game", "private LAN", func(c *instanceConfig) { c.Server.AdvertiseIP = "8.8.8.8" }},
		{"public_admin", "adminListen", func(c *instanceConfig) { c.Server.AdminListen = "192.168.50.2:28111" }},
		{"public_channel", "channelListen", func(c *instanceConfig) { c.Server.ChannelListen = "192.168.50.2:27001" }},
		{"remote_db", "database.host", func(c *instanceConfig) { c.Database.Host = "192.168.50.2" }},
		{"wrong_pvf_scope", "asset paths", func(c *instanceConfig) { c.Game.PVFPath = "data/other/Script.pvf" }},
		{"wrong_channel_scope", "asset paths", func(c *instanceConfig) { c.Game.ChannelInfoPath = "data/other/channel_info.etc" }},
		{"relay_range", "partyUdpRelayPortCount", func(c *instanceConfig) { c.Server.PartyUDPRelayPortStart = 65530 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := native46Instance()
			tc.change(&cfg)
			err := validateInstance(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal("specific existing guard was lost or wrong guard obscured native contract")
			}
		})
	}
	legacy := validTestInstance()
	legacy.Client.HookCreate = false
	if err := validateInstance(legacy); err == nil || !strings.Contains(err.Error(), "hookCreate") {
		t.Fatal("legacy hook contract changed")
	}
}

func TestNormalControlNative46GeneratedConfigAndManagedConfigBinding(t *testing.T) {
	cfg := native46Instance()
	paths := newProjectPaths(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(paths.channelAsset), 0755); err != nil {
		t.Fatal("fixture unavailable")
	}
	if os.WriteFile(paths.channelAsset, []byte("public synthetic channel asset"), 0600) != nil {
		t.Fatal("fixture unavailable")
	}
	if generateRuntimeConfigs(paths, cfg) != nil {
		t.Fatal("native configuration generation failed")
	}
	want, err := desiredServerRuntimeConfigSHA256(cfg)
	if err != nil {
		t.Fatal("native desired identity unavailable")
	}
	got, err := currentServerRuntimeConfigSHA256(paths, cfg)
	if err != nil || got != want {
		t.Fatal("native generated/current identity mismatch")
	}
	c := newController(paths, io.Discard, io.Discard)
	state := processState{RuntimeConfigSHA256: want}
	if _, err = c.validateRunningServerConfig(cfg, state); err != nil {
		t.Fatal("current native managed config rejected")
	}
	if _, err = c.validateRunningServerConfig(cfg, processState{}); err == nil {
		t.Fatal("historical state without config binding accepted")
	}
	legacy := cfg
	legacy.Protocol.Profile = "90cn-decode-bypass-v1"
	legacy.Client.HookCreate = true
	if _, err = c.validateRunningServerConfig(legacy, state); err == nil {
		t.Fatal("legacy config adopted native runtime state")
	}
	file := filepath.Join(paths.runtimeConfigs, "dnfbridge.toml")
	data, _ := os.ReadFile(file)
	if os.WriteFile(file, append(data, []byte("\n# public synthetic config drift\n")...), 0600) != nil {
		t.Fatal("fixture drift failed")
	}
	if _, err = c.validateRunningServerConfig(cfg, state); err == nil {
		t.Fatal("runtime config drift accepted")
	}
}

func TestNormalControlNative46NativeClientManifestChecksRemainExact(t *testing.T) {
	cfg := native46Instance()
	paths := newProjectPaths(t.TempDir())
	client := filepath.Join(paths.projectRoot, "public-client")
	if os.MkdirAll(client, 0755) != nil || os.MkdirAll(filepath.Dir(paths.clientManifest), 0755) != nil {
		t.Fatal("fixture unavailable")
	}
	manifest := clientManifest{SchemaVersion: 1, Profile: cfg.Protocol.Profile}
	for _, name := range []string{"DNF.exe", "ijl15.dll"} {
		data := []byte("public synthetic file, never executed: " + name)
		if os.WriteFile(filepath.Join(client, name), data, 0600) != nil {
			t.Fatal("fixture write failed")
		}
		digest := sha256.Sum256(data)
		manifest.Files = append(manifest.Files, clientManifestEntry{Names: []string{name}, Required: true, SHA256: hex.EncodeToString(digest[:])})
	}
	if writeJSON(paths.clientManifest, manifest, 0600) != nil {
		t.Fatal("manifest fixture unavailable")
	}
	if _, _, err := validateClient(paths, cfg, client, io.Discard); err != nil {
		t.Fatal("exact native manifest rejected")
	}
	manifest.Profile = "90cn-decode-bypass-v1"
	_ = writeJSON(paths.clientManifest, manifest, 0600)
	if _, _, err := validateClient(paths, cfg, client, io.Discard); err == nil {
		t.Fatal("mismatched profile accepted")
	}
	manifest.Profile = cfg.Protocol.Profile
	_ = writeJSON(paths.clientManifest, manifest, 0600)
	if os.WriteFile(filepath.Join(client, "DNF.exe"), []byte("public changed bytes"), 0600) != nil {
		t.Fatal("fixture drift failed")
	}
	if _, _, err := validateClient(paths, cfg, client, io.Discard); err == nil {
		t.Fatal("changed executable SHA accepted")
	}
	if os.WriteFile(filepath.Join(client, "DNF.exe"), []byte("public synthetic file, never executed: DNF.exe"), 0600) != nil {
		t.Fatal("fixture restore failed")
	}
	if os.Remove(filepath.Join(client, "ijl15.dll")) != nil {
		t.Fatal("fixture remove failed")
	}
	if _, _, err := validateClient(paths, cfg, client, io.Discard); err == nil {
		t.Fatal("missing required native dependency accepted")
	}
	if os.Remove(paths.clientManifest) != nil {
		t.Fatal("fixture remove failed")
	}
	if _, _, err := validateClient(paths, cfg, client, io.Discard); err == nil {
		t.Fatal("missing manifest accepted")
	}
}

func TestNormalControlNative46NoHistoricalReceiptOrUnmanagedLaunch(t *testing.T) {
	paths := newProjectPaths(t.TempDir())
	cfg := native46Instance()
	native46WriteInstance(t, paths, cfg)
	c := newController(paths, io.Discard, io.Discard)
	// Missing managed state stops before readiness HTTP, auth or client creation.
	if err := c.launchClient(context.Background(), "", false, "synthetic_admin", "synthetic-password46"); err == nil || !strings.Contains(err.Error(), "server is not running") {
		t.Fatal("native launch did not require normal managed state")
	}
	if writeJSON(filepath.Join(paths.projectRoot, "historical-once03-receipt.json"), map[string]any{"pid": 46, "birth": "public-historical-marker"}, 0600) != nil {
		t.Fatal("receipt fixture unavailable")
	}
	if _, found, err := c.loadManagedProcess(cfg.InstallationID, false); err != nil || found {
		t.Fatal("historical receipt adopted as managed state")
	}
	if writeJSON(paths.processState, processState{PID: 46, Executable: "public-special-native-server.exe"}, 0600) != nil {
		t.Fatal("state fixture unavailable")
	}
	if _, _, err := c.loadManagedProcess(cfg.InstallationID, false); err == nil || !strings.Contains(err.Error(), "creation time") {
		t.Fatal("state missing creation time accepted")
	}
	// The method returns before inspectProcess in every test call above.
}

func TestNormalControlNative46NormalCredentialTokenContractUnchanged(t *testing.T) {
	cfg := native46Instance()
	before := cfg
	issued := localAccountToken{AccountID: "dnf:46", Token: strings.ToLower(cfg.Protocol.GameOuterToken[:8]) + strings.Repeat("e", 24), ExpiresAt: time.Now().Add(time.Minute)}
	starts, auths := 0, 0
	auth := func(_ context.Context, received instanceConfig, username, password string) (localAccountToken, error) {
		auths++
		if received.Protocol.Profile != cfg.Protocol.Profile || username != "synthetic_admin" || password != "synthetic-password46" {
			t.Fatal("normal credentials/config were changed")
		}
		return issued, nil
	}
	start := func(owner, argument string) error {
		starts++
		if owner != issued.AccountID || !strings.Contains(argument, issued.Token) || strings.Contains(argument, cfg.Protocol.GameOuterToken) {
			t.Fatal("launch failed to consume only issued response")
		}
		return nil
	}
	if launchWithLocalLoginToken(context.Background(), cfg, "synthetic_admin", "synthetic-password46", auth, start) != nil || starts != 1 || auths != 1 || !reflect.DeepEqual(cfg, before) {
		t.Fatal("normal native one-shot token contract changed")
	}
	starts = 0
	reject := func(context.Context, instanceConfig, string, string) (localAccountToken, error) {
		return localAccountToken{}, errors.New("public synthetic normal auth rejection")
	}
	if launchWithLocalLoginToken(context.Background(), cfg, "synthetic_admin", "synthetic-password46", reject, start) == nil || starts != 0 {
		t.Fatal("normal auth rejection started a client")
	}
}
