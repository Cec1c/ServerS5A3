package dnfbridge

import (
	"encoding/binary"
	"path/filepath"
	"reflect"
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

func TestLocalDefaultGroupLandingCoversRepeatRequestsWithoutChangingNamedGroups(t *testing.T) {
	service := &Service{options: options{channelInfoFile: filepath.Join("testdata", "channel_info.etc"), channelServerID: 1}}
	catalog, script, err := service.loadChannelAssets()
	if err != nil {
		t.Fatal(err)
	}
	service.catalog, service.channelScript = catalog, script
	complete := service.channelsForRequestMode("cain", false)
	named := service.channelsForRequestMode("granfloris", false)
	if len(complete) <= 1 || len(named) == 0 {
		t.Fatal("fixture must have a complete default directory and named group")
	}
	service.options.defaultGroupLoginOnly = true
	for _, bootstrap := range []bool{true, false} {
		for _, group := range []string{"", "cain", "[cain]", "`[cain]`"} {
			channels := service.channelsForRequestMode(group, bootstrap)
			if len(channels) != 1 || channels[0].ID != dnfenum.DefaultGameChannelID || !channelcatalog.IsResidentChannel(channels[0]) {
				t.Fatalf("local directory group=%q bootstrap=%t: %+v", group, bootstrap, channels)
			}
		}
	}
	if got := service.channelsForRequestMode("granfloris", false); !reflect.DeepEqual(got, named) {
		t.Fatalf("named group changed: got %+v, want %+v", got, named)
	}
	service.options.defaultGroupLoginOnly = false
	if got := service.channelsForRequestMode("cain", false); !reflect.DeepEqual(got, complete) {
		t.Fatal("disabling local restriction must restore the complete directory")
	}
}
