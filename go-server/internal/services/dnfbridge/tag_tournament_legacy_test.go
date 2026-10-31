package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func tagRosterLegacyFrame(class byte, sequence uint16, body []byte) []byte {
	frame := make([]byte, dnfproto.LegacyGameHeaderSize+len(body))
	frame[0] = class
	binary.LittleEndian.PutUint16(frame[1:3], 1457)
	binary.LittleEndian.PutUint32(frame[3:7], uint32(len(frame)))
	binary.LittleEndian.PutUint16(frame[11:13], sequence)
	copy(frame[dnfproto.LegacyGameHeaderSize:], body)
	return frame
}

func dispatchTagRosterObservedStream(t *testing.T, s *Service, p *gameSession, class byte, body []byte) {
	t.Helper()
	frame := tagRosterLegacyFrame(class, 34, body)
	packets, rest, skipped, err := dnfproto.SplitLatestGameStream(frame, 65535)
	if err != nil || len(packets) != 1 || len(rest) != 0 || skipped != 0 || packets[0].Kind != dnfproto.LatestGameStreamLegacy {
		t.Fatal("13字节头1457未按现场legacy路径拆流", packets, rest, skipped, err)
	}
	if err = s.handleGameStreamPacket(p, packets[0]); err != nil {
		t.Fatal(err)
	}
}

func TestTagTournamentObservedLegacyStreamPersistsAndRestoresTeam(t *testing.T) {
	// 现场14:53/14:54两次正文为00000001、00010004；隔离账号用同一紧凑序号重放。
	for _, transport := range []string{"完整字节流", "legacy命令", "upper命令"} {
		t.Run(transport, func(t *testing.T) {
			s, p, g := newTagRosterBridgeTest(t)
			ctx := context.Background()
			original := p.currentRenameRoster
			rows := []repo.CharacterRecord{original[0], original[2]}
			for _, id := range []string{"101", "102", "103"} {
				ch := repo.CharacterRecord{CharacterID: id, AccountID: "a", Slot: 30 + len(rows), Name: "合成队员" + id, Job: "0", Level: 90, Stats: map[string]int64{"gold": 1000000}}
				if err := g.Character.Save(ctx, ch); err != nil {
					t.Fatal(err)
				}
				rows = append(rows, ch)
			}
			p.currentRenameRoster = rows
			for _, body := range [][]byte{{0, 0, 0, 1}, {0, 1, 0, 4}, {0, 1, 0, 4}} {
				p.conn.(*bufferConn).write.Reset()
				switch transport {
				case "完整字节流":
					dispatchTagRosterObservedStream(t, s, p, 1, body)
				case "legacy命令":
					if err := s.handleGameCommand(p, 1, 1457, body); err != nil {
						t.Fatal(err)
					}
				case "upper命令":
					handled, err := currentGameplayModules.DispatchUpper(s, p, 1457, 1, body)
					if !handled || err != nil {
						t.Fatal(handled, err)
					}
				}
				written := p.conn.(*bufferConn).write.Bytes()
				if len(written) == 0 {
					t.Fatal("实际保存入口没有回包或落库")
				}
				ack, rest := splitCurrentGameServerUpperPacketAuto(t, written)
				if ack.Header.MsgID != 1457 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, upperSuccessBody(body)) || len(rest) != 0 {
					t.Fatal("保存未成功或自动1388形成回写循环", ack, rest)
				}
				// 新服务对象与新连接从仓储恢复，避免只在本会话里看起来选好了。
				reopened := &Service{repositoryProvider: s.repositoryProvider, options: s.options, tagTournamentCatalog: s.tagTournamentCatalog}
				owner, err := reopened.currentTagTournamentOwner()
				if err != nil {
					t.Fatal(err)
				}
				view, err := owner.Lobby(ctx, "a")
				if err != nil || !view.Registered || view.Day.Paid || view.Characters[0].CharacterID != rows[body[1]].CharacterID || view.Characters[1].CharacterID != rows[body[3]].CharacterID {
					t.Fatal("保存没有持久化真实角色", view, err)
				}
				fresh := &gameSession{accountID: p.accountID, selectedCharacterID: p.selectedCharacterID, loginPacketSeen: true, channel: p.channel, currentRenameRoster: rows, conn: &bufferConn{}}
				if err = reopened.sendCurrentTagTournamentLobby(fresh); err != nil {
					t.Fatal(err)
				}
				team, rest := splitCurrentGameServerUpperPacketAuto(t, tagDailyPacketsRest(t, fresh.conn.(*bufferConn).write.Bytes()))
				if team.Header.MsgID != 1388 || !bytes.Equal(team.Body, body) || len(rest) != 0 {
					t.Fatal("重入未静默恢复队伍", team, rest)
				}
			}
			for _, ch := range rows {
				actual, _, _ := g.Character.Load(ctx, ch.CharacterID)
				box, _, _ := g.Mailbox.Load(ctx, ch.CharacterID)
				if !reflect.DeepEqual(actual, ch) || len(box.Mails) != 0 {
					t.Fatal("保存改变了角色资产或发奖")
				}
			}
		})
	}
}

func TestTagTournamentLegacyIngressKeepsValidationAndScope(t *testing.T) {
	for _, scenario := range []string{"截断", "尾随", "外区服", "越界序号", "重复角色", "低等级", "跨账号", "未就绪", "旧会话", "其他频道", "旧配套", "错误分类"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, g := newTagRosterBridgeTest(t)
			body, class := []byte{0, 2, 0, 0}, byte(1)
			switch scenario {
			case "截断":
				body = body[:3]
			case "尾随":
				body = append(body, 0)
			case "外区服":
				body[2] = 1
			case "越界序号":
				body[3] = 255
			case "重复角色":
				body[3] = 2
			case "低等级":
				body[3] = 1
			case "跨账号":
				p.currentRenameRoster[0].AccountID = "其他账号"
			case "未就绪":
				p.sceneBootstrapTailSent = false
			case "旧会话":
				s.gameSessions[29] = &gameSession{}
			case "其他频道":
				p.channel.Type = 1
			case "旧配套":
				s.options.currentClientVersion = "legacy"
			case "错误分类":
				class = 0
			}
			before, _, _ := g.Account.Load(context.Background(), "a")
			dispatchTagRosterObservedStream(t, s, p, class, body)
			after, _, _ := g.Account.Load(context.Background(), "a")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("非法请求修改了账号")
			}
			written := p.conn.(*bufferConn).write.Bytes()
			if scenario == "其他频道" || scenario == "旧配套" || scenario == "错误分类" {
				if len(written) != 0 {
					t.Fatal("越界入口仍有回包", written)
				}
				return
			}
			if len(written) == 0 {
				t.Fatal("实际legacy路径未执行拒绝校验")
			}
			ack, rest := splitCurrentGameServerUpperPacketAuto(t, written)
			if !bytes.Equal(ack.Body, []byte{0, 3}) || len(rest) != 0 {
				t.Fatal(ack, rest)
			}
		})
	}
}
