package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/platform/config"
)

func TestPartyRelayRemoteEndpointRequiresExplicitConfiguration(t *testing.T) {
	t.Setenv("DNFBRIDGE_PARTY_UDP_RELAY_ENABLED", "true")
	t.Setenv(partyUDPRelayModeEnv, partyUDPRelayModeValueRemote)
	t.Setenv(partyUDPRelayRemoteSecretEnv, partyRemoteTestSecret)
	t.Setenv(partyUDPRelayRemoteRoomBaseEnv, "100000")
	t.Setenv("DNFBRIDGE_SERVER_IP", "192.0.2.50")
	t.Setenv(partyUDPRelayRemoteEndpointEnv, "")

	opts := optionsFromConfig(config.ServiceConfig{})
	if opts.partyUDPRelayRemoteEndpoint != "" {
		t.Fatal("an unconfigured relay must not select a built-in external endpoint")
	}
	service := &Service{options: opts}
	if plan := service.resolvePartyUDPRelayRemotePlan(); plan.effective != partyUDPRelayModeEmbedded || plan.reason != partyUDPRelayRemoteReasonEndpointInvalid {
		t.Fatalf("missing endpoint plan = (%v, %q)", plan.effective, plan.reason)
	}

	t.Setenv(partyUDPRelayRemoteEndpointEnv, "relay.example.test:10250")
	service.options = optionsFromConfig(config.ServiceConfig{})
	plan := service.resolvePartyUDPRelayRemotePlan()
	if plan.effective != partyUDPRelayModeRemote || plan.settings.endpoint != "relay.example.test:10250" {
		t.Fatalf("explicit endpoint plan = (%v, %q, %q)", plan.effective, plan.reason, plan.settings.endpoint)
	}
}
