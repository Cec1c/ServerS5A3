package dnfbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestTagTournamentLobbyReusesLocalRosterWithoutAppendingDuplicates(t *testing.T) {
	s, p, g := newTagRosterBridgeTest(t)
	ctx := context.Background()
	ch, _, _ := g.Character.Load(ctx, "60")
	ch.Stats["grow_type"] = 0x21
	if err := g.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	// 缓存只保留身份；数据库顺序不同，仍按客户端原紧凑顺序核对。
	for i := range p.currentRenameRoster {
		p.currentRenameRoster[i].Stats = nil
	}
	before, _, _ := g.Account.Load(ctx, "a")
	rows, err := s.currentTagTournamentLobbyCharacters(p, p.currentRenameRoster)
	if err != nil || rows[0].CharacterID != "60" || rows[1].CharacterID != "17" || rows[2].CharacterID != "29" || rows[0].Stats["grow_type"] != 0x21 {
		t.Fatal("角色身份没有按已发顺序重读", rows, err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		p.conn.(*bufferConn).write.Reset()
		if err = s.sendCurrentTagTournamentLobby(p); err != nil {
			t.Fatal(err)
		}
		if len(tagDailyPacketsRest(t, p.conn.(*bufferConn).write.Bytes())) != 0 {
			t.Fatal("未注册时入频道发了重复名单或自动打开通知")
		}
	}
	after, _, _ := g.Account.Load(ctx, "a")
	updated, _, _ := g.Character.Load(ctx, "60")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(ch, updated) {
		t.Fatal("名单投影写入账号或角色")
	}
}

func TestTagTournamentLobbyDoesNotAdvertiseUnresolvableDetails(t *testing.T) {
	for _, scenario := range []string{"改名", "槽位变化", "删除", "归属变化"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, g := newTagRosterBridgeTest(t)
			ctx := context.Background()
			ch, _, _ := g.Character.Load(ctx, "60")
			switch scenario {
			case "改名":
				ch.Name = "已改名"
			case "槽位变化":
				ch.Slot++
			case "删除":
				ch.Slot = -1
			case "归属变化":
				ch.AccountID = "b"
			}
			if err := g.Character.Save(ctx, ch); err != nil {
				t.Fatal(err)
			}
			if err := s.sendCurrentTagTournamentLobby(p); err != nil {
				t.Fatal(err)
			}
			if p.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("失效身份仍被投影")
			}
		})
	}
}

func TestTagTournamentRosterUsesAdvertisedRegionForSaveAndRestore(t *testing.T) {
	var fixtures []map[string]any
	for _, server := range []byte{0, 7, 249} {
		s, p, g := newTagRosterBridgeTest(t)
		s.options.channelAdvertiseID = int(server)
		// 本机仓储/缓存编号为1，对外编号可以为0；不能把它们混用。
		p.clientPID = 12345
		s.rememberCurrentRenameRoster(p, p.currentRenameRoster)
		normal := s.buildCurrentRosterBody(p.currentRenameRoster)
		p.currentRenameRoster = nil
		request := []byte{server, 2, server, 0}
		if err := s.handleCurrentTagTournamentSetRoster(p, request); err != nil {
			t.Fatal(err)
		}
		ack, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
		if !bytes.Equal(ack.Body, upperSuccessBody(request)) || len(rest) != 0 {
			t.Fatal("对外区服号保存失败", server, ack, rest)
		}
		// 重建服务对象，验证名单来自持久化仓储。
		reopened := &Service{repositoryProvider: s.repositoryProvider, options: s.options, tagTournamentCatalog: s.tagTournamentCatalog}
		p.currentRenameRoster = s.cachedCurrentTagTournamentSentRoster(p, 1)
		p.conn.(*bufferConn).write.Reset()
		if err := reopened.sendCurrentTagTournamentLobby(p); err != nil {
			t.Fatal(err)
		}
		team, rest := splitCurrentGameServerUpperPacketAuto(t, tagDailyPacketsRest(t, p.conn.(*bufferConn).write.Bytes()))
		if team.Header.MsgID != 1388 || !bytes.Equal(team.Body, request) || len(rest) != 0 {
			t.Fatal("重新进入未静默恢复队伍", server, team, rest)
		}
		p.conn.(*bufferConn).write.Reset()
		if err := s.handleCurrentTagTournamentRosterList(p, nil); err != nil {
			t.Fatal(err)
		}
		openedTeam, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
		counts, rest := splitCurrentGameServerUpperPacketAuto(t, rest)
		if openedTeam.Header.MsgID != 1388 || !bytes.Equal(openedTeam.Body, request) || counts.Header.MsgID != 1403 || !bytes.Equal(counts.Body, []byte{0}) || len(rest) != 0 {
			t.Fatal("手动打开没有先恢复保存队伍", openedTeam, counts, rest)
		}
		for _, id := range []string{"29", "60"} {
			ch, _, _ := g.Character.Load(context.Background(), id)
			box, _, _ := g.Mailbox.Load(context.Background(), id)
			if ch.Stats["gold"] != 1000000 || len(box.Mails) != 0 {
				t.Fatal("队员保存触发扣费或发奖")
			}
		}
		fixtures = append(fixtures, map[string]any{"server": server, "count": 3, "roster": normal, "counts": counts.Body, "request": request, "ack": ack.Body, "saved": team.Body, "entry_opcodes": []int{1395, 1398, 1388}, "open_opcodes": []int{1388, 1403}})
	}
	if path := os.Getenv("DNFBRIDGE_TAG_TEAM_UI_NATIVE_FIXTURES"); path != "" {
		data, err := json.MarshalIndent(fixtures, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTagTournamentRosterRejectsInternalRegionAndInvalidAdvertisedRange(t *testing.T) {
	for _, server := range []int{0, 7, 250, 255} {
		s, p, g := newTagRosterBridgeTest(t)
		s.options.channelAdvertiseID = server
		before, _, _ := g.Account.Load(context.Background(), "a")
		if err := s.handleCurrentTagTournamentSetRoster(p, []byte{1, 2, 1, 0}); err != nil {
			t.Fatal(err)
		}
		ack, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
		after, _, _ := g.Account.Load(context.Background(), "a")
		if !bytes.Equal(ack.Body, []byte{0, 3}) || len(rest) != 0 || !reflect.DeepEqual(before, after) {
			t.Fatal("错误区服修改账号", server, ack, rest)
		}
	}
}
