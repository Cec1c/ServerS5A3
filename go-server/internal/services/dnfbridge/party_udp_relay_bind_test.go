package dnfbridge

import (
	"net"
	"testing"
)

func TestEmbeddedRelaySeparatesBindAndAdvertisement(t *testing.T) {
	settings := currentPartyUDPRelaySettings{enabled: true, advertiseIP: "192.0.2.10", bindHost: "127.0.0.1", portStart: 30000, portCount: 64}
	relay, err := newCurrentPartyUDPRelayWithSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	if !relay.bindIP.Equal(net.ParseIP("127.0.0.1")) || relay.advertise != [4]byte{192, 0, 2, 10} {
		t.Fatalf("incorrect NAT addresses: bind=%v advertise=%v", relay.bindIP, relay.advertise)
	}
	for _, invalid := range []string{"0.0.0.0", "::", "hostname", "::1"} {
		settings.bindHost = invalid
		if _, err := newCurrentPartyUDPRelayWithSettings(settings); err == nil {
			t.Errorf("accepted %q", invalid)
		}
	}
	settings.bindHost = ""
	legacy, err := newCurrentPartyUDPRelayWithSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if !legacy.bindIP.Equal(net.ParseIP(settings.advertiseIP)) {
		t.Fatal("changed default bind behavior")
	}
}
