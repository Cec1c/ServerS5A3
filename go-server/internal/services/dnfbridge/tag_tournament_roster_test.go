package dnfbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/tagtournament"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 编号、金币与效果均为隔离合成输入；真实PVF另有不跳过的显式专项。
func tagRosterTestSource(t *testing.T) townMoveTestSource {
	t.Helper()
	const config = `[daily default reward count]
1
[daily default extra reward count]
0
[tag tournament reward]
0 100 2 100 1 101 3 100
[/tag tournament reward]
[tag tournament special reward]
0 100 5 100 1 101 3 100
[/tag tournament special reward]
[tag tournament return village time]
5 30 10
[tag tournament random map]
4 11 21 12 22 13 23 14 24 15 25
[/tag tournament random map]
[tag tournament special map]
1 16 26 17 27
[/tag tournament special map]
[tag tournament hard map]
7 31 41 32 42 33 43 34 44 35 45 36 46 37 47
[/tag tournament hard map]
[tag tournament stackable]
200 201 202 203 204 205
[/tag tournament stackable]
[tag tournament enter gold]
150000
[tag tournament buff add stat]
4000
[tag tournament hard mode buff add state]
10000
[tag tournament hard mode balance]
7 1 1.1 1.3 1.7 2.1 2.5 3.1
[/tag tournament hard mode balance]
[tag tournament dungeon list]
320 336
[/tag tournament dungeon list]
[tag tournament dungeon charac tag cool time]
5
`
	var rewards strings.Builder
	rewards.WriteString("[tag tournament hard reward]\n")
	for i, n := range []int{3, 6, 9, 13, 17, 21, 27} {
		fmt.Fprintf(&rewards, "%d 100 %d %d 101 %d\n", i, n, i, n)
	}
	rewards.WriteString("[/tag tournament reward hard]\n")
	source := townMoveTestSource{tagtournament.ConfigPath: config + rewards.String()}
	add := func(kind string, id int, body string) {
		file := fmt.Sprintf("%d.pvf", id)
		source[kind+"/"+kind+".lst"] += fmt.Sprintf("%d `%s`\n", id, file)
		source[kind+"/"+file] = "[name]\n`合成资源`\n" + body
	}
	for _, pair := range [][2]int{{320, 1}, {336, 2}} {
		add("dungeon", pair[0], fmt.Sprintf("[minimum required level]\n90\n[basis level]\n%d\n[tag tournament dungeon]\n%d\n[disable exp]\n[disable drop item]\n[no fatigue]\n", 80+pair[1]*20, pair[1]))
	}
	for _, pair := range [][2]int{{11, 21}, {12, 22}, {13, 23}, {14, 24}, {15, 25}, {16, 26}, {17, 27}, {31, 41}, {32, 42}, {33, 43}, {34, 44}, {35, 45}, {36, 46}, {37, 47}} {
		add("map", pair[0], fmt.Sprintf("[monster]\n%d 1 0 100 100 0 1 1 `[fixed]` `[normal]`\n[/monster]\n", pair[1]))
		add("monster", pair[1], "")
	}
	add("stackable", 100, "")
	add("stackable", 101, "")
	for id := 200; id < 206; id++ {
		add("stackable", id, "[cool time]\n20000\n[stackable type]\n`[unlimited waste]` 1\n[hp recovery]\n`%` 30 1000 `myself`\n")
	}
	return source
}

func tagRosterTestCatalog(t *testing.T) *tagtournament.Catalog {
	t.Helper()
	c, err := tagtournament.LoadCatalog(tagRosterTestSource(t))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newTagRosterBridgeTest(t *testing.T) (*Service, *gameSession, repo.Group) {
	t.Helper()
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{"无关账号项": "保留"}}); err != nil {
		t.Fatal(err)
	}
	rows := []repo.CharacterRecord{
		{CharacterID: "60", AccountID: "a", Slot: 21, Name: "队员二", Level: 90, Job: "0", Stats: map[string]int64{"gold": 1000000}},
		{CharacterID: "17", AccountID: "a", Slot: 2, Name: "低等级", Level: 89, Job: "1", Stats: map[string]int64{"gold": 1000000}},
		{CharacterID: "29", AccountID: "a", Slot: 8, Name: "当前角色", Level: 90, Job: "2", Stats: map[string]int64{"gold": 1000000, "town_id": 48}, Location: repo.CharacterLocation{TownID: 48}},
	}
	for _, ch := range rows {
		if err := g.Character.Save(ctx, ch); err != nil {
			t.Fatal(err)
		}
	}
	p := &gameSession{accountID: "a", loginPacketSeen: true, selectedCharacterID: 29, characterGeneration: 1, sceneBootstrapTailSent: true, conn: &bufferConn{}, channel: channelcatalog.Channel{ID: 88, ServerID: 1, Type: 39}, currentRenameRoster: rows}
	s := &Service{repositoryProvider: func() (repo.Group, bool) { return g, true }, gameSessions: map[uint16]*gameSession{29: p}, tagTournamentCatalog: tagRosterTestCatalog(t)}
	s.options.currentClientVersion = "official-24.9"
	s.options.channelServerID = 1
	return s, p, g
}

func TestTagTournamentRosterUsesSentCompactPositionsWithoutCharging(t *testing.T) {
	s, p, g := newTagRosterBridgeTest(t)
	ctx := context.Background()
	body := []byte{0, 2, 0, 0}
	for n := 0; n < 3; n++ {
		p.conn.(*bufferConn).write.Reset()
		handled, err := currentGameplayModules.DispatchUpper(s, p, 1457, 1, body)
		if !handled || err != nil {
			t.Fatal(handled, err)
		}
		ack, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
		if ack.Header.MsgID != 1457 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1, 0, 2, 0, 0}) || len(rest) != 0 {
			t.Fatal("设置队员应答错位或产生1388循环", ack, rest)
		}
	}
	o, err := s.currentTagTournamentOwner()
	if err != nil {
		t.Fatal(err)
	}
	view, err := o.Lobby(ctx, "a")
	if err != nil || !view.Registered || view.Characters[0].CharacterID != "29" || view.Characters[1].CharacterID != "60" || view.Day.Paid {
		t.Fatal(view, err)
	}
	for _, id := range []string{"29", "60"} {
		ch, _, _ := g.Character.Load(ctx, id)
		box, _, _ := g.Mailbox.Load(ctx, id)
		if ch.Stats["gold"] != 1000000 || len(box.Mails) != 0 {
			t.Fatal("设置队员扣钱或发奖")
		}
	}
	p.conn.(*bufferConn).write.Reset()
	if err = s.sendCurrentTagTournamentLobby(p); err != nil {
		t.Fatal(err)
	}
	team, rest := splitCurrentGameServerUpperPacketAuto(t, tagDailyPacketsRest(t, p.conn.(*bufferConn).write.Bytes()))
	if team.Header.MsgID != 1388 || team.Header.Classification != 0 || !bytes.Equal(team.Body, body) || len(rest) != 0 {
		t.Fatal("登记恢复不是原紧凑序号", team, rest)
	}
}

func TestTagTournamentLobbyDoesNotAutoChooseOrWriteAccount(t *testing.T) {
	s, p, g := newTagRosterBridgeTest(t)
	ctx := context.Background()
	before, _, _ := g.Account.Load(ctx, "a")
	if err := s.sendCurrentTagTournamentLobby(p); err != nil {
		t.Fatal(err)
	}
	if len(tagDailyPacketsRest(t, p.conn.(*bufferConn).write.Bytes())) != 0 {
		t.Fatal("未注册时不应发队伍或自动打开选择框")
	}
	after, _, _ := g.Account.Load(ctx, "a")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("进入休息室写入了账号")
	}
}

func TestTagTournamentRosterRejectsInvalidRequestsAndStaleIdentity(t *testing.T) {
	for _, scenario := range []string{"截断", "尾随", "重复角色", "跨服务器", "SQL槽位冒充序号", "低等级", "旧姓名", "旧槽位", "删除角色", "未就绪", "战斗中", "错误城镇", "缺少已发角色表", "旧会话", "跨账号"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, g := newTagRosterBridgeTest(t)
			ctx := context.Background()
			body := []byte{0, 2, 0, 0}
			switch scenario {
			case "截断":
				body = body[:3]
			case "尾随":
				body = append(body, 0)
			case "重复角色":
				body[3] = 2
			case "跨服务器":
				body[2] = 2
			case "SQL槽位冒充序号":
				body[3] = 21
			case "低等级":
				body[3] = 1
			case "未就绪":
				p.sceneBootstrapTailSent = false
			case "战斗中":
				p.dungeon.runtime = &runtimeDungeonState{}
			case "缺少已发角色表":
				p.currentRenameRoster = nil
			case "旧会话":
				s.gameSessions[29] = &gameSession{}
			case "跨账号":
				p.currentRenameRoster[0].AccountID = "b"
			default:
				id := "60"
				if scenario == "错误城镇" {
					id = "29"
				}
				ch, _, _ := g.Character.Load(ctx, id)
				switch scenario {
				case "旧姓名":
					ch.Name = "改过的名"
				case "旧槽位":
					ch.Slot = 20
				case "删除角色":
					ch.Slot = -1
				case "错误城镇":
					ch.Stats["town_id"] = 38
				}
				if err := g.Character.Save(ctx, ch); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := g.Account.Load(ctx, "a")
			if err := s.handleCurrentTagTournamentSetRoster(p, body); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
			if ack.Header.MsgID != 1457 || !bytes.Equal(ack.Body, []byte{0, 3}) || len(rest) != 0 {
				t.Fatal(ack, rest)
			}
			after, _, _ := g.Account.Load(ctx, "a")
			ch, _, _ := g.Character.Load(ctx, "29")
			if !reflect.DeepEqual(before, after) || ch.Stats["gold"] != 1000000 {
				t.Fatal("非法请求修改了名单或金币")
			}
		})
	}
}

func TestTagTournamentRosterUsesCommittedTownStatsWhenLocationMirrorIsStale(t *testing.T) {
	s, p, g := newTagRosterBridgeTest(t)
	ctx := context.Background()
	ch, _, _ := g.Character.Load(ctx, "29")
	ch.Location = repo.CharacterLocation{TownID: 38, DungeonID: 123}
	if err := g.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	if err := s.handleCurrentTagTournamentSetRoster(p, []byte{0, 2, 0, 0}); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
	if !bytes.Equal(ack.Body, []byte{1, 0, 2, 0, 0}) || len(rest) != 0 {
		t.Fatal("旧Location镜像误拒绝实际休息室队员", ack)
	}
	updated, _, _ := g.Character.Load(ctx, "29")
	if updated.Stats["gold"] != 1000000 || updated.Stats["town_id"] != 48 || updated.Location != ch.Location {
		t.Fatal("队员设置修改了资产或落点")
	}
}

func TestTagTournamentRouteIsCurrentProfileAndClassificationOnly(t *testing.T) {
	for _, scenario := range []string{"其他频道", "旧配套", "错误分类"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, g := newTagRosterBridgeTest(t)
			class := byte(1)
			switch scenario {
			case "其他频道":
				p.channel.Type = 1
			case "旧配套":
				s.options.currentClientVersion = "legacy"
			case "错误分类":
				class = 0
			}
			before, _, _ := g.Account.Load(context.Background(), "a")
			_, err := currentGameplayModules.DispatchUpper(s, p, 1457, class, []byte{0, 2, 0, 0})
			if err != nil || p.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("帝国请求影响其他配套", err)
			}
			after, _, _ := g.Account.Load(context.Background(), "a")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("跨范围请求写账号")
			}
		})
	}
}

func TestTagTournamentReconnectUsesOnlySameClientSentRoster(t *testing.T) {
	for _, scenario := range []string{"同一进程", "不同进程", "不同账号", "无进程号"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, g := newTagRosterBridgeTest(t)
			p.clientPID = 12345
			s.rememberCurrentRenameRoster(p, p.currentRenameRoster)
			// 新连接本身没有再发op2；保存的紧凑顺序仍为60、17、29，SQL槽位21、2、8。
			p = &gameSession{accountID: "a", clientPID: 12345, loginPacketSeen: true,
				selectedCharacterID: 29, characterGeneration: 2, sceneBootstrapTailSent: true,
				conn: &bufferConn{}, channel: p.channel}
			s.gameSessions[29] = p
			switch scenario {
			case "不同进程":
				p.clientPID++
			case "不同账号":
				p.accountID = "b"
			case "无进程号":
				p.clientPID = 0
			}
			if err := s.handleCurrentTagTournamentSetRoster(p, []byte{0, 2, 0, 0}); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
			if len(rest) != 0 {
				t.Fatal(rest)
			}
			if scenario == "同一进程" {
				if !bytes.Equal(ack.Body, []byte{1, 0, 2, 0, 0}) {
					t.Fatal("切频道丢失真实角色序号", ack)
				}
			} else if !bytes.Equal(ack.Body, []byte{0, 3}) {
				t.Fatal("继承了另一个客户端的角色表", ack)
			}
			ch, _, _ := g.Character.Load(context.Background(), "29")
			if ch.Stats["gold"] != 1000000 {
				t.Fatal("切频道设置队员扣费")
			}
		})
	}
}

func TestTagTournamentNewRosterInvalidatesOldReconnectCache(t *testing.T) {
	for _, scenario := range []string{"空列表", "错误归属"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, _ := newTagRosterBridgeTest(t)
			p.clientPID = 12345
			s.rememberCurrentRenameRoster(p, p.currentRenameRoster)
			var next []repo.CharacterRecord
			if scenario == "错误归属" {
				next = []repo.CharacterRecord{{CharacterID: "29", AccountID: "其他账号", Slot: 8, Name: "当前角色"}}
			}
			s.rememberCurrentRenameRoster(p, next)
			p.currentRenameRoster = nil
			if err := s.handleCurrentTagTournamentSetRoster(p, []byte{0, 2, 0, 0}); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
			if !bytes.Equal(ack.Body, []byte{0, 3}) || len(rest) != 0 {
				t.Fatal("新角色表失效后仍使用旧缓存", ack)
			}
		})
	}
}

func TestRealTagTournamentRosterRegistrationAndLobbyProjection(t *testing.T) {
	name := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if name == "" {
		t.Skip("未提供真实PVF")
	}
	a, err := platformpvf.LoadArchive(platformpvf.Options{Path: name})
	if err != nil {
		t.Fatal(err)
	}
	c, err := tagtournament.LoadCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	s, p, g := newTagRosterBridgeTest(t)
	s.tagTournamentCatalog = c
	if err = s.handleCurrentTagTournamentSetRoster(p, []byte{0, 2, 0, 0}); err != nil {
		t.Fatal(err)
	}
	o, err := s.currentTagTournamentOwner()
	if err != nil {
		t.Fatal(err)
	}
	view, err := o.Lobby(context.Background(), "a")
	if err != nil || !view.Registered || view.Day.Paid {
		t.Fatal(view, err)
	}
	ch, _, _ := g.Character.Load(context.Background(), "29")
	if ch.Stats["gold"] != 1000000 {
		t.Fatal("真实PVF设置队员扣费")
	}
	p.conn.(*bufferConn).write.Reset()
	if err = s.sendCurrentTagTournamentLobby(p); err != nil {
		t.Fatal(err)
	}
	team, rest := splitCurrentGameServerUpperPacketAuto(t, tagDailyPacketsRest(t, p.conn.(*bufferConn).write.Bytes()))
	if team.Header.MsgID != 1388 {
		t.Fatal("已注册时进入休息室未恢复队伍", team)
	}
	weekly, rest := splitCurrentGameServerUpperPacketAuto(t, rest)
	if weekly.Header.MsgID != 1530 || weekly.Header.Classification != 0 || len(weekly.Body) != 13 || !bytes.Equal(weekly.Body, []byte{0, 0, 0, 0, 16, 16, 0, 0, 0x80, 0xa9, 3, 0, 0}) || len(rest) != 0 {
		t.Fatal("真实PVF周挑战基准或无成绩队伍错误", weekly, rest)
	}
}

func TestTagTournamentRosterNativeFixtures(t *testing.T) {
	var fixtures []map[string]any
	for _, count := range []int{2, 3, 17, 255} {
		s, p, g := newTagRosterBridgeTest(t)
		original := p.currentRenameRoster
		rows := make([]repo.CharacterRecord, count)
		rows[0], rows[count-1] = original[0], original[2]
		for i := 1; i < count-1; i++ {
			rows[i] = repo.CharacterRecord{CharacterID: fmt.Sprint(10000 + i), AccountID: "a", Slot: 100 + i, Name: fmt.Sprint("合成队员", i), Level: 90, Job: "0"}
			if i == 1 {
				rows[i] = original[1]
			}
			if err := g.Character.Save(context.Background(), rows[i]); err != nil {
				t.Fatal(err)
			}
		}
		p.currentRenameRoster = rows
		body := []byte{0, byte(count - 1), 0, 0}
		if err := s.handleCurrentTagTournamentSetRoster(p, body); err != nil {
			t.Fatal(err)
		}
		ack, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
		if !bytes.Equal(ack.Body, upperSuccessBody(body)) || len(rest) != 0 {
			t.Fatal(ack, rest)
		}
		p.conn.(*bufferConn).write.Reset()
		if err := s.sendCurrentTagTournamentLobby(p); err != nil {
			t.Fatal(err)
		}
		if count > defaultCharacterSlots {
			// 255仍校验u8队员请求的边界，但不伪装成仓储确有255份已发送角色资料。
			if p.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("无法重读的合成角色明细仍被发送")
			}
			continue
		}
		p.conn.(*bufferConn).write.Reset()
		if err := s.handleCurrentTagTournamentRosterList(p, nil); err != nil {
			t.Fatal(err)
		}
		team, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
		counts, rest := splitCurrentGameServerUpperPacketAuto(t, rest)
		if counts.Header.MsgID != 1403 || !bytes.Equal(counts.Body, []byte{0}) || team.Header.MsgID != 1388 || len(rest) != 0 {
			t.Fatal(counts, team, rest)
		}
		fixtures = append(fixtures, map[string]any{"人数": count, "请求hex": hex.EncodeToString(body), "应答hex": hex.EncodeToString(ack.Body), "人数正文hex": hex.EncodeToString(counts.Body), "队员正文hex": hex.EncodeToString(team.Body)})
	}
	if name := os.Getenv("DNFBRIDGE_TAG_ROSTER_NATIVE_FIXTURES"); name != "" {
		b, err := json.MarshalIndent(fixtures, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(name, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
