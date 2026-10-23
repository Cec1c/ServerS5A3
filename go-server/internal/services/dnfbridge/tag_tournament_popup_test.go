package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func TestTagTournamentChannelEntryIsSilentAndManualRequestOpensAfterRestore(t *testing.T) {
	for _, registered := range []bool{false, true} {
		for _, transport := range []string{"legacy字节流", "upper"} {
			t.Run(transport+map[bool]string{false: "未注册", true: "已保存"}[registered], func(t *testing.T) {
				s, p, g := newTagRosterBridgeTest(t)
				body := []byte{0, 2, 0, 0}
				if registered {
					if err := s.handleCurrentTagTournamentSetRoster(p, body); err != nil {
						t.Fatal(err)
					}
				}
				before, _, _ := g.Account.Load(context.Background(), "a")
				for n := 0; n < 2; n++ {
					p.conn.(*bufferConn).write.Reset()
					if err := s.sendCurrentTagTournamentLobby(p); err != nil {
						t.Fatal(err)
					}
					wire := tagDailyPacketsRest(t, p.conn.(*bufferConn).write.Bytes())
					if !registered {
						if len(wire) != 0 {
							t.Fatal("未注册时进入频道自动弹框", wire)
						}
					} else {
						team, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
						if team.Header.MsgID != 1388 || !bytes.Equal(team.Body, body) || len(rest) != 0 {
							t.Fatal("进入频道没有只静默恢复保存队伍", team, rest)
						}
					}
				}
				p.conn.(*bufferConn).write.Reset()
				if transport == "legacy字节流" {
					frame := tagRosterLegacyFrame(1, 40, nil)
					binary.LittleEndian.PutUint16(frame[1:3], 1467)
					packets, rest, skipped, err := dnfproto.SplitLatestGameStream(frame, 65535)
					if err != nil || skipped != 0 || len(rest) != 0 || len(packets) != 1 || packets[0].Kind != dnfproto.LatestGameStreamLegacy {
						t.Fatal(packets, rest, skipped, err)
					}
					if err = s.handleGameStreamPacket(p, packets[0]); err != nil {
						t.Fatal(err)
					}
				} else {
					handled, err := currentGameplayModules.DispatchUpper(s, p, 1467, 1, nil)
					if !handled || err != nil {
						t.Fatal(handled, err)
					}
				}
				wire := p.conn.(*bufferConn).write.Bytes()
				if registered {
					team, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
					if team.Header.MsgID != 1388 || !bytes.Equal(team.Body, body) {
						t.Fatal("打开通知前未恢复队伍", team)
					}
					wire = rest
				}
				counts, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
				if counts.Header.MsgID != 1403 || !bytes.Equal(counts.Body, []byte{0}) || len(rest) != 0 {
					t.Fatal("点击后未发准确打开通知或重发了本区角色", counts, rest)
				}
				after, _, _ := g.Account.Load(context.Background(), "a")
				if !reflect.DeepEqual(before, after) {
					t.Fatal("查看队伍写入了账号")
				}
				if registered {
					// 1388原生会回写1457；应答只确认保存，不再发1403打开窗口。
					p.conn.(*bufferConn).write.Reset()
					dispatchTagRosterObservedStream(t, s, p, 1, body)
					ack, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
					if ack.Header.MsgID != 1457 || !bytes.Equal(ack.Body, upperSuccessBody(body)) || len(rest) != 0 {
						t.Fatal("恢复回写形成弹框循环", ack, rest)
					}
				}
			})
		}
	}
}

func TestTagTournamentManualRosterRequestScopeAndValidation(t *testing.T) {
	for _, scenario := range []string{"多余正文", "未就绪", "延后就绪", "战斗中", "旧会话", "无名单", "其他频道", "旧配套", "错误分类", "过期角色"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, g := newTagRosterBridgeTest(t)
			var body []byte
			class := byte(1)
			switch scenario {
			case "多余正文":
				body = []byte{0}
			case "未就绪":
				p.sceneBootstrapTailSent = false
			case "延后就绪":
				p.sceneBootstrapTailDeferred = true
			case "战斗中":
				p.dungeon.runtime = &runtimeDungeonState{}
			case "旧会话":
				s.gameSessions[29] = &gameSession{}
			case "无名单":
				p.currentRenameRoster = nil
			case "其他频道":
				p.channel.Type = 1
			case "旧配套":
				s.options.currentClientVersion = "legacy"
			case "错误分类":
				class = 0
			case "过期角色":
				ch, _, _ := g.Character.Load(context.Background(), "60")
				ch.Name = "已改名"
				if err := g.Character.Save(context.Background(), ch); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := g.Account.Load(context.Background(), "a")
			handled, err := currentGameplayModules.DispatchUpper(s, p, 1467, class, body)
			if !handled || err != nil {
				t.Fatal(handled, err)
			}
			after, _, _ := g.Account.Load(context.Background(), "a")
			if p.conn.(*bufferConn).write.Len() != 0 || !reflect.DeepEqual(before, after) {
				t.Fatal("非法或越界请求弹框或修改账号")
			}
		})
	}
}

func TestTagTournamentRosterLookupUsesNative1467AndPreservesAdventure1473(t *testing.T) {
	if currentTagTournamentRosterListOpcode != 1467 || currentAdventureRepresentCharactersMsgID != 1473 {
		t.Fatal("混用了旧枚举名与现用协议")
	}
	for _, routes := range []map[uint16]registeredGameplayHandler{currentGameplayModules.legacyHandlers, currentGameplayModules.upperHandlers} {
		if routes[1467].module != "tag-tournament" || routes[1473].module != "adventure-info" {
			t.Fatal("帝国名单与冒险团角色查询路由串用")
		}
	}
}
