package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestOrdinaryChannelSwitchEntersSeriaAndKeepsExitTown(t *testing.T) {
	assertOrdinaryChannelSeriaReconnect(t, nil, nil)
}

func TestRealPVFOrdinaryChannelSwitchEntersSeriaAndKeepsExitTown(t *testing.T) {
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
	assertOrdinaryChannelSeriaReconnect(t, table, archive)
}

func assertOrdinaryChannelSeriaReconnect(t *testing.T, table *dnftown.Table, archive *platformpvf.Archive) {
	t.Helper()
	for _, channelID := range []int{10, 11, 17, 51, 253} {
		for _, startInRoom := range []bool{false, true} {
			t.Run(fmt.Sprintf("频道%d/原本在房间%t", channelID, startInRoom), func(t *testing.T) {
				t.Setenv(currentLoginPersistedTownEnv, "")
				t.Setenv(currentLoginForcedSeriaFallbackEnv, "")
				t.Setenv(currentLoginFallbackKeepsTownEnv, "")
				s, session, repos := pvpReturnTownFixture(t, table)
				if archive != nil {
					s.seriaRoomDoorMapReader = func(area dnftown.Area) (worldmap.Map, bool) {
						return currentTownAreaMapDocument(archive, area)
					}
				}
				ctx := context.Background()
				c, _, err := repos.Character.Load(ctx, "29")
				if err != nil {
					t.Fatal(err)
				}
				origin := currentTownPositionSnapshot{CharacterID: 29, TownID: 39, AreaID: 0, PositionX: 596, PositionY: 179, MovementCode: 5, PositionValid: true}
				if startInRoom {
					c.Stats["town_id"], c.Stats["area_id"] = 38, 1
					currentTownPrevVillageApplyOriginStats(c.Stats, origin)
				} else {
					c.Stats["town_id"], c.Stats["area_id"] = 39, 0
					c.Stats["pos_x"], c.Stats["pos_y"], c.Stats["direction"] = 596, 179, 5
				}
				if err := repos.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
				quests, hadQuests, err := repos.Quest.Load(ctx, "29")
				if err != nil {
					t.Fatal(err)
				}
				session.channel = channelcatalog.Channel{ID: channelID, Type: 11, Group: "granfloris"}
				session.residentChannel = session.channel
				session.selectedCharacterID, session.accountID, session.channelReconnect = 29, c.AccountID, true
				s.bindGameSessionCharacter(session, 29)
				if err := s.sendCurrentChannelReconnectTownEntry(session); err != nil {
					t.Fatal(err)
				}
				stored, _, err := repos.Character.Load(ctx, "29")
				if err != nil || stored.Stats["town_id"] != 38 || stored.Stats["area_id"] != 1 {
					t.Fatalf("普通换线仍落在街道：%v %v", stored.Stats, err)
				}
				if session.townSeriaRoomEntrySnapshot != origin {
					t.Fatalf("房门来源没有保留赫顿玛尔：%+v", session.townSeriaRoomEntrySnapshot)
				}
				foundScene := false
				for _, p := range splitAllCurrentGameServerUpperPackets(t, session.conn.(*bufferConn).write.Bytes()) {
					if packetLooksLikeInitialTownTransition(p) {
						foundScene = true
						if p.Body[0] != 38 || p.Body[1] != 1 {
							t.Fatalf("op24没有进入赛丽亚：%x", p.Body)
						}
					}
					if p.Header.MsgID == currentTownWorldTownContextNotificationMsgID && (len(p.Body) != 4 || p.Body[0] != 39) {
						t.Fatalf("房间世界城镇错误：%x", p.Body)
					}
				}
				if !foundScene {
					t.Fatal("缺少房间场景包")
				}
				session.conn.(*bufferConn).write.Reset()
				if err := s.handleCurrentPrevVillage(session, nil); err != nil {
					t.Fatal(err)
				}
				returned, _, err := repos.Character.Load(ctx, "29")
				if err != nil || returned.Stats["town_id"] != 39 || returned.Stats["area_id"] != 0 || returned.Stats["gold"] != c.Stats["gold"] {
					t.Fatalf("出房间没有返回原城镇或修改资产：%v %v", returned.Stats, err)
				}
				after, stillHadQuests, err := repos.Quest.Load(ctx, "29")
				if err != nil || hadQuests != stillHadQuests || !reflect.DeepEqual(quests, after) {
					t.Fatal("普通换线修改了任务")
				}
			})
		}
	}
}
