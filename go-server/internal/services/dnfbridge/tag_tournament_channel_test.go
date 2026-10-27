package dnfbridge

import (
	"context"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
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
