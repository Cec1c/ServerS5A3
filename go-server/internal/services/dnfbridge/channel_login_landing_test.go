package dnfbridge

import (
	"encoding/binary"
	"path/filepath"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func TestInitialLoginAdvertisesOnlyOrdinaryLandingAndKeepsManualDirectory(t *testing.T) {
	service := &Service{options: options{channelInfoFile: filepath.Join("testdata", "channel_info.etc"), channelServerID: 1}}
	catalog, script, err := service.loadChannelAssets()
	if err != nil {
		t.Fatal(err)
	}
	service.catalog, service.channelScript = catalog, script
	for _, group := range []string{"", "cain", "[cain]"} {
		initial := service.channelsForRequestMode(group, true)
		if len(initial) != 1 || initial[0].ID != dnfenum.DefaultGameChannelID || !channelcatalog.IsResidentChannel(initial[0]) {
			t.Fatalf("initial directory %q = %+v", group, initial)
		}
	}
	manual := service.channelsForRequestMode("cain", false)
	foundAnton, foundLuke := false, false
	for _, channel := range manual {
		foundAnton = foundAnton || channel.Type == 23
		foundLuke = foundLuke || channel.Type == 32
	}
	if !foundAnton || !foundLuke {
		t.Fatalf("manual directory lost Anton/Luke: Anton=%t Luke=%t", foundAnton, foundLuke)
	}
	foundSpecial := false
	for _, channel := range manual {
		if channel.ID == 251 {
			foundSpecial = true
		}
	}
	if !foundSpecial || len(manual) <= 1 {
		t.Fatal("manual channel switch lost the real complete directory")
	}
	body, err := service.buildChannelInfoBodyFor([]byte("cain"), true)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := decryptCompressedChannelData(body, service.aesKey())
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != 64 || binary.LittleEndian.Uint32(plain[8:12]) != 1 || binary.LittleEndian.Uint32(plain[56:60]) != dnfenum.GamePortBase+dnfenum.DefaultGameChannelID {
		t.Fatalf("wire bootstrap directory = %x", plain)
	}
}

func TestInitialLoginFallsBackToCatalogOrdinaryChannel(t *testing.T) {
	service := oversizedOwnerTestService(t)
	initial := service.channelsForRequestMode("cain", true)
	if len(initial) != 1 || initial[0].ID != 11 {
		t.Fatalf("fallback bootstrap = %+v", initial)
	}
}
