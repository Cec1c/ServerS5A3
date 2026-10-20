package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func startup50NativeConfig() instanceConfig {
	cfg := validTestInstance()
	cfg.Protocol.Profile = "native-channel-profile-official-24.9"
	cfg.Client.HookCreate = false
	cfg.Database.Host = "127.0.0.1"
	cfg.Database.Port = 23306
	cfg.Database.Name = "controlled_startup50"
	cfg.Game.ShardID = "9999"
	cfg.Server.PartyUDPRelayPortStart = 32000
	cfg.Server.PartyUDPRelayPortCount = 64
	return cfg
}
func TestStartup50PureFlagDefaultAndExplicitChoice(t *testing.T) {
	for _, tc := range []struct {
		args           []string
		reuse, rebuild bool
	}{
		{nil, false, false}, {[]string{"--use-running-database"}, true, false},
		{[]string{"--rebuild", "--use-running-database=false"}, false, true},
		{[]string{"--use-running-database=true", "--rebuild"}, true, true},
	} {
		got, err := parseStartOptions(tc.args, io.Discard)
		if err != nil || got.useRunningDatabase != tc.reuse || got.rebuild != tc.rebuild {
			t.Fatal("normal pure start parser contract mismatch")
		}
	}
	for _, args := range [][]string{{"--existing-pid=26284"}, {"--state=unowned"}, {"--use-running-database=maybe"}, {"extra"}} {
		if _, err := parseStartOptions(args, io.Discard); err == nil {
			t.Fatal("unsupported normal start input accepted")
		}
	}
}
func TestStartup50NativeLoopbackPolicy(t *testing.T) {
	cfg := startup50NativeConfig()
	for _, host := range []string{"127.0.0.1", "localhost"} {
		cfg.Database.Host = host
		got, err := configureStartupDatabase(cfg, true)
		if err != nil || !got.UseRunningDatabase {
			t.Fatal("proved native loopback mode rejected")
		}
	}
	cfg = startup50NativeConfig()
	for _, tc := range []struct {
		name   string
		mutate func(*instanceConfig)
	}{
		{"legacy", func(c *instanceConfig) { c.Protocol.Profile = "90cn-decode-bypass-v1"; c.Client.HookCreate = true }},
		{"unknown", func(c *instanceConfig) { c.Protocol.Profile = "unproved" }},
		{"hook", func(c *instanceConfig) { c.Client.HookCreate = true }},
		{"public", func(c *instanceConfig) { c.Database.Host = "203.0.113.1" }},
		{"wildcard", func(c *instanceConfig) { c.Database.Host = "0.0.0.0" }},
		{"bad_port", func(c *instanceConfig) { c.Database.Port = 0 }},
		{"bad_db", func(c *instanceConfig) { c.Database.Name = "bad database" }},
		{"different_mode", func(c *instanceConfig) { c.Database.Mode = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := cfg
			tc.mutate(&bad)
			if _, err := configureStartupDatabase(bad, true); err == nil {
				t.Fatal("unsupported reuse policy accepted")
			}
		})
	}
	got, err := configureStartupDatabase(validTestInstance(), false)
	if err != nil || got.UseRunningDatabase {
		t.Fatal("portable default altered")
	}
}
func TestStartup50ReuseNeverStartsOrRollsBackExternalDB(t *testing.T) {
	cfg, err := configureStartupDatabase(startup50NativeConfig(), true)
	if err != nil {
		t.Fatal(err)
	}
	started, err := beginStartupDatabase(context.Background(), cfg, func(context.Context, instanceConfig) (bool, error) {
		t.Fatal("reuse called portable database lifecycle")
		return true, nil
	})
	if err != nil || started {
		t.Fatal("reuse falsely claimed owned database startup")
	}
	// Normal start uses this boolean to enter every DB rollback path.
	rollbackCalls := 0
	if started {
		rollbackCalls++
	}
	if rollbackCalls != 0 {
		t.Fatal("external DB rollback attempted")
	}
}
func TestStartup50PortableLifecycleForwardedUnchanged(t *testing.T) {
	cfg := validTestInstance()
	cause := errors.New("controlled portable startup failure")
	calls := 0
	started, err := beginStartupDatabase(context.Background(), cfg, func(ctx context.Context, got instanceConfig) (bool, error) {
		calls++
		if got.UseRunningDatabase || ctx != context.Background() {
			t.Fatal("portable inputs changed")
		}
		return true, cause
	})
	if calls != 1 || !started || err != cause {
		t.Fatal("portable lifecycle/partial-start error changed")
	}
}
func TestStartup50StopDecisionRespectsRealServerMode(t *testing.T) {
	// Pure metadata contract only: no PID, process birth, managed file or live process.
	for _, tc := range []struct{ keep, found, reuse, want bool }{
		{false, true, true, false}, {true, true, true, false},
		{false, true, false, true}, {true, true, false, false},
		{false, false, false, true}, {true, false, false, false},
		{false, false, true, false}, {true, false, true, false},
	} {
		t.Run(fmt.Sprintf("keep_%t_found_%t_reuse_%t", tc.keep, tc.found, tc.reuse), func(t *testing.T) {
			if shouldStopStartupDatabase(tc.keep, tc.found, processState{UsesRunningDatabase: tc.reuse}) != tc.want {
				t.Fatal("DB stop lifecycle decision changed")
			}
		})
	}
}
func TestStartup50RejectsOwnedStateWithoutAdoptingIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "owned-mysql-state-guard")
	if err := rejectOwnedDatabaseState(path); err != nil {
		t.Fatal(err)
	}
	marker := []byte("controlled opaque guard fixture; not a managed PID or state")
	if err := os.WriteFile(path, marker, 0600); err != nil {
		t.Fatal(err)
	}
	if err := rejectOwnedDatabaseState(path); err == nil {
		t.Fatal("mixed DB lifecycle accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(marker) {
		t.Fatal("owned-state guard modified input")
	}
}
func TestStartup50RuntimeReuseDisablesAllInitialization(t *testing.T) {
	cfg, err := configureStartupDatabase(startup50NativeConfig(), true)
	if err != nil {
		t.Fatal(err)
	}
	text := renderLogicConfig(cfg)
	if !strings.Contains(text, "auto_create_schema = false") || !strings.Contains(text, "create_databases = false") {
		t.Fatal("reuse runtime initializes database")
	}
	cfg.UseRunningDatabase = false
	if !strings.Contains(renderLogicConfig(cfg), "auto_create_schema = true") {
		t.Fatal("portable auto-create default changed")
	}
	b, err := marshalInstance(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "UseRunningDatabase") || strings.Contains(string(b), "useRunningDatabase") {
		t.Fatal("CLI-only flag leaked into instance JSON")
	}
}
func TestStartup50RuntimeHashAndOwnedConfigBoundary(t *testing.T) {
	cfg, err := configureStartupDatabase(startup50NativeConfig(), true)
	if err != nil {
		t.Fatal(err)
	}
	paths := newProjectPaths(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(paths.channelAsset), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.channelAsset, []byte("controlled channel fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := generateRuntimeConfigs(paths, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.mysqlConfig); !os.IsNotExist(err) {
		t.Fatal("reuse wrote own MySQL configuration")
	}
	digest, err := desiredServerRuntimeConfigSHA256(cfg)
	if err != nil {
		t.Fatal(err)
	}
	controller := newController(paths, io.Discard, io.Discard)
	// No PID/state file fixture: exercise only the existing pure runtime-hash verifier.
	marker := processState{UsesRunningDatabase: true, RuntimeConfigSHA256: digest}
	fromInstance := cfg
	fromInstance.UseRunningDatabase = false
	if _, err := controller.validateRunningServerConfig(fromInstance, marker); err != nil {
		t.Fatal("saved mode did not restore active hash")
	}
	marker.UsesRunningDatabase = false
	if _, err := controller.validateRunningServerConfig(fromInstance, marker); err == nil {
		t.Fatal("wrong lifecycle falsely matched runtime hash")
	}
	if _, err := os.Stat(paths.processState); !os.IsNotExist(err) {
		t.Fatal("offline verifier produced managed process state")
	}
}
