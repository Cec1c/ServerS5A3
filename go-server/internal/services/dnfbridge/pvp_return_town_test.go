package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func pvpReturnTownFixture(t *testing.T, table *dnftown.Table) (*Service, *gameSession, dnfrepo.Group) {
	t.Helper()
	s, session, repos := newChannelReconnectTest(t)
	if table == nil {
		var err error
		table, err = dnftown.Load(context.Background(), townMoveTestSource{
			"town/town.lst":     "10 `pvp.twn` 38 `ordinary.twn` 39 `hendon.twn`",
			"town/hendon.twn":   "[area]\n0 `new_HendonMyre.map` `[normal]`\n[/area]\n",
			"town/pvp.twn":      "[area]\n0 `Fair_PVP/Gate_Hendon.map` `[gate]` 474 234\n[/area]\n[area]\n1 `Fair_PVP/Hendon.map` `[normal]`\n[/area]\n",
			"town/ordinary.twn": townSeriaFixtureElvengard,
		}, dnftown.Options{})
		if err != nil {
			t.Fatal(err)
		}
	}
	s.townCatalog = table
	c, _, err := repos.Character.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	c.Stats["gold"] = 12345
	c.Stats["pos_y"] = 220
	aggregate := dnfrepomemory.NewMemoryGroup()
	if err := aggregate.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	repos.Character, repos.CharacterAssets, repos.CharacterSettlement = aggregate.Character, aggregate.CharacterAssets, aggregate.CharacterSettlement
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	return s, session, repos
}

func TestPVPReturnTownAllConfiguredChannelKinds(t *testing.T) { assertPVPReturnTown(t, nil) }

func TestRealPVFPVPReturnTown(t *testing.T) {
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
	assertPVPReturnTown(t, table)
}

func assertPVPReturnTown(t *testing.T, table *dnftown.Table) {
	t.Helper()
	for _, id := range []int{75, 191, 192, 193, 194, 195, 196, 197} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			s, session, repos := pvpReturnTownFixture(t, table)
			ctx := context.Background()
			c, _, _ := repos.Character.Load(ctx, "29")
			origin := currentReconnectLocationSnapshot(c)
			_, found := s.townArea(origin.townID, origin.areaID)
			if !found {
				t.Fatal("入场原位地图缺失")
			}
			questBefore, questFound, err := repos.Quest.Load(ctx, "29")
			if err != nil {
				t.Fatal(err)
			}
			kind := uint8(8)
			if id >= 192 {
				kind = 24
			}
			session.channel = channelcatalog.Channel{ID: id, Type: kind, Group: "pvp"}
			projected, town, area, _, path, err := s.currentChannelReconnectTownTransition(ctx, session, 29, c)
			if err != nil || town != 10 || area != 0 || path != "Fair_PVP/Gate_Hendon.map" {
				t.Fatalf("决斗入场错误: %d/%d %q %v", town, area, path, err)
			}
			c, err = s.commitCurrentReconnectLocation(ctx, 29, projected)
			if err != nil {
				t.Fatal(err)
			}
			// 兼容旧版本已保存的决斗场出口原点，房间下门不能把角色送回去。
			currentTownPrevVillageApplyOriginStats(c.Stats, currentTownPositionSnapshot{CharacterID: 29, TownID: 10, AreaID: 0, PositionX: 474, PositionY: 234, MovementCode: 5, PositionValid: true})
			if err := repos.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			session.channel = channelcatalog.Channel{ServerID: 1, ID: 10, Type: 11, Group: "granfloris"}
			session.residentChannel = session.channel
			session.connectionTownActorOwnerChannel = 10
			session.townActorOwnerChannel = 10
			session.selectedCharacterID = 29
			session.channelReconnect = true
			if err := s.sendCurrentChannelReconnectTownEntry(session); err != nil {
				t.Fatal(err)
			}
			stored, _, err := repos.Character.Load(ctx, "29")
			if err != nil {
				t.Fatal(err)
			}
			if stored.Stats["town_id"] != 38 || stored.Stats["area_id"] != 1 {
				t.Fatalf("退场未进入原城镇的赛丽亚房间: %+v", stored.Stats)
			}
			if stored.Stats[currentTownPrevVillageTownStat] != origin.townID || stored.Stats[currentTownPrevVillageAreaStat] != origin.areaID {
				t.Fatal("没有以普通城镇来源替换旧决斗出口")
			}
			if stored.Stats["gold"] != 12345 {
				t.Fatal("退场修改玩家金币")
			}
			packets := splitAllCurrentGameServerUpperPackets(t, session.conn.(*bufferConn).write.Bytes())
			assertImmediateChannelReconnectOrder(t, packets, 10, false)
			_, _, _, row, _, err := s.currentPersistedTownTransition(ctx, session, 29, stored, "")
			if err != nil {
				t.Fatal(err)
			}
			want, err := buildCurrentSceneTransitionBody(byte(stored.Stats["town_id"]), byte(stored.Stats["area_id"]), []currentSceneTransitionRow{row})
			if err != nil {
				t.Fatal(err)
			}
			foundTransition := false
			for _, packet := range packets {
				if packet.Header.MsgID == currentSceneTransitionMsgID {
					foundTransition = true
					if !bytes.Equal(packet.Body, want) {
						t.Fatalf("退场地图发包与保存位置不一致: %x != %x", packet.Body, want)
					}
				}
			}
			if !foundTransition {
				t.Fatal("缺少退场地图通知")
			}
			questAfter, afterFound, err := repos.Quest.Load(ctx, "29")
			if err != nil || questFound != afterFound || !reflect.DeepEqual(questBefore, questAfter) {
				t.Fatal("退场更改任务数据")
			}
			// 决斗退场及后续普通换线共用房间规则，房门来源不得被共用地图覆盖。
			projected, gotTown, gotArea, _, gotPath, err := s.currentChannelReconnectTownTransition(ctx, session, 29, stored)
			if err != nil || gotTown != 38 || gotArea != 1 || !townMapPathLooksLikeSeria(gotPath) {
				t.Fatalf("二次普通换线未进入赛丽亚房间: %d/%d %s %v", gotTown, gotArea, gotPath, err)
			}
			room, _, err := repos.Character.Load(ctx, "29")
			if err != nil || currentReconnectLocationSnapshot(room) != currentReconnectLocationSnapshot(projected) ||
				room.Stats[currentTownPrevVillageTownStat] != origin.townID || room.Stats[currentTownPrevVillageAreaStat] != origin.areaID {
				t.Fatalf("二次普通换线没有同步落点或房门来源：%v %v", room.Stats, err)
			}
		})
	}
}

func TestPVPOrdinaryLoginClearsPVPExitOrigin(t *testing.T) {
	for _, preserve := range []string{"", "preserve"} {
		t.Run("保留开关="+preserve, func(t *testing.T) {
			s, session, repos := pvpReturnTownFixture(t, nil)
			ctx := context.Background()
			c, _, _ := repos.Character.Load(ctx, "29")
			c.Stats["town_id"], c.Stats["area_id"] = 10, 0
			origin := currentTownPositionSnapshot{CharacterID: 29, TownID: 10, AreaID: 0, PositionX: 474, PositionY: 234, MovementCode: 5, PositionValid: true}
			currentTownPrevVillageApplyOriginStats(c.Stats, origin)
			if err := repos.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			session.townPositionSnapshot, session.townPrevVillageSnapshot, session.townSeriaRoomEntrySnapshot = origin, origin, origin
			t.Setenv(currentLoginPersistedTownEnv, "keep")
			t.Setenv(currentLoginFallbackKeepsTownEnv, preserve)
			_, town, area, _, path, err := s.currentCharacterListLoginTransition(ctx, session, 29, c)
			if err != nil || town != 38 || area != 1 || path == "Fair_PVP/Gate_Hendon.map" {
				t.Fatalf("普通重登留在决斗图: %d/%d %s %v", town, area, path, err)
			}
			stored, _, _ := repos.Character.Load(ctx, "29")
			if stored.Stats["town_id"] != 38 {
				t.Fatal("普通重登未保存实际位置")
			}
			if _, exists := stored.Stats[currentTownPrevVillageTownStat]; exists {
				t.Fatal("重登保留决斗出口")
			}
			if session.townPrevVillageSnapshot.PositionValid || session.townSeriaRoomEntrySnapshot.PositionValid {
				t.Fatal("房门仍可指回决斗场")
			}
		})
	}
}

func TestPVPReturnTownRequiresPVFAndTransaction(t *testing.T) {
	for _, dependency := range []string{"PVF", "事务"} {
		t.Run(dependency, func(t *testing.T) {
			s, session, repos := pvpReturnTownFixture(t, nil)
			ctx := context.Background()
			c, _, _ := repos.Character.Load(ctx, "29")
			c.Stats["town_id"], c.Stats["area_id"] = 10, 0
			if err := repos.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			if dependency == "PVF" {
				s.townCatalog = nil
			} else {
				repos.CharacterAssets = nil
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			}
			session.channelReconnect, session.selectedCharacterID = true, 29
			if err := s.sendCurrentChannelReconnectTownEntry(session); err == nil {
				t.Fatal("依赖缺失仍允许退场")
			}
			stored, _, _ := repos.Character.Load(ctx, "29")
			if !reflect.DeepEqual(c, stored) {
				t.Fatal("失败退场写入位置")
			}
			if session.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("位置未提交仍下发场景")
			}
		})
	}
}

func TestPVPReturnTownOriginCleanupIsIdempotentAndKeepsOrdinaryOrigin(t *testing.T) {
	for _, originTown := range []byte{10, 38} {
		t.Run(fmt.Sprint(originTown), func(t *testing.T) {
			s, _, repos := pvpReturnTownFixture(t, nil)
			ctx := context.Background()
			c, _, _ := repos.Character.Load(ctx, "29")
			// 位置已经在普通房间，仍需清除旧决斗出口；真实普通出口则须保持。
			c.Stats["town_id"], c.Stats["area_id"] = 38, 1
			currentTownPrevVillageApplyOriginStats(c.Stats, currentTownPositionSnapshot{CharacterID: 29, TownID: originTown, AreaID: 0, PositionX: 474, PositionY: 234, MovementCode: 5, PositionValid: true})
			if err := repos.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			before := dnfrepo.CloneCharacter(c)
			committed, err := s.commitCurrentReconnectLocation(ctx, 29, c)
			if err != nil {
				t.Fatal(err)
			}
			_, exists := committed.Stats[currentTownPrevVillageTownStat]
			if exists != (originTown == 38) {
				t.Fatalf("出口处理错误: %v", committed.Stats)
			}
			again, err := s.commitCurrentReconnectLocation(ctx, 29, committed)
			if err != nil || !reflect.DeepEqual(committed, again) {
				t.Fatal("重复退场改变已提交记录")
			}
			if !reflect.DeepEqual(c, before) {
				t.Fatal("输入记录被修改")
			}
		})
	}
}
