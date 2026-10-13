package main

import (
	"strings"
	"testing"
)

func TestChannelScriptEncodingIsOwnedByInstance(t *testing.T) {
	t.Setenv("DNFBRIDGE_CHANNEL_SCRIPT_ENCODING", "inherited-invalid")
	cfg := validTestInstance()
	before, err := desiredServerRuntimeConfigSHA256(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(runtimeEnvironment(cfg), "\n"), "DNFBRIDGE_CHANNEL_SCRIPT_ENCODING=utf8") {
		t.Fatal("default wire encoding changed or inherited override survived")
	}
	cfg.Protocol.ChannelScriptEncoding = "gbk"
	if err := validateInstance(cfg); err != nil {
		t.Fatal(err)
	}
	after, err := desiredServerRuntimeConfigSHA256(cfg)
	if err != nil || before == after {
		t.Fatal("wire encoding was not bound to server runtime identity")
	}
	if !strings.Contains(strings.Join(runtimeEnvironment(cfg), "\n"), "DNFBRIDGE_CHANNEL_SCRIPT_ENCODING=gbk") {
		t.Fatal("instance encoding not forwarded to service")
	}
	cfg.Protocol.ChannelScriptEncoding = "utf16"
	if err := validateInstance(cfg); err == nil {
		t.Fatal("unsupported wire encoding accepted")
	}
}
