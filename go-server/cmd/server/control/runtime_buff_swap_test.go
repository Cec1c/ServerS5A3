package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestControllerNativeBuffSwapProfileAndEnvironmentIsolation(t *testing.T) {
	for _, inherited := range []string{"", "off", "on"} {
		t.Run(inherited, func(t *testing.T) {
			t.Setenv("DNFBRIDGE_BUFF_SWAP_REAPPLY", inherited)
			cfg := native46Instance()
			if got := native46Env(cfg)["DNFBRIDGE_BUFF_SWAP_REAPPLY"]; got != "on" {
				t.Fatalf("official profile did not enable native BUFF persistence: %q", got)
			}
			cfg.Protocol.Profile = "90cn-decode-bypass-v1"
			cfg.Client.HookCreate = true
			if _, exists := native46Env(cfg)["DNFBRIDGE_BUFF_SWAP_REAPPLY"]; exists {
				t.Fatal("native BUFF persistence leaked into legacy profile")
			}
		})
	}
}

func TestControllerNativeBuffSwapRejectsPreFixRunningReceipt(t *testing.T) {
	for _, native := range []bool{true, false} {
		cfg := native46Instance()
		if !native {
			cfg.Protocol.Profile = "90cn-decode-bypass-v1"
			cfg.Client.HookCreate = true
		}
		paths := newProjectPaths(t.TempDir())
		if err := os.MkdirAll(filepath.Dir(paths.channelAsset), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(paths.channelAsset, []byte("synthetic channel"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := generateRuntimeConfigs(paths, cfg); err != nil {
			t.Fatal(err)
		}
		// Model the old controller's persisted receipt, which did not bind the
		// native BUFF policy. Reusing that process would leave the fix disabled.
		identity, err := json.Marshal(struct {
			Server   serverConfig   `json:"server"`
			Database databaseConfig `json:"database"`
			Game     gameConfig     `json:"game"`
			Protocol protocolConfig `json:"protocol"`
		}{cfg.Server, cfg.Database, cfg.Game, cfg.Protocol})
		if err != nil {
			t.Fatal(err)
		}
		var oldConfigs []namedRuntimeConfig
		for _, name := range []string{"dnfbridge.toml", "dnf/logic.toml", "servergroup/plan.json"} {
			data, err := os.ReadFile(filepath.Join(paths.runtimeConfigs, filepath.FromSlash(name)))
			if err != nil {
				t.Fatal(err)
			}
			oldConfigs = append(oldConfigs, namedRuntimeConfig{name: name, data: data})
		}
		oldConfigs = append(oldConfigs, namedRuntimeConfig{name: "runtime-identity.json", data: identity})
		c := newController(paths, io.Discard, io.Discard)
		_, err = c.validateRunningServerConfig(cfg, processState{RuntimeConfigSHA256: digestNamedRuntimeConfig(oldConfigs)})
		if native && err == nil {
			t.Fatal("pre-fix native server was silently reused without BUFF persistence")
		}
		if !native && err != nil {
			t.Fatalf("legacy server identity changed: %v", err)
		}
		current, err := desiredServerRuntimeConfigSHA256(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.validateRunningServerConfig(cfg, processState{RuntimeConfigSHA256: current}); err != nil {
			t.Fatalf("current server receipt rejected: %v", err)
		}
	}
}
