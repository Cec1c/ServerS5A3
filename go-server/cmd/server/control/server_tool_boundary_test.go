package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerIgnoresLegacyGMSettingAndDoesNotExportToolEnvironment(t *testing.T) {
	data, err := json.Marshal(validTestInstance())
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	document["server"].(map[string]any)["gmListen"] = "127.0.0.1:28211"
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "instance.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := decodeInstance(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateInstance(cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DNFBRIDGE_GM_LISTEN", "127.0.0.1:28211")
	t.Setenv("DNFBRIDGE_GM_PASSWORD", "local-tool-only-test-value")
	t.Setenv("DNFBRIDGE_GM_DSN", "local-tool-only-test-dsn")
	for _, entry := range runtimeEnvironment(cfg) {
		if strings.HasPrefix(strings.ToUpper(entry), "DNFBRIDGE_GM_") {
			t.Fatalf("server exported GM environment key %s", strings.SplitN(entry, "=", 2)[0])
		}
	}
	projected, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(projected), "gmListen") {
		t.Fatal("server projection retained obsolete GM configuration")
	}
	unchanged, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != string(data) {
		t.Fatal("reading old configuration modified local source file")
	}
}
