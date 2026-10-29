package dnfbridge

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
)

// 主干普通换线政策先回赛丽亚房间，再通过房门恢复入帝国前的普通城镇。
func TestImperialSharedLandingPersistsOrdinaryOrigin(t *testing.T) {
	for _, coldEntry := range []bool{false, true} {
		for _, coldReturn := range []bool{false, true} {
			table, err := dnftown.Load(context.Background(), townMoveTestSource{
				"town/town.lst":     "10 `pvp.twn` 38 `ordinary.twn` 39 `hendon.twn` 48 `empire.twn`",
				"town/pvp.twn":      "[area]\n0 `Fair_PVP/Gate_Hendon.map` `[gate]` 474 234\n[/area]\n",
				"town/ordinary.twn": townSeriaFixtureElvengard,
				"town/hendon.twn":   "[area]\n0 `new_HendonMyre.map` `[normal]`\n[/area]\n",
				"town/empire.twn":   "[area]\n0 `Town/CollosseumofEmpire/CollosseumofEmpire.map` `[gate]` 543 255\n[/area]\n",
			}, dnftown.Options{})
			if err != nil {
				t.Fatal(err)
			}
			s, p, repos := pvpReturnTownFixture(t, table)
			s.options.currentClientVersion = "official-24.9"
			ctx := context.Background()
			c, found, err := repos.Character.Load(ctx, "29")
			if err != nil || !found {
				t.Fatalf("普通来源角色读取失败：%t %v", found, err)
			}
			c.Level = 90
			if err = repos.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			before := dnfrepo.CloneCharacter(c)
			origin := currentReconnectLocationSnapshot(c)
			room, roomErr := s.currentInitialTownSeriaLoginLocation()
			if roomErr != nil {
				t.Fatal(roomErr)
			}
			p.channel = channelcatalog.Channel{ServerID: 1, ID: 187, Type: 39, Group: "tagtournament"}
			if channelcatalog.IsResidentChannel(p.channel) {
				t.Fatal("帝国频道进入普通常驻候选")
			}
			ack, _, err := s.currentSessionSelectAckTownID(p, c, 29, 0)
			if err != nil || ack != 48 {
				t.Fatalf("帝国选角应答未使用48：%d %v", ack, err)
			}
			if coldEntry {
				c, _, _, _, _, err = s.currentCharacterListLoginTransition(ctx, p, 29, c)
			} else {
				c, _, _, _, _, err = s.currentChannelReconnectTownTransition(ctx, p, 29, c)
				if err == nil {
					c, err = s.commitCurrentReconnectLocation(ctx, 29, c)
				}
			}
			if err != nil || c.Stats["town_id"] != 48 || c.Stats[currentRaidReturnOriginMarker] != 1 || c.Stats["raid_return_town_id"] != origin.townID {
				t.Fatalf("帝国入场未保存普通来源：冷入%t %+v %v", coldEntry, c.Stats, err)
			}
			// 帝国分线及决斗互转之后，仍恢复第一次入帝国前的普通位置。
			for _, ch := range []channelcatalog.Channel{{ID: 188, Type: 39}, {ID: 75, Type: 8, Group: "pvp"}, {ID: 187, Type: 39}} {
				p.channel = ch
				c, _, _, _, _, err = s.currentChannelReconnectTownTransition(ctx, p, 29, c)
				if err == nil {
					c, err = s.commitCurrentReconnectLocation(ctx, 29, c)
				}
				if err != nil || c.Stats["raid_return_town_id"] != origin.townID {
					t.Fatalf("专用频道互转覆盖普通来源：%+v %v", ch, err)
				}
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
			stored, _, readErr := repos.Character.Load(ctx, "29")
			wantRoom := currentReconnectLocation{townID: int64(room.TownID), areaID: int64(room.AreaID), positionX: room.PositionX, positionY: room.PositionY, direction: int64(room.Direction), areaState: int64(room.AreaState)}
			if err != nil || readErr != nil || currentReconnectLocationSnapshot(c) != wantRoom || currentReconnectLocationSnapshot(stored) != wantRoom {
				t.Fatalf("普通来源房间未持久恢复：冷入%t冷退%t %+v %v %v", coldEntry, coldReturn, c.Stats, err, readErr)
			}
			for _, record := range []dnfrepo.CharacterRecord{c, stored} {
				if record.Stats[currentTownPrevVillageTownStat] != origin.townID || record.Stats[currentTownPrevVillageAreaStat] != origin.areaID || record.Stats[currentTownPrevVillagePosXStat] != origin.positionX || record.Stats[currentTownPrevVillagePosYStat] != origin.positionY || record.Stats[currentTownPrevVillageDirectionStat] != origin.direction {
					t.Fatalf("原普通城镇房门出口丢失：%+v", record.Stats)
				}
			}
			if c.Stats[currentRaidReturnOriginMarker] != 0 || c.Stats[currentPVPReturnOriginMarker] != 0 || c.Stats["gold"] != before.Stats["gold"] || c.Level != before.Level {
				t.Fatal("帝国退场留下旧凭据或改变资产等级")
			}
		}
	}
}
