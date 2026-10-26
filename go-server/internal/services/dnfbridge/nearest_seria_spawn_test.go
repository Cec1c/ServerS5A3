package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestNearestSeriaFollowsPVFDoorTarget(t *testing.T) {
	s, _, _ := pvpReturnTownFixture(t, nil)
	table, err := dnftown.Load(context.Background(), townMoveTestSource{
		"town/town.lst":   "38 `common.twn` 39 `city.twn` 61 `room.twn`",
		"town/common.twn": townSeriaFixtureElvengard,
		"town/city.twn":   "[area]\n0 `city_main.map` `[normal]`\n[/area]\n[area]\n2 `city_back.map` `[normal]`\n[/area]\n",
		"town/room.twn":   "[area]\n4 `local_seria.map` `[gate]` 567 210\n[/area]\n",
	}, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.townCatalog = table
	s.seriaRoomDoorMapReader = func(area dnftown.Area) (worldmap.Map, bool) {
		targetTown, targetArea := int64(38), int64(1)
		if area.ID == 2 {
			targetTown, targetArea = 61, 4
		}
		return worldmap.Map{TownMovableAreas: []worldmap.MovableArea{{X: 100, Y: 100, Width: 50, Height: 30, Params: [2]int64{targetTown, targetArea}}}}, true
	}
	for _, areaID := range []int64{0, 2} {
		c := dnfrepo.CharacterRecord{CharacterID: "29", Stats: map[string]int64{"town_id": 39, "area_id": areaID, "pos_x": 70, "pos_y": 80, "direction": 5, "area_state": 3}}
		location, found := s.currentTownLinkedSeriaLoginLocation(29, c)
		wantTown, wantArea := byte(38), byte(1)
		if areaID == 2 {
			wantTown, wantArea = 61, 4
		}
		if !found || location.TownID != wantTown || location.AreaID != wantArea {
			t.Fatalf("没有跟随当前区域的PVF房门：%+v found=%t", location, found)
		}
	}
}

// 共用地图编号为38/1不等于属于防线。逐城验证普通选角、换线，以及各专用频道
// 返回时的1375、实际op24、持久化房门与首次出房落点，不能只断言房内两个坐标。
func TestRealPVFNearestSeriaSpawnKeepsCityAndDoor(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要真实运行时PVF")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	table, err := dnftown.Load(context.Background(), archive, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	entries := []struct {
		name    string
		channel channelcatalog.Channel
	}{
		{"选角色", channelcatalog.Channel{}},
		{"普通换线", channelcatalog.Channel{}},
		{"无双退场", channelcatalog.Channel{ID: 75, Type: 8, Group: "pvp"}},
		{"传统决斗退场", channelcatalog.Channel{ID: 192, Type: 24, Group: "pvp"}},
		{"安图恩退场", channelcatalog.Channel{ID: 23, Type: 23, Group: "anton_raid"}},
		{"卢克退场", channelcatalog.Channel{ID: 181, Type: 32, Group: "luke_raid"}},
		{"帝国退场", channelcatalog.Channel{ID: 187, Type: 39, Group: "tag_tournament"}},
	}
	for _, city := range []int64{6, 11, 30, 31, 39, 40, 41, 42, 43, 45, 46} {
		for _, entry := range entries {
			t.Run(fmt.Sprintf("城镇%d/%s", city, entry.name), func(t *testing.T) {
				t.Setenv(currentLoginPersistedTownEnv, "")
				t.Setenv(currentLoginForcedSeriaFallbackEnv, "")
				s, p, repos := channelSpawnReturnFixture(t, table)
				s.seriaRoomDoorMapReader = func(area dnftown.Area) (worldmap.Map, bool) { return currentTownAreaMapDocument(archive, area) }
				principal, found := table.FindPrincipalArea(city)
				if !found {
					t.Fatal("真实城镇主街缺失")
				}
				door, found, reason := s.currentSeriaRoomReturnDoorLocation(38, 1, city, principal.ID)
				if !found {
					t.Fatalf("真实城镇房门不存在：%s", reason)
				}
				ctx := context.Background()
				c, _, err := repos.Character.Load(ctx, "29")
				if err != nil {
					t.Fatal(err)
				}
				c.Level = 90
				c.Stats["town_id"], c.Stats["area_id"], c.Stats["pos_x"], c.Stats["pos_y"], c.Stats["direction"], c.Stats["area_state"] = city, principal.ID, 700, 240, 5, 3
				if err := repos.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
				before := dnfrepo.CloneCharacter(c)
				quests, hadQuests, err := repos.Quest.Load(ctx, "29")
				if err != nil {
					t.Fatal(err)
				}
				if entry.channel.ID != 0 {
					p.channel = entry.channel
					var town byte
					c, town, _, _, _, err = s.currentChannelReconnectTownTransition(ctx, p, 29, c)
					if err == nil {
						c, err = s.commitCurrentReconnectLocation(ctx, 29, c)
					}
					if err != nil || !s.currentDedicatedChannelTown(int64(town)) {
						t.Fatalf("特殊频道入场失败：town=%d %v", town, err)
					}
				}
				p.channel = channelcatalog.Channel{ServerID: 1, ID: 10, Type: 11, Group: "granfloris"}
				p.residentChannel = p.channel
				p.selectedCharacterID, p.accountID = 29, c.AccountID
				s.bindGameSessionCharacter(p, 29)
				if entry.name == "选角色" {
					var town, area byte
					var row currentSceneTransitionRow
					c, town, area, row, _, err = s.currentCharacterListLoginTransition(ctx, p, 29, c)
					if err == nil {
						p.initialTownRouteCharacterID, p.initialTownRouteStage = 29, currentInitialTownRoutePlayerStatePrepared
						err = s.sendCurrentInitialTownLocationNotificationsLocked(p, 29, town, area, row)
					}
					if err == nil {
						body, e := buildCurrentSceneTransitionBody(town, area, []currentSceneTransitionRow{row})
						if e != nil {
							t.Fatal(e)
						}
						err = s.sendGameUpperRawClass(p, currentSceneTransitionMsgID, body, 0)
						p.townSceneReadyCharacterID = 29
					}
				} else {
					p.channelReconnect = true
					err = s.sendCurrentChannelReconnectTownEntry(p)
				}
				if err != nil {
					t.Fatal(err)
				}
				stored, _, err := repos.Character.Load(ctx, "29")
				if err != nil || stored.Stats["town_id"] != 38 || stored.Stats["area_id"] != 1 || stored.Stats[currentTownPrevVillageTownStat] != city {
					t.Fatalf("房间或所属城镇错误：%v %v", stored.Stats, err)
				}
				worldFound, sceneFound := false, false
				for _, packet := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
					if packet.Header.MsgID == currentTownWorldTownContextNotificationMsgID {
						worldFound = true
						if len(packet.Body) != 4 || binary.LittleEndian.Uint32(packet.Body) != uint32(city) {
							t.Fatalf("房间误归属防线：%x", packet.Body)
						}
					}
					if packetLooksLikeInitialTownTransition(packet) {
						sceneFound = true
						if packet.Body[0] != 38 || packet.Body[1] != 1 {
							t.Fatalf("出生仍在街道：%x", packet.Body)
						}
					}
				}
				if !worldFound || !sceneFound {
					t.Fatalf("缺少房间/所属城镇通知：world=%t scene=%t", worldFound, sceneFound)
				}
				p.conn.(*bufferConn).write.Reset()
				if err := s.handleCurrentPrevVillage(p, nil); err != nil {
					t.Fatal(err)
				}
				returned, _, err := repos.Character.Load(ctx, "29")
				if err != nil || returned.Stats["town_id"] != city || returned.Stats["area_id"] != int64(door.AreaID) || returned.Stats["pos_x"] != int64(door.PositionX) || returned.Stats["pos_y"] != int64(door.PositionY) {
					t.Fatalf("出房没有回到原城镇实际房门：%v door=%+v %v", returned.Stats, door, err)
				}
				if returned.Stats["gold"] != before.Stats["gold"] || returned.Level != 90 || returned.Stats[currentPVPReturnOriginMarker] != 0 || returned.Stats[currentRaidReturnOriginMarker] != 0 {
					t.Fatal("资产变化或返城标记残留")
				}
				after, hasQuests, err := repos.Quest.Load(ctx, "29")
				if err != nil || hasQuests != hadQuests || !reflect.DeepEqual(quests, after) {
					t.Fatal("换线修改了任务")
				}
				t.Logf("城镇%d 区域%d → 共用赛丽亚；世界城镇%d；出房%d/%d @%d,%d", city, principal.ID, city, door.TownID, door.AreaID, door.PositionX, door.PositionY)
			})
		}
	}
}

// 退出客户端后不能依赖旧会话快照。普通初次登录与重新选择特殊频道
// 分别从同一份已保存角色记录恢复；专用场景不能覆盖普通房间的来源。
func TestRealPVFSpecialChannelReloginUsesFreshSession(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要真实运行时PVF")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	table, err := dnftown.Load(context.Background(), archive, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	channels := []channelcatalog.Channel{
		{ID: 75, Type: 8, Group: "pvp"}, {ID: 191, Type: 8, Group: "pvp"},
		{ID: 192, Type: 24, Group: "pvp"}, {ID: 193, Type: 24, Group: "pvp"}, {ID: 194, Type: 24, Group: "pvp"},
		{ID: 195, Type: 24, Group: "pvp"}, {ID: 196, Type: 24, Group: "pvp"}, {ID: 197, Type: 24, Group: "pvp"},
		{ID: 23, Type: 23, Group: "anton_raid"}, {ID: 181, Type: 32, Group: "luke_raid"},
		{ID: 187, Type: 39, Group: "tagtournament"},
	}
	for _, ch := range channels {
		for _, roomOrigin := range []bool{false, true} {
			for _, ordinaryLogin := range []bool{false, true} {
				t.Run(fmt.Sprintf("下线频道%d/原本在房间%t/重登普通频道%t", ch.ID, roomOrigin, ordinaryLogin), func(t *testing.T) {
					t.Setenv(currentLoginPersistedTownEnv, "")
					t.Setenv(currentLoginForcedSeriaFallbackEnv, "")
					s, old, repos := channelSpawnReturnFixture(t, table)
					s.seriaRoomDoorMapReader = func(area dnftown.Area) (worldmap.Map, bool) { return currentTownAreaMapDocument(archive, area) }
					ctx := context.Background()
					c, _, err := repos.Character.Load(ctx, "29")
					if err != nil {
						t.Fatal(err)
					}
					c.Level = 90
					origin := currentTownPositionSnapshot{CharacterID: 29, TownID: 39, AreaID: 0, PositionX: 700, PositionY: 240, MovementCode: 5, PositionValid: true}
					c.Stats["town_id"], c.Stats["area_id"], c.Stats["pos_x"], c.Stats["pos_y"] = 39, 0, 700, 240
					if roomOrigin {
						c.Stats["town_id"], c.Stats["area_id"], c.Stats["pos_x"], c.Stats["pos_y"] = 38, 1, 433, 311
						currentTownPrevVillageApplyOriginStats(c.Stats, origin)
					}
					if err := repos.Character.Save(ctx, c); err != nil {
						t.Fatal(err)
					}
					old.accountID, old.channel = c.AccountID, ch
					entered, city, _, _, _, err := s.currentCharacterListLoginTransition(ctx, old, 29, c)
					if err != nil {
						t.Fatal(err)
					}
					// 决斗/团本街道或等待区下线，重新选择该频道仍应回其正式入口。
					if principal, found := table.FindPrincipalArea(int64(city)); found && principal.ID != 0 {
						entered.Stats["area_id"], entered.Stats["pos_x"], entered.Stats["pos_y"] = principal.ID, 700, 240
						if err := repos.Character.Save(ctx, entered); err != nil {
							t.Fatal(err)
						}
					}
					persisted, _, err := repos.Character.Load(ctx, "29")
					if err != nil {
						t.Fatal(err)
					}
					before := dnfrepo.CloneCharacter(persisted)
					quests, hadQuests, err := repos.Quest.Load(ctx, "29")
					if err != nil {
						t.Fatal(err)
					}
					fresh := &gameSession{conn: &bufferConn{}, accountID: c.AccountID, selectedCharacterID: 29, channel: ch, residentChannel: ch}
					if ordinaryLogin {
						fresh.channel = channelcatalog.Channel{ID: 10, Type: 11, Group: "granfloris"}
						fresh.residentChannel = fresh.channel
					}
					projected, town, area, row, mapPath, err := s.currentCharacterListLoginTransition(ctx, fresh, 29, persisted)
					if err != nil {
						t.Fatal(err)
					}
					wantTown, wantArea := city, byte(0)
					if ordinaryLogin {
						wantTown, wantArea = 38, 1
					}
					stored, _, err := repos.Character.Load(ctx, "29")
					if err != nil || town != wantTown || area != wantArea || stored.Stats["town_id"] != int64(wantTown) || stored.Stats["area_id"] != int64(wantArea) || currentReconnectLocationSnapshot(stored) != currentReconnectLocationSnapshot(projected) {
						t.Fatalf("新会话重登落点错误：%d/%d %s %v %v", town, area, mapPath, stored.Stats, err)
					}
					if stored.Stats["gold"] != before.Stats["gold"] {
						t.Fatal("重登修改金币")
					}
					if ordinaryLogin {
						if !townMapPathLooksLikeSeria(mapPath) || stored.Stats[currentTownPrevVillageTownStat] != 39 || fresh.townSeriaRoomEntrySnapshot.TownID != 39 || stored.Stats[currentPVPReturnOriginMarker] != 0 || stored.Stats[currentRaidReturnOriginMarker] != 0 {
							t.Fatalf("普通重登丢失原城镇或残留特殊频道来源：%v", stored.Stats)
						}
						fresh.initialTownRouteCharacterID, fresh.initialTownRouteStage = 29, currentInitialTownRoutePlayerStatePrepared
						if err := s.sendCurrentInitialTownLocationNotificationsLocked(fresh, 29, town, area, row); err != nil {
							t.Fatal(err)
						}
						foundWorld := false
						for _, packet := range splitAllCurrentGameServerUpperPackets(t, fresh.conn.(*bufferConn).write.Bytes()) {
							if packet.Header.MsgID == currentTownWorldTownContextNotificationMsgID {
								foundWorld = true
								if len(packet.Body) != 4 || binary.LittleEndian.Uint32(packet.Body) != 39 {
									t.Fatal("普通重登房间归属错误")
								}
							}
						}
						if !foundWorld {
							t.Fatal("冷重登未恢复房间所属城镇")
						}
					} else {
						for _, field := range append([]string{currentPVPReturnOriginMarker, currentRaidReturnOriginMarker}, currentPVPReturnLocationFields[:]...) {
							if field != currentPVPReturnOriginMarker && field != currentRaidReturnOriginMarker {
								for _, prefix := range []string{"pvp_return_", "raid_return_"} {
									if stored.Stats[prefix+field] != before.Stats[prefix+field] {
										t.Fatal("特殊频道重登覆盖入场来源")
									}
								}
							} else if stored.Stats[field] != before.Stats[field] {
								t.Fatal("特殊频道重登清除了入场来源")
							}
						}
					}
					after, hasQuests, err := repos.Quest.Load(ctx, "29")
					if err != nil || hasQuests != hadQuests || !reflect.DeepEqual(quests, after) {
						t.Fatal("重登修改任务")
					}
					if !reflect.DeepEqual(before, persisted) {
						t.Fatal("重登改写调用者角色记录")
					}
					t.Logf("频道%d下线 → 登录频道%d → %d/%d %s", ch.ID, fresh.channel.ID, town, area, mapPath)
				})
			}
		}
	}
}
