package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestPVPReturnOriginFreezesLatestPositionAndSurvivesPVPTransfer(t *testing.T) {
	for _, cold := range []bool{false, true} {
		s, session, repos := pvpReturnTownFixture(t, nil)
		ctx := context.Background()
		session.selectedCharacterID, session.townSceneReadyCharacterID = 29, 29
		session.accountID = "dnf:1"
		session.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 29, TownID: 39, AreaID: 0, PositionX: 1311, PositionY: 221, MovementCode: 4, PositionValid: true}
		if err := s.captureCurrentOrdinaryChannelExitPosition(session); err != nil {
			t.Fatal(err)
		}
		before, _, _ := repos.Character.Load(ctx, "29")
		if before.Stats["pos_x"] != 1311 || before.Stats["pos_y"] != 221 {
			t.Fatal("丢失进场前最后一次位移")
		}
		origin := currentReconnectLocationSnapshot(before)
		session.channel = channelcatalog.Channel{ID: 75, Type: 8, Group: "pvp"}
		var entered dnfrepo.CharacterRecord
		var err error
		if cold {
			entered, _, _, _, _, err = s.currentCharacterListLoginTransition(ctx, session, 29, before)
		} else {
			entered, _, _, _, _, err = s.currentChannelReconnectTownTransition(ctx, session, 29, before)
			if err == nil {
				entered, err = s.commitCurrentReconnectLocation(ctx, 29, entered)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		// 换另一个决斗频道及重建会话均不得用决斗位置覆盖原位。
		session = &gameSession{channel: channelcatalog.Channel{ID: 192, Type: 24, Group: "pvp"}}
		projected, _, _, _, _, err := s.currentChannelReconnectTownTransition(ctx, session, 29, entered)
		if err != nil {
			t.Fatal(err)
		}
		entered, err = s.commitCurrentReconnectLocation(ctx, 29, projected)
		if err != nil {
			t.Fatal(err)
		}
		session.channel = channelcatalog.Channel{ID: 10, Type: 11, Group: "granfloris"}
		returned, _, _, _, _, err := s.currentChannelReconnectTownTransition(ctx, session, 29, entered)
		if err != nil {
			t.Fatal(err)
		}
		returned, err = s.commitCurrentReconnectLocation(ctx, 29, returned)
		if err != nil || currentReconnectLocationSnapshot(returned) != origin {
			t.Fatalf("返城未恢复原位: %+v %v", returned.Stats, err)
		}
		if returned.Stats[currentPVPReturnOriginMarker] != 0 {
			t.Fatal("退场后残留旧原位")
		}
		if !reflect.DeepEqual(before.Stats["gold"], returned.Stats["gold"]) {
			t.Fatal("原位操作修改资产")
		}
	}
}

func TestPVPReturnOriginRejectsInvalidSavedPoint(t *testing.T) {
	s, _, repos := pvpReturnTownFixture(t, nil)
	c, _, _ := repos.Character.Load(context.Background(), "29")
	if err := s.currentSavePVPReturnOrigin(29, &c); err != nil {
		t.Fatal(err)
	}
	c.Stats["pvp_return_town_id"] = 10
	if _, _, err := s.currentPVPReturnOriginLocation(29, c); err == nil {
		t.Fatal("将决斗地图接受为返城原位")
	}
	c.Stats["pvp_return_town_id"] = 39
	delete(c.Stats, "pvp_return_pos_x")
	if _, _, err := s.currentPVPReturnOriginLocation(29, c); err == nil {
		t.Fatal("用猜测坐标补齐残缺原位")
	}
}

func TestPVPMatchingNativeModeAcceptsLive75OrdinaryRequest(t *testing.T) {
	s := pvpRoomTestService(t)
	p := &gameSession{conn: &bufferConn{}, accountID: "pvp-29", selectedCharacterID: 29, channel: s.catalog.Channels()[1]}
	p.channel.ID, p.channel.Type = 75, 8
	s.bindGameSessionCharacter(p, 29)
	p.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 29, TownID: 10, AreaID: 0, PositionX: 474, PositionY: 234, MovementCode: 5, PositionValid: true}
	// 现场05:41请求包体；不能把未选三角色队伍当成普通1对1的前提。
	body := []byte{1, 0, 0, 0, 0, 255, 255, 255, 255, 255, 255, 255, 255, 0}
	if err := s.handleCurrentPVPMatching(p, 520, body); err != nil {
		t.Fatal(err)
	}
	if len(s.pvpMatching) != 1 || s.pvpMatching[0].gameType != 0 || s.pvpMatching[0].member.total != nil {
		t.Fatal("普通匹配仍被频道编号强制改成三角色")
	}
	packets := totalTestPackets(t, p)
	if len(packets) != 1 || packets[0].Header.MsgID != 520 || !reflect.DeepEqual(packets[0].Body, []byte{1, 1, 0, 0, 0}) {
		t.Fatal("开始匹配应答不符合现用读取器")
	}
	if err := s.handleCurrentPVPMatching(p, 521, []byte{1, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if len(s.pvpMatching) != 0 {
		t.Fatal("普通匹配不能取消")
	}
}
