package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuffGroupFreshTemplateAndLoadedRuntime(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "deploy", "templates", "instance.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var template instanceConfig
	if err := json.Unmarshal(raw, &template); err != nil {
		t.Fatal(err)
	}
	if template.Protocol.BuffGroupSync != "auto" {
		t.Fatal("fresh template omitted protocol pairing")
	}
	for _, native := range []bool{true, false} {
		cfg := validTestInstance()
		if native {
			cfg = native46Instance()
		}
		cfg.Protocol.BuffGroupSync = template.Protocol.BuffGroupSync
		paths := newProjectPaths(t.TempDir())
		if err := os.MkdirAll(filepath.Dir(paths.instanceExample), 0700); err != nil {
			t.Fatal(err)
		}
		data, err := marshalInstance(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(paths.instanceExample, data, 0600); err != nil {
			t.Fatal(err)
		}
		loaded, err := loadInstance(paths)
		if err != nil {
			t.Fatal(err)
		}
		env := native46Env(loaded)
		if native && env["DNFBRIDGE_BUFF_GROUP_SYNC"] != "on" {
			t.Fatal("fresh native instance lost grouped Buff support")
		}
		if !native && env["DNFBRIDGE_BUFF_GROUP_SYNC"] != "" {
			t.Fatal("legacy template gained grouped Buff packets")
		}
	}
}

func TestBuffGroupExistingInstancesAndExplicitModes(t *testing.T) {
	for _, mode := range []string{"", "auto", "on", "off"} {
		cfg := native46Instance()
		cfg.Protocol.BuffGroupSync = mode
		paths := newProjectPaths(t.TempDir())
		native46WriteInstance(t, paths, cfg)
		loaded, err := loadInstance(paths)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Protocol.BuffGroupSync != mode {
			t.Fatal("load rewrote an existing instance choice")
		}
		want := mode
		if mode == "auto" {
			want = "on"
		}
		if native46Env(loaded)["DNFBRIDGE_BUFF_GROUP_SYNC"] != want {
			t.Fatal("runtime differs from persisted protocol")
		}
	}
	for _, mode := range []string{"ON", "true", "unknown"} {
		cfg := native46Instance()
		cfg.Protocol.BuffGroupSync = mode
		if validateInstance(cfg) == nil {
			t.Fatal("invalid protocol setting accepted")
		}
	}
	legacy := validTestInstance()
	legacy.Protocol.BuffGroupSync = "on"
	if validateInstance(legacy) == nil {
		t.Fatal("grouped protocol enabled for legacy profile")
	}
}

func TestBuffGroupRuntimeSurvivesInheritedEnvironmentScrubbing(t *testing.T) {
	t.Setenv("DNFBRIDGE_BUFF_GROUP_SYNC", "off")
	cfg := native46Instance()
	cfg.Protocol.BuffGroupSync = "auto"
	cmd := exec.Command(os.Args[0], "-test.run=^TestBuffGroupRuntimeChildProbe$")
	cmd.Env = append(runtimeEnvironment(cfg), "DNF90_TEST_BUFF_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "on" {
		t.Fatalf("child runtime did not receive configured Buff protocol: %q %v", out, err)
	}
	cfg.Protocol.BuffGroupSync = ""
	if native46Env(cfg)["DNFBRIDGE_BUFF_GROUP_SYNC"] != "" {
		t.Fatal("unconfigured old instance inherited a protocol override")
	}
}

func TestBuffGroupRuntimeChildProbe(t *testing.T) {
	if os.Getenv("DNF90_TEST_BUFF_CHILD") != "1" {
		return
	}
	_, _ = os.Stdout.WriteString(os.Getenv("DNFBRIDGE_BUFF_GROUP_SYNC"))
	os.Exit(0)
}

func TestBuffGroupProtocolParticipatesInRuntimeIdentity(t *testing.T) {
	cfg := native46Instance()
	cfg.Protocol.BuffGroupSync = "on"
	on, err := desiredServerRuntimeConfigSHA256(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Protocol.BuffGroupSync = "off"
	off, err := desiredServerRuntimeConfigSHA256(cfg)
	if err != nil || on == off {
		t.Fatal("protocol change was absent from runtime identity")
	}
}
