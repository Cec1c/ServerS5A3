package dnfbridge

import (
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelinfo"
)

func TestRealFiendWarU8DirectoryAndDirectLogin(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_FIENDWAR_U8_DIRECTORY")
	if path == "" {
		t.Skip("requires the migrated live directory")
	}
	t.Setenv(channelDirectoryOwnerRangeGateEnv, "u8")
	s, p, _, _ := directFiendWarLoginFixture(t, 261)
	if len(s.channelsForRequestMode("fiendwar", false)) != 0 {
		t.Fatal("original >255 directory no longer reproduces the live filter")
	}
	s.options.channelInfoFile = path
	catalog, script, err := s.loadChannelAssets()
	if err != nil {
		t.Fatal(err)
	}
	s.catalog, s.channelScript = catalog, script
	channels := s.channelsForRequestMode("fiendwar", false)
	if len(channels) != 3 {
		t.Fatalf("u8 gate still removes FiendWar: %+v", channels)
	}
	downloaded, err := channelinfo.Parse(script)
	if err != nil {
		t.Fatal(err)
	}
	for _, channel := range channels {
		if channel.ID < 244 || channel.ID > 246 || channel.Type != 42 || channel.Port != 10000+channel.ID || !currentDirectFiendWarChannel(channel) {
			t.Fatalf("migrated real channel differs: %+v", channel)
		}
		projected := false
		for _, entry := range downloaded.ChannelsForServer(s.channelAdvertiseID()) {
			if entry.ID == channel.ID && entry.Type == 45 {
				projected = true
			}
		}
		if !projected {
			t.Fatalf("downloaded client script lacks type45 channel%d", channel.ID)
		}
		if endpoint, ok := catalog.ForPort(channel.Port); !ok || endpoint.ID != channel.ID {
			t.Fatal("directory and listening endpoint differ")
		}
		p.channel = channel
		if owner, ok := currentSessionOwnerChannelField(p); !ok || int(owner) != channel.ID {
			t.Fatal("native scene owner differs from the selected channel")
		}
		p.gameEndpointSuccessSent = false
		if err := s.handleCurrentGameEndpointRequest(p, 1, currentObservedChannelReconnectDisplayProbeSize); err != nil {
			t.Fatal(err)
		}
		if p.channel.ID != channel.ID || p.coldChannelLanding != nil || !p.gameEndpointSuccessSent {
			t.Fatal("compatible FiendWar login was redirected")
		}
	}
}
