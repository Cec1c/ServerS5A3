package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestPVPReturnSeriaReconnectKeepsEntryTown(t *testing.T) {
	assertPVPReturnSeriaEntryTown(t, nil, nil)
}

func TestRealPVFPVPReturnSeriaReconnectKeepsEntryTown(t *testing.T) {
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
	assertPVPReturnSeriaEntryTown(t, table, archive)
}

// 现场先从赫顿玛尔进入共用房间，再进决斗。返城既要保留房内坐标，
// 也要在发1375前恢复房门所属城镇；只断言六个坐标字段会漏掉这个错误。
func assertPVPReturnSeriaEntryTown(t *testing.T, table *dnftown.Table, archive *platformpvf.Archive) {
	t.Helper()
	for _, channelID := range []int{75, 191, 192, 193, 194, 195, 196, 197} {
		t.Run(fmt.Sprint(channelID), func(t *testing.T) {
			t.Setenv(currentTownSeriaWorldTownEnv, "")
			s, session, repos := pvpReturnTownFixture(t, table)
			if archive != nil {
				s.seriaRoomDoorMapReader = func(area dnftown.Area) (worldmap.Map, bool) {
					return currentTownAreaMapDocument(archive, area)
				}
			}
			ctx := context.Background()
			c, _, _ := repos.Character.Load(ctx, "29")
			c.Stats["town_id"], c.Stats["area_id"] = 38, 1
			c.Stats["pos_x"], c.Stats["pos_y"], c.Stats["direction"], c.Stats["area_state"] = 433, 311, 5, 3
			entry := currentTownPositionSnapshot{CharacterID: 29, TownID: 39, AreaID: 0, PositionX: 596, PositionY: 179, MovementCode: 5, PositionValid: true}
			currentTownPrevVillageApplyOriginStats(c.Stats, entry)
			if err := repos.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			kind := uint8(8)
			if channelID >= 192 {
				kind = 24
			}
			pvpSession := &gameSession{channel: channelcatalog.Channel{ID: channelID, Type: kind, Group: "pvp"}}
			entered, _, _, _, _, err := s.currentCharacterListLoginTransition(ctx, pvpSession, 29, c)
			if err != nil {
				t.Fatal(err)
			}
			if entered.Stats[currentPVPReturnOriginMarker] != 1 {
				t.Fatal("入场原位未保存")
			}
			session.selectedCharacterID, session.accountID, session.channelReconnect = 29, "dnf:1", true
			s.bindGameSessionCharacter(session, 29)
			if err := s.sendCurrentChannelReconnectTownEntry(session); err != nil {
				t.Fatal(err)
			}
			stored, _, _ := repos.Character.Load(ctx, "29")
			if stored.Stats["town_id"] != 38 || stored.Stats["area_id"] != 1 || stored.Stats["pos_x"] != 433 || stored.Stats["pos_y"] != 311 {
				t.Fatalf("房内原位丢失：%v", stored.Stats)
			}
			if session.townSeriaRoomEntrySnapshot != entry {
				t.Fatalf("重连未恢复房门来源：%+v", session.townSeriaRoomEntrySnapshot)
			}
			packets := splitAllCurrentGameServerUpperPackets(t, session.conn.(*bufferConn).write.Bytes())
			worldFound := false
			for _, p := range packets {
				if p.Header.MsgID == currentTownWorldTownContextNotificationMsgID {
					worldFound = true
					if len(p.Body) != 4 || p.Body[0] != 39 {
						t.Fatalf("房间世界城镇发错：%x", p.Body)
					}
				}
			}
			if !worldFound {
				t.Fatal("重连缺少房间世界城镇通知")
			}
			session.conn.(*bufferConn).write.Reset()
			if err := s.handleCurrentPrevVillage(session, nil); err != nil {
				t.Fatal(err)
			}
			returned, _, _ := repos.Character.Load(ctx, "29")
			if returned.Stats["town_id"] != 39 || returned.Stats["area_id"] != 0 {
				t.Fatalf("房门错误返回防线：%v", returned.Stats)
			}
			wantY := int64(179)
			if table != nil {
				wantY = 144
			}
			if returned.Stats["pos_x"] != 596 || returned.Stats["pos_y"] != wantY {
				t.Fatalf("房门未采用来源城镇真实落点：%v", returned.Stats)
			}
			if returned.Stats["gold"] != 12345 || returned.Stats[currentPVPReturnOriginMarker] != 0 {
				t.Fatal("返城改资产或残留决斗原位")
			}
		})
	}
}
