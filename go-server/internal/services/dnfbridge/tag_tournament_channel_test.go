package dnfbridge

import (
	"context"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/channelinfo"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	town "longheng.io/server/internal/modules/dnf/town"
	pvf "longheng.io/server/internal/platform/pvf"
)

func TestRealTagTournamentChannelLandingAndReturn(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("未提供真实PVF")
	}
	archive, err := pvf.LoadArchive(pvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	table, err := town.Load(context.Background(), archive, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s, p, repos := newTownMoveTest(t)
	s.options.currentClientVersion = "official-24.9"
	s.townCatalog = table
	c, found, err := repos.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatal(err)
	}
	c.Level = 90
	c.AccountID = "tag-channel-test"
	p.accountID = c.AccountID
	aggregate := memory.NewMemoryGroup()
	if err = aggregate.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	repos.Character, repos.CharacterSettlement = aggregate.Character, aggregate.CharacterSettlement
	repos.CharacterAssets = aggregate.CharacterAssets
	s.repositoryProvider = func() (repo.Group, bool) { return repos, true }
	p.channel = channelcatalog.Channel{ServerID: 1, ID: 187, Type: 39, Group: "tagtournament"}
	p.residentChannel = p.channel
	before := repo.CloneCharacter(c)
	if channelcatalog.IsResidentChannel(p.channel) {
		t.Fatal("帝国频道被作为普通自动登录落点")
	}
	ack, _, err := s.currentSessionSelectAckTownID(p, c, 29, 0)
	if err != nil || ack != 48 {
		t.Fatalf("选角ACK=%d/%v", ack, err)
	}
	for name, transition := range map[string]func(context.Context, *gameSession, uint16, repo.CharacterRecord) (repo.CharacterRecord, byte, byte, currentSceneTransitionRow, string, error){
		"冷选角": s.currentCharacterListLoginTransition, "初次场景": s.currentInitialTownTransition, "换线重连": s.currentChannelReconnectTownTransition,
	} {
		projected, city, area, row, mapPath, e := transition(context.Background(), p, 29, c)
		if e != nil || city != 48 || area != 0 || row.Value1 != 543 || row.Value2 != 255 || mapPath != "Town/CollosseumofEmpire/CollosseumofEmpire.map" || projected.Stats["town_id"] != 48 {
			t.Fatalf("%s落点：%d/%d %+v %s %v", name, city, area, row, mapPath, e)
		}
	}
	if !reflect.DeepEqual(c, before) {
		t.Fatal("入场投影改写了调用者角色")
	}
	stored, found, err := aggregate.Character.Load(context.Background(), "29")
	if err != nil || !found || stored.Stats["town_id"] != 48 || stored.Stats["pos_x"] != 543 {
		t.Fatal("下一次城镇请求无法加载同一位置")
	}
	p.channel = channelcatalog.Channel{ServerID: 1, ID: 11, Type: 11, Group: "granfloris"}
	projected, city, area, _, _, err := s.currentChannelReconnectTownTransition(context.Background(), p, 29, stored)
	if err != nil || city != 38 || area != 1 || projected.Stats["town_id"] == 48 {
		t.Fatalf("普通频道未离开帝国城镇：%d/%d %v", city, area, err)
	}
	p.channel = channelcatalog.Channel{ID: 187, Type: 39, Group: "tagtournament"}
	stored.Level = 89
	if _, _, _, _, _, err = s.currentChannelReconnectTownTransition(context.Background(), p, 29, stored); err == nil {
		t.Fatal("绕过了真实PVF的90级要求")
	}
}

func TestTagTournamentMissingResourcesAndProfileIsolation(t *testing.T) {
	s := &Service{options: options{currentClientVersion: "official-24.9"}}
	if _, err := s.currentTagTournamentLoginLocation(repo.CharacterRecord{Level: 90}); err == nil {
		t.Fatal("缺少PVF区域仍生成了假落点")
	}
	for _, kind := range []uint8{11, 23, 24, 32, 42, 45} {
		if s.isCurrentTagTournamentChannel(&gameSession{channel: channelcatalog.Channel{Type: kind}}) {
			t.Fatalf("其他类型%d进入帝国分支", kind)
		}
	}
	s.options.currentClientVersion = "90cn"
	if s.isCurrentTagTournamentChannel(&gameSession{channel: channelcatalog.Channel{Type: 39}}) {
		t.Fatal("旧配套误用24.9入口")
	}
}

func TestOfficial249TagTournamentDirectoryProjection(t *testing.T) {
	const base = "\n[dungeon]\n`[tagtournament]`\n`帝国竞技场`\n[/dungeon]\n[server]\n" +
		"19 `<chn_channel_info_028>` 11 `[granfloris]` 5 0 0 ``\n" +
		"187 `帝国竞技场` 39 `[tagtournament]` 0 0 0 0 0 0 0 0 0 0 0 ``\n[/server]\n"
	parseCatalog := func(t *testing.T, raw string) *channelcatalog.Catalog {
		t.Helper()
		index, err := channelinfo.Parse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		catalog, err := channelcatalog.New(index, channelcatalog.Options{ServerID: 1})
		if err != nil {
			t.Fatal(err)
		}
		return catalog
	}

	s := &Service{options: options{currentClientVersion: "official-24.9", channelServerID: 1, channelAdvertiseID: 0}}
	catalog := parseCatalog(t, base)
	channel, ok := catalog.Channel(187)
	if !ok || channel.Type != 39 || channel.Group != "tagtournament" || channel.Port != 10187 {
		t.Fatalf("empire channel did not reach catalog/listener projection: %+v ok=%v", channel, ok)
	}

	script, err := s.buildChannelScript([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	online, err := channelinfo.Parse(script)
	if err != nil {
		t.Fatal(err)
	}
	onlineChannel, ok := online.ServerChannel(0, 187)
	if !ok || onlineChannel.Type != 39 || onlineChannel.AreaKey != "tagtournament" || onlineChannel.NameKey != "帝国竞技场" {
		t.Fatalf("empire channel did not reach downloaded directory: %+v ok=%v\n%s", onlineChannel, ok, script)
	}
}

func TestExternalOfficial249TagTournamentChannelDirectory(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_EMPIRE_CHANNEL_INFO")
	if path == "" {
		t.Skip("未提供待部署的完整频道目录")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	index, err := channelinfo.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := channelcatalog.New(index, channelcatalog.Options{ServerID: 1})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{currentClientVersion: "official-24.9", channelServerID: 1, channelAdvertiseID: 0}}
	channels := catalog.Channels()
	if len(channels) != 119 {
		t.Fatalf("完整候选目录应有118条原记录加1条帝国频道，实际%d", len(channels))
	}
	channel, ok := catalog.Channel(187)
	if !ok || channel.Type != 39 || channel.Group != "tagtournament" || channel.Port != 10187 {
		t.Fatalf("帝国频道目录/监听投影不符：%+v ok=%v", channel, ok)
	}
	script, err := s.buildChannelScript(raw)
	if err != nil {
		t.Fatal(err)
	}
	online, err := channelinfo.Parse(script)
	if err != nil {
		t.Fatal(err)
	}
	onlineChannel, ok := online.ServerChannel(0, 187)
	if !ok || onlineChannel.Type != 39 || onlineChannel.AreaKey != "tagtournament" || onlineChannel.NameKey != "帝国竞技场" {
		t.Fatalf("帝国频道未进入动态下载目录：%+v ok=%v", onlineChannel, ok)
	}
}
