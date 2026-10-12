package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
)

// gmRegressWithListen round-trips a fully valid instanceConfig through its JSON
// form and sets server.gmListen. Round-tripping keeps these tests compiling against
// both the pre-change and the GM-enabled sources: the field is only ever reached
// through the wire form, so a missing field is an assertion failure, not a compile
// error.
func gmRegressWithListen(t *testing.T, base instanceConfig, listen string) instanceConfig {
	t.Helper()
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	server, ok := doc["server"].(map[string]any)
	if !ok {
		t.Fatal("server object missing from marshalled instance")
	}
	if listen == "" {
		delete(server, "gmListen")
	} else {
		server["gmListen"] = listen
	}
	doc["server"] = server
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var cfg instanceConfig
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func gmRegressEnv(cfg instanceConfig) map[string]string {
	out := map[string]string{}
	for _, entry := range runtimeEnvironment(cfg) {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			out[key] = value
		}
	}
	return out
}

func gmRegressFixtures() map[string]instanceConfig {
	return map[string]instanceConfig{
		"legacy":          validTestInstance(),
		"official_native": native46Instance(),
	}
}

func gmRegressAdminChannelPorts(t *testing.T, cfg instanceConfig) (int, int) {
	t.Helper()
	adminPort, err := listenPort(cfg.Server.AdminListen)
	if err != nil {
		t.Fatal(err)
	}
	channelPort, err := listenPort(cfg.Server.ChannelListen)
	if err != nil {
		t.Fatal(err)
	}
	return adminPort, channelPort
}

// An absent or blank GM listener must emit no GM environment.
// The generated configuration and identity equality assertions below apply to
// the absent-field case; they do not assert raw blank identity equality.
func TestNormalGMDefaultDisabledEmitsNoEnvironment(t *testing.T) {
	for name, base := range gmRegressFixtures() {
		t.Run(name, func(t *testing.T) {
			absent := gmRegressWithListen(t, base, "")
			for _, listen := range []string{"", " \t "} {
				cfg := gmRegressWithListen(t, base, listen)
				if err := validateInstance(cfg); err != nil {
					t.Fatalf("disabled gmListen rejected: %v", err)
				}
				for key := range gmRegressEnv(cfg) {
					if strings.HasPrefix(strings.ToUpper(key), "DNFBRIDGE_GM_") {
						t.Fatalf("disabled gmListen emitted %s", key)
					}
				}
			}
			adminPort, channelPort := gmRegressAdminChannelPorts(t, base)
			if renderServiceConfig(base, adminPort, channelPort) != renderServiceConfig(absent, adminPort, channelPort) {
				t.Fatal("legacy instance without gmListen changed the generated service config")
			}
			if renderLogicConfig(base) != renderLogicConfig(absent) {
				t.Fatal("legacy instance without gmListen changed the generated logic config")
			}
			want, err := desiredServerRuntimeConfigSHA256(absent)
			if err != nil {
				t.Fatal(err)
			}
			got, err := desiredServerRuntimeConfigSHA256(base)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatal("legacy instance without gmListen changed the generated runtime identity")
			}
		})
	}
}

// A legacy instance.json that predates the field must still load and resolve to the
// same runtime identity, with no GM environment.
func TestNormalGMLegacyInstanceWithoutFieldStillLoads(t *testing.T) {
	base := native46Instance()
	paths := newProjectPaths(t.TempDir())
	if err := paths.ensureDirectories(); err != nil {
		t.Fatal(err)
	}
	raw, err := marshalInstance(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.instance, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadInstance(paths)
	if err != nil {
		t.Fatalf("legacy instance without gmListen failed to load: %v", err)
	}
	for key := range gmRegressEnv(loaded) {
		if strings.HasPrefix(strings.ToUpper(key), "DNFBRIDGE_GM_") {
			t.Fatalf("legacy load emitted %s", key)
		}
	}
	want, err := desiredServerRuntimeConfigSHA256(base)
	if err != nil {
		t.Fatal(err)
	}
	got, err := desiredServerRuntimeConfigSHA256(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatal("legacy load changed the runtime identity")
	}
}

// An enabled GM listener must reuse the configured admin token and the exact
// database ownership the logic config already renders.
func TestNormalGMEnabledReusesAdminTokenAndLogicDSN(t *testing.T) {
	for name, base := range gmRegressFixtures() {
		t.Run(name, func(t *testing.T) {
			cfg := gmRegressWithListen(t, base, "127.0.0.1:28211")
			if err := validateInstance(cfg); err != nil {
				t.Fatalf("valid loopback gmListen rejected: %v", err)
			}
			env := gmRegressEnv(cfg)
			if env["DNFBRIDGE_GM_LISTEN"] != "127.0.0.1:28211" {
				t.Fatal("supported GM request missing from the generated environment")
			}
			if env["DNFBRIDGE_GM_PASSWORD"] != cfg.Server.AdminToken {
				t.Fatal("GM password does not reuse the configured admin token")
			}
			dsn := env["DNFBRIDGE_GM_DSN"]
			parsed, err := mysql.ParseDSN(dsn)
			if err != nil {
				t.Fatalf("generated GM DSN invalid: %v", err)
			}
			if parsed.User != cfg.Database.User || parsed.Passwd != cfg.Database.Password ||
				parsed.DBName != cfg.Database.Name || parsed.Net != "tcp" ||
				parsed.Addr != fmt.Sprintf("%s:%d", cfg.Database.Host, cfg.Database.Port) || !parsed.ParseTime {
				t.Fatal("GM DSN differs from the configured database ownership")
			}
			if !strings.Contains(renderLogicConfig(cfg), "mysql_dsn = "+tomlString(dsn)) {
				t.Fatal("GM DSN differs from the generated logic config DSN")
			}
			disabled, err := desiredServerRuntimeConfigSHA256(gmRegressWithListen(t, base, ""))
			if err != nil {
				t.Fatal(err)
			}
			enabled, err := desiredServerRuntimeConfigSHA256(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if enabled == disabled {
				t.Fatal("enabled gmListen is absent from the runtime identity")
			}
		})
	}
}

// Inherited GM fields, including the privilege toggles, must not survive the
// normal runtime environment scrub.
func TestNormalGMInheritedEnvironmentScrubbed(t *testing.T) {
	for _, key := range []string{
		"DNFBRIDGE_GM_LISTEN", "DNFBRIDGE_GM_PASSWORD", "dnfbridge_gm_dsn",
		"DNFBRIDGE_GM_REQUIRE_OFFLINE", "DNFBRIDGE_GM_ITEM_REMOVE",
	} {
		t.Setenv(key, "PUBLIC_BAD_INHERITED")
	}
	cfg := gmRegressWithListen(t, native46Instance(), "127.0.0.1:28211")
	if err := validateInstance(cfg); err != nil {
		t.Fatal(err)
	}
	env := gmRegressEnv(cfg)
	if env["DNFBRIDGE_GM_LISTEN"] != "127.0.0.1:28211" {
		t.Fatal("supported GM request lost after the inherited environment scrub")
	}
	if env["DNFBRIDGE_GM_PASSWORD"] != cfg.Server.AdminToken {
		t.Fatal("inherited GM password survived the scrub")
	}
	for _, key := range []string{"DNFBRIDGE_GM_REQUIRE_OFFLINE", "DNFBRIDGE_GM_ITEM_REMOVE"} {
		if value, ok := env[key]; ok {
			t.Fatalf("inherited GM privilege toggle survived the scrub: %s=%s", key, value)
		}
	}
}

// Only the exact loopback host is accepted for the GM listener.
func TestNormalGMLoopbackContract(t *testing.T) {
	base := validTestInstance()
	if err := validateInstance(gmRegressWithListen(t, base, "127.0.0.1:28211")); err != nil {
		t.Fatalf("strict loopback gmListen rejected: %v", err)
	}
	for _, bad := range []string{
		"0.0.0.0:28211", "192.168.1.113:28211", "localhost:28211", "[::1]:28211",
		"127.0.0.2:28211", ":28211", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:65536", "off",
	} {
		if err := validateInstance(gmRegressWithListen(t, base, bad)); err == nil {
			t.Fatalf("non-contract gmListen %q accepted", bad)
		}
	}
}

// The GM listener must not reuse the channel or admin endpoint, including
// leading-zero numeric aliases; a distinct loopback port stays legal.
func TestNormalGMEndpointCollisionRules(t *testing.T) {
	for name, base := range gmRegressFixtures() {
		t.Run(name, func(t *testing.T) {
			adminPort, channelPort := gmRegressAdminChannelPorts(t, base)
			conflicts := []string{
				base.Server.AdminListen,
				base.Server.ChannelListen,
				fmt.Sprintf("127.0.0.1:0%d", adminPort),
				fmt.Sprintf("127.0.0.1:0%d", channelPort),
			}
			for _, listen := range conflicts {
				if err := validateInstance(gmRegressWithListen(t, base, listen)); err == nil {
					t.Fatalf("gmListen endpoint collision %q accepted", listen)
				}
			}
			for _, listen := range []string{"127.0.0.1:28211", "127.0.0.1:28212"} {
				if err := validateInstance(gmRegressWithListen(t, base, listen)); err != nil {
					t.Fatalf("distinct gmListen %q rejected: %v", listen, err)
				}
			}
		})
	}
}
