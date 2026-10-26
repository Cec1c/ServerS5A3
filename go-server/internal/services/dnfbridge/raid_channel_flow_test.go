package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	dnftown "longheng.io/server/internal/modules/dnf/town"
)

func TestRaidLoginReplyNativeChannelType(t *testing.T) {
	for _, channel := range []channelcatalog.Channel{
		{ID: 10, Type: 11, Group: "granfloris"}, {ID: 75, Type: 8, Group: "pvp"},
		{ID: 23, Type: 23, Group: "anton_raid"}, {ID: 28, Type: 32, Group: "luke_raid"},
		{ID: 184, Type: 42, Group: "fiendwar"}, {ID: 188, Type: 42, Group: "beast"},
	} {
		legacy := (&Service{}).buildLoginSuccess(channel)
		for _, profile := range []string{"official-24.9", "90cn", ""} {
			body := (&Service{options: options{currentClientVersion: profile}}).buildLoginSuccess(channel)
			want := channel.Type
			if profile == "official-24.9" && channel.Type == 42 && channel.Group == "fiendwar" {
				want = 45
			}
			if len(body) < 4 || body[2] != want {
				t.Fatalf("频道%+v登录类型不符：%s %x", channel, profile, body)
			}
			body[2] = legacy[2]
			if !bytes.Equal(body, legacy) {
				t.Fatal("登录应答非目标字段改变")
			}
		}
	}
}

func raidReturnFixture(t *testing.T) (*Service, *gameSession, dnfrepo.Group) {
	t.Helper()
	s, p, repos := newChannelReconnectTest(t)
	table, err := dnftown.Load(context.Background(), townMoveTestSource{
		"town/town.lst":     "19 `anton.twn` 32 `luke.twn` 38 `ordinary.twn` 39 `hendon.twn` 98 `fiend.twn`",
		"town/anton.twn":    "[area]\n0 `Common/(R)Gate_Seria.map` `[gate]` 400 240\n[/area]\n",
		"town/luke.twn":     "[area]\n0 `Common/(R)Gate_Seria.map` `[gate]` 400 240\n[/area]\n",
		"town/ordinary.twn": townSeriaFixtureElvengard,
		"town/hendon.twn":   "[area]\n0 `new_HendonMyre.map` `[normal]`\n[/area]\n",
		"town/fiend.twn":    "[area]\n0 `GContents/180621_Fiendwar/Town/Gate_Fiend.map` `[gate]` 474 234\n[/area]\n",
	}, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.townCatalog = table
	s.fiendWarConfig = &fiendwar.FiendWarConfig{Battle: &fiendwar.FiendWarBattleConfig{WaitingTown: 98, WaitingArea: 2}}
	character, found, err := repos.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("退场测试角色不存在：%t %v", found, err)
	}
	aggregate := dnfrepomemory.NewMemoryGroup()
	if err := aggregate.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	repos.Character, repos.CharacterAssets, repos.CharacterSettlement = aggregate.Character, aggregate.CharacterAssets, aggregate.CharacterSettlement
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	return s, p, repos
}

func TestFiendWarReturnOriginPersistsAndRestores(t *testing.T) {
	for _, coldEntry := range []bool{false, true} {
		for _, coldReturn := range []bool{false, true} {
			s, p, repos := raidReturnFixture(t)
			ctx := context.Background()
			before, _, err := repos.Character.Load(ctx, "29")
			if err != nil {
				t.Fatal(err)
			}
			origin := currentReconnectLocationSnapshot(before)
			quests, present, err := repos.Quest.Load(ctx, "29")
			if err != nil {
				t.Fatal(err)
			}
			p.channel = channelcatalog.Channel{ID: 184, Type: 42, Group: "fiendwar"}
			c := before
			if coldEntry {
				c, _, _, _, _, err = s.currentCharacterListLoginTransition(ctx, p, 29, c)
			} else {
				c, _, _, _, _, err = s.currentChannelReconnectTownTransition(ctx, p, 29, c)
				if err == nil {
					c, err = s.commitCurrentReconnectLocation(ctx, 29, c)
				}
			}
			if err != nil || c.Stats[currentRaidReturnOriginMarker] != 1 || c.Stats["town_id"] != 98 {
				t.Fatalf("入场来源未提交：%+v %v", c.Stats, err)
			}
			p = &gameSession{accountID: before.AccountID, channel: channelcatalog.Channel{ID: 185, Type: 42, Group: "fiendwar"}}
			c, _, _, _, _, err = s.currentChannelReconnectTownTransition(ctx, p, 29, c)
			if err == nil {
				c, err = s.commitCurrentReconnectLocation(ctx, 29, c)
			}
			if err != nil || c.Stats["raid_return_town_id"] != origin.townID {
				t.Fatalf("换分线覆盖来源：%v", err)
			}
			p.channel = channelcatalog.Channel{ID: 10, Type: 11, Group: "granfloris"}
			if coldReturn {
				c, _, _, _, _, err = s.currentCharacterListLoginTransition(ctx, p, 29, c)
			} else {
				c, _, _, _, _, err = s.currentChannelReconnectTownTransition(ctx, p, 29, c)
				if err == nil {
					c, err = s.commitCurrentReconnectLocation(ctx, 29, c)
				}
			}
			if err != nil || currentReconnectLocationSnapshot(c) != origin || c.Stats[currentRaidReturnOriginMarker] != 0 || c.Stats["gold"] != before.Stats["gold"] {
				t.Fatalf("退场或资产异常：冷入%t冷退%t %+v %v", coldEntry, coldReturn, c.Stats, err)
			}
			stored, _, err := repos.Character.Load(ctx, "29")
			if err != nil || currentReconnectLocationSnapshot(stored) != origin {
				t.Fatal("退场只修改内存投影")
			}
			after, stillPresent, err := repos.Quest.Load(ctx, "29")
			if err != nil || present != stillPresent || !reflect.DeepEqual(quests, after) {
				t.Fatal("团本退场修改任务")
			}
		}
	}
}

func TestFiendWarReturnOriginRejectsInvalidAndLegacyFallsBack(t *testing.T) {
	s, p, repos := raidReturnFixture(t)
	c, _, err := repos.Character.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.currentSaveRaidReturnOrigin(29, &c); err != nil {
		t.Fatal(err)
	}
	c.Stats["raid_return_town_id"] = 98
	if _, _, err = s.currentRaidReturnOriginLocation(29, c); err == nil {
		t.Fatal("接受团本为普通来源")
	}
	c.Stats["raid_return_town_id"] = 39
	delete(c.Stats, "raid_return_pos_x")
	if _, _, err = s.currentRaidReturnOriginLocation(29, c); err == nil {
		t.Fatal("补造来源坐标")
	}
	currentClearRaidReturnOrigin(c.Stats)
	c.Stats["town_id"], c.Stats["area_id"] = 98, 0
	p.channel = channelcatalog.Channel{ID: 10, Type: 11}
	_, town, area, _, _, err := s.currentChannelReconnectTownTransition(context.Background(), p, 29, c)
	if err != nil || town != 38 || area != 1 {
		t.Fatalf("旧记录未退出团本：%d/%d %v", town, area, err)
	}
}
