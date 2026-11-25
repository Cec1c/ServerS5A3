package main

import (
	"os"
	"path/filepath"
	"testing"

	"longheng.io/server/internal/partyudprelay"
)

func TestLoadSecretSourceEnvAndFile(t *testing.T) {
	const envKey = "TEST_PARTY_UDP_RELAY_SECRET_HEX"
	const validHex = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	t.Setenv(envKey, validHex)

	raw, source, err := loadSecretSource("", envKey)
	if err != nil || source != "env" {
		t.Fatalf("load env failed: source=%q err=%v", source, err)
	}
	if _, state := partyudprelay.ParseSecretHex(raw); state != "configured" {
		t.Fatalf("env secret state=%q, want configured", state)
	}

	secretPath := filepath.Join(t.TempDir(), "relay.secret")
	if err := os.WriteFile(secretPath, []byte(validHex+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, source, err = loadSecretSource(secretPath, envKey)
	if err != nil || source != "file" {
		t.Fatalf("load file failed: source=%q err=%v", source, err)
	}
	if _, state := partyudprelay.ParseSecretHex(raw); state != "configured" {
		t.Fatalf("file secret state=%q, want configured", state)
	}

	if _, source, err := loadSecretSource(filepath.Join(t.TempDir(), "missing.secret"), envKey); err == nil || source != "file" {
		t.Fatalf("expected missing file error, got source=%q err=%v", source, err)
	}
}
