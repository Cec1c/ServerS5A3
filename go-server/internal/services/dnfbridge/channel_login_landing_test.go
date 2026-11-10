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
			if len(channels) <= 1 || len(channels) >= len(complete) {
				t.Fatalf("local directory group=%q bootstrap=%t: %+v", group, bootstrap, channels)
			}
			for _, channel := range channels {
				original, exists := catalog.Channel(channel.ID)
				if !exists || !reflect.DeepEqual(original, channel) || channel.ID > currentOwnerChannelByteLimit {
					t.Fatalf("local directory contains a changed or oversized row: %+v", channel)
				}
				// 登录引导只允许常驻城镇频道；刷新目录额外放行团本频道，
				// 否则客户端的频道列表里看不到"团队模式频道"（2026-10-25 现场）。
				// 帝国竞技场（type 39 / [tagtournament]）同理（2026-10-29 现场）。
				if channelcatalog.IsResidentChannel(channel) {
					continue
				}
				if bootstrap || !(channelcatalog.IsRaidChannel(channel) || channelcatalog.IsTagTournamentChannel(channel) || channelcatalog.IsPVPRoomChannel(channel)) {
					t.Fatalf("local directory group=%q bootstrap=%t has a non-resident row: %+v", group, bootstrap, channel)
				}
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

// TestLocalDefaultGroupRefreshKeepsRaidChannelsVisible 锁死 2026-10-25 90CN 现场：
// `DNFBRIDGE_DEFAULT_GROUP_LOGIN_ONLY=true` 曾把默认组目录收窄成"只留常驻城镇频道"，
// 团本整族（type 23/32）被剔除，客户端频道列表里"团队模式频道"直接消失。
// 契约：刷新目录必须同时含常驻城镇频道和团本频道；登录引导仍只含常驻频道，
// 落点候选集不变。
func TestLocalDefaultGroupRefreshKeepsRaidChannelsVisible(t *testing.T) {
	service := &Service{options: options{
		channelInfoFile:       filepath.Join("testdata", "channel_info.etc"),
		channelServerID:       1,
		defaultGroupLoginOnly: true,
	}}
	catalog, script, err := service.loadChannelAssets()
	if err != nil {
		t.Fatal(err)
	}
	service.catalog, service.channelScript = catalog, script

	refresh := service.channelsForRequestMode("cain", false)
	raid := 0
	for _, channel := range refresh {
		if channelcatalog.IsRaidChannel(channel) {
			raid++
		}
	}
	if raid == 0 {
		t.Fatal("refresh directory lost every raid channel; 团队模式频道 would disappear from the client list")
	}
	for _, id := range []int{23, 28, 221, 241} {
		found := false
		for _, channel := range refresh {
			if channel.ID == id {
				found = true
			}
		}
		if !found {
			t.Fatalf("refresh directory missing raid channel %d", id)
		}
	}

	// 决斗场手动建房频道（2026-11-02 现场）：刷新目录必须可见。
	pvpRoomFound := false
	for _, channel := range refresh {
		if channel.ID == 192 && channelcatalog.IsPVPRoomChannel(channel) {
			pvpRoomFound = true
		}
	}
	if !pvpRoomFound {
		t.Fatal("refresh directory missing manual PVP room channel 192")
	}

	// 登录引导那一次仍只给常驻城镇频道：落点候选集不允许因为本次放行而变大。
	bootstrap := service.channelsForRequestMode("cain", true)
	for _, channel := range bootstrap {
		if !channelcatalog.IsResidentChannel(channel) || channelcatalog.IsPVPRoomChannel(channel) {
			t.Fatalf("bootstrap directory must stay resident-only, got %+v", channel)
		}
	}
	// 具名组请求不受影响。
	named := service.channelsForRequestMode("[raid]", false)
	for _, channel := range named {
		if !channelcatalog.IsRaidChannel(channel) {
			t.Fatalf("named raid group returned a non-raid row: %+v", channel)
		}
	}
	if len(named) == 0 {
		t.Fatal("named raid group must not be empty")
	}
}
