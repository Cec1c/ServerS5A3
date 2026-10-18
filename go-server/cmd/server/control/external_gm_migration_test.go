package main

import (
	"strings"
	"testing"
)

func TestLegacyGMSettingRequiresMigration(t *testing.T) {
	for _, cfg := range []instanceConfig{validTestInstance(), native46Instance()} {
		if err := validateInstance(cfg); err != nil {
			t.Fatal(err)
		}
		cfg.Server.LegacyGMListen = "127.0.0.1:18112"
		if err := validateInstance(cfg); err == nil || !strings.Contains(err.Error(), "independent DNF90GM") {
			t.Fatalf("legacy GM setting must explicitly require migration: %v", err)
		}
	}
}

func TestGameEnvironmentDoesNotOwnGM(t *testing.T) {
	t.Setenv("DNFBRIDGE_GM_LISTEN", "127.0.0.1:18112")
	t.Setenv("DNFBRIDGE_GM_PASSWORD", "fixture-password")
	t.Setenv("DNFBRIDGE_GM_DSN", "fixture-dsn")
	for _, entry := range runtimeEnvironment(validTestInstance()) {
		if strings.HasPrefix(entry, "DNFBRIDGE_GM_") {
			t.Fatal("game inherited a legacy GM variable")
		}
	}
}
