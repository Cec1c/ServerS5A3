package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/tagtournament"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func tagDailyPacketsRest(t *testing.T, wire []byte) []byte {
	t.Helper()
	daily, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
	status, rest := splitCurrentGameServerUpperPacketAuto(t, rest)
	if daily.Header.MsgID != 1395 || daily.Header.Classification != 0 || len(daily.Body) != 64 || status.Header.MsgID != 1398 || status.Header.Classification != 0 || !bytes.Equal(status.Body, []byte{0, 0, 0}) {
		t.Fatal("休息室每日资料契约不符", daily, status)
	}
	return rest
}

func TestTagTournamentDailyWireUsesActualGroupsElementsAndPaidState(t *testing.T) {
	c := tagRosterTestCatalog(t)
	now := time.Date(2026, 10, 19, 8, 0, 0, 0, time.FixedZone("北京时间", 8*3600))
	view, err := c.LobbySchedule(now, tagtournament.DayState{})
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []map[string]any
	for _, mode := range []tagtournament.Mode{0, tagtournament.Normal, tagtournament.Hard} {
		for _, cleared := range []int{0, 2} {
			if mode == 0 && cleared != 0 {
				continue
			}
			v := view
			v.Paid = mode != 0
			v.LockedMode = mode
			v.RewardedRounds = cleared
			daily, status, err := buildCurrentTagTournamentLobbyWire(v)
			if err != nil || len(daily) != 64 || !bytes.Equal(status, []byte{boolByte(mode != 0), byte(mode), byte(cleared)}) {
				t.Fatal(daily, status, err)
			}
			pos := 0
			var expected []map[string]any
			for group, rows := range [][]tagtournament.DailyEncounter{v.Normal.Encounters[:4], v.Normal.Encounters[4:], v.Hard.Encounters} {
				if int(daily[pos]) != len(rows) {
					t.Fatal("三组长度错位", daily)
				}
				pos++
				for _, row := range rows {
					want := map[tagtournament.ElementWeakness]byte{tagtournament.Fire: 0, tagtournament.Ice: 1, tagtournament.Dark: 2, tagtournament.Light: 3, "": 4}[row.Weakness]
					if binary.LittleEndian.Uint32(daily[pos:]) != row.MonsterID || daily[pos+4] != want {
						t.Fatal("怪物或弱点错位", row, daily[pos:pos+5])
					}
					pos += 5
					expected = append(expected, map[string]any{"group": group, "monster": row.MonsterID, "element": want})
				}
			}
			wantCommon := byte(0)
			if v.Hard.CommonWeakness == tagtournament.Physical {
				wantCommon = 1
			}
			if daily[pos] != wantCommon || pos+1 != len(daily) {
				t.Fatal("困难共同弱点错位")
			}
			fixtures = append(fixtures, map[string]any{"mode": mode, "cleared": cleared, "daily": daily, "status": status, "rows": expected, "common": wantCommon})
		}
	}
	if p := os.Getenv("DNFBRIDGE_TAG_LOBBY_NATIVE_FIXTURES"); p != "" {
		data, err := json.MarshalIndent(fixtures, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(p, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, scenario := range []string{"错误模式", "错误数量", "重复怪物", "王有属性", "普通无属性", "未知属性", "未付费锁定", "普通过大进度", "无效共同弱点"} {
		t.Run(scenario, func(t *testing.T) {
			v, err := c.LobbySchedule(now, tagtournament.DayState{})
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "错误模式":
				v.Normal.Mode = tagtournament.Hard
			case "错误数量":
				v.Hard.Encounters = v.Hard.Encounters[:6]
			case "重复怪物":
				v.Hard.Encounters[0].MonsterID = v.Normal.Encounters[0].MonsterID
			case "王有属性":
				v.Normal.Encounters[4].Weakness = tagtournament.Fire
			case "普通无属性":
				v.Normal.Encounters[0].Weakness = ""
			case "未知属性":
				v.Normal.Encounters[0].Weakness = "未知"
			case "未付费锁定":
				v.LockedMode = tagtournament.Normal
			case "普通过大进度":
				v.Paid = true
				v.LockedMode = tagtournament.Normal
				v.RewardedRounds = 6
			case "无效共同弱点":
				v.Hard.CommonWeakness = "未知"
			}
			if _, _, err := buildCurrentTagTournamentLobbyWire(v); err == nil {
				t.Fatal("接受非法每日资料")
			}
		})
	}
}

func TestTagTournamentChallengeLegacyAndUpperNeverChargeBeforeActorCreation(t *testing.T) {
	for _, transport := range []string{"legacy", "upper"} {
		for _, body := range [][]byte{nil, {1}, {2}, {3}, {1, 2}} {
			t.Run(transport+string(body), func(t *testing.T) {
				s, p, g := newTagRosterBridgeTest(t)
				if err := s.handleCurrentTagTournamentSetRoster(p, []byte{0, 2, 0, 0}); err != nil {
					t.Fatal(err)
				}
				p.conn.(*bufferConn).write.Reset()
				before, _, _ := g.Account.Load(context.Background(), "a")
				character, _, _ := g.Character.Load(context.Background(), "29")
				if transport == "legacy" {
					frame := tagRosterLegacyFrame(1, 77, body)
					binary.LittleEndian.PutUint16(frame[1:3], 1458)
					packets, rest, skipped, err := dnfproto.SplitLatestGameStream(frame, 65535)
					if err != nil || skipped != 0 || len(rest) != 0 || len(packets) != 1 {
						t.Fatal(err)
					}
					if err = s.handleGameStreamPacket(p, packets[0]); err != nil {
						t.Fatal(err)
					}
				} else {
					handled, err := currentGameplayModules.DispatchUpper(s, p, 1458, 1, body)
					if !handled || err != nil {
						t.Fatal(handled, err)
					}
				}
				ack, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
				if ack.Header.MsgID != 1458 || !bytes.Equal(ack.Body, []byte{0, 0x13}) || len(rest) != 0 {
					t.Fatal("未完成1389却触发原生成功进图", ack, rest)
				}
				after, _, _ := g.Account.Load(context.Background(), "a")
				ch, _, _ := g.Character.Load(context.Background(), "29")
				box, _, _ := g.Mailbox.Load(context.Background(), "29")
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(character, ch) || len(box.Mails) != 0 || p.dungeon.runtime != nil {
					t.Fatal("双角色准备未完成却改变了金币、账本、邮件或副本")
				}
			})
		}
	}
}

func TestTagTournamentServiceDayRefreshKeepsRosterAndOnlySendsOnce(t *testing.T) {
	s, p, g := newTagRosterBridgeTest(t)
	before, _, _ := g.Account.Load(context.Background(), "a")
	zone := time.FixedZone("北京时间", 8*3600)
	now := time.Date(2026, 10, 19, 5, 59, 59, 0, zone)
	if err := s.sendCurrentTagTournamentDailyLobby(p, now); err != nil {
		t.Fatal(err)
	}
	first := append([]byte(nil), p.conn.(*bufferConn).write.Bytes()...)
	if len(tagDailyPacketsRest(t, first)) != 0 {
		t.Fatal("每日刷新自动打开队伍")
	}
	p.conn.(*bufferConn).write.Reset()
	if err := s.refreshCurrentTagTournamentLobby(p, now); err != nil {
		t.Fatal(err)
	}
	if p.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("同服务日重复刷新")
	}
	now = now.Add(time.Second)
	s.queueCurrentTagTournamentLobbyRefresh(now)
	second := p.conn.(*bufferConn).write.Bytes()
	if len(tagDailyPacketsRest(t, second)) != 0 || bytes.Equal(first, second) || p.tagTournamentLobbyDay != premium.ServiceDayID(now) {
		t.Fatal("06点名单未更新或队伍框被打开")
	}
	after, _, _ := g.Account.Load(context.Background(), "a")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("06点界面刷新改写账号")
	}
	p.conn.(*bufferConn).write.Reset()
	if err := s.refreshCurrentTagTournamentLobby(p, now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if p.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("迟到的旧服务日回调覆盖新名单")
	}
	for _, scenario := range []string{"旧会话", "其他频道", "未就绪", "战斗中"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, _ := newTagRosterBridgeTest(t)
			switch scenario {
			case "旧会话":
				s.gameSessions[29] = &gameSession{}
			case "其他频道":
				p.channel.Type = 1
			case "未就绪":
				p.sceneBootstrapTailSent = false
			case "战斗中":
				p.dungeon.runtime = &runtimeDungeonState{}
			}
			s.queueCurrentTagTournamentLobbyRefresh(now)
			if p.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("越界刷新")
			}
		})
	}
}

func TestRealTagTournamentLobbyBothModesResourcesAndWire(t *testing.T) {
	name := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if name == "" {
		t.Skip("未提供真实PVF")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: name})
	if err != nil {
		t.Fatal(err)
	}
	c, err := tagtournament.LoadCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	view, err := c.LobbySchedule(time.Date(2026, 10, 19, 8, 0, 0, 0, time.FixedZone("北京时间", 8*3600)), tagtournament.DayState{})
	if err != nil {
		t.Fatal(err)
	}
	daily, status, err := buildCurrentTagTournamentLobbyWire(view)
	if err != nil || len(daily) != 64 || !bytes.Equal(status, []byte{0, 0, 0}) {
		t.Fatal(daily, status, err)
	}
	for _, plan := range []tagtournament.DailyPlan{view.Normal, view.Hard} {
		for _, row := range plan.Encounters {
			m, ok := c.Monster(row.MonsterID)
			if !ok || m.Name == "" || m.Path == "" {
				t.Fatal("今日怪物不是实际PVF资源", row)
			}
		}
	}
	if p := os.Getenv("DNFBRIDGE_TAG_LOBBY_REAL_FIXTURES"); p != "" {
		var rows []map[string]any
		for group, entries := range [][]tagtournament.DailyEncounter{view.Normal.Encounters[:4], view.Normal.Encounters[4:], view.Hard.Encounters} {
			for _, row := range entries {
				attr, err := currentTagTournamentElement(row.Weakness)
				if err != nil {
					t.Fatal(err)
				}
				rows = append(rows, map[string]any{"group": group, "monster": row.MonsterID, "element": attr})
			}
		}
		data, err := json.MarshalIndent([]map[string]any{{"mode": 0, "cleared": 0, "daily": daily, "status": status, "rows": rows, "common": daily[len(daily)-1], "real_resources": true}}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(p, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
