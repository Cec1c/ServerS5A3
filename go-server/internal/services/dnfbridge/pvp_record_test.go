package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestCurrentPVPRecordNativeReply(t *testing.T) {
	for _, route := range []string{"legacy", "upper"} {
		t.Run(route, func(t *testing.T) {
			s := pvpRoomTestService(t)
			session := &gameSession{conn: &bufferConn{}, accountID: "pvp-29", selectedCharacterID: 29, channel: s.catalog.Channels()[0]}
			repos, _ := s.repositoryGroup()
			character, _, _ := repos.Character.Load(context.Background(), "29")
			character.Stats[dnfpvp.WinsKey], character.Stats[dnfpvp.LossesKey], character.Stats[dnfpvp.DrawsKey] = 16, 7, 2
			character.Stats["pvp_win_streak"], character.Stats[dnfpvp.MaxWinStreakKey] = 2, 6
			character.Stats[dnfpvp.RecentResultsKey] = 1<<6 | 2<<4 | 3<<2 | 1
			character.Stats[dnfpvp.GradeKey] = 1
			if err := dnfrepo.SaveCharacterFields(context.Background(), repos.Character, character, dnfrepo.CharacterFieldStats); err != nil {
				t.Fatal(err)
			}
			for _, view := range []byte{5, 2, 3, 4, 5} {
				session.conn.(*bufferConn).write.Reset()
				body := []byte{29, 0, 1, view, 0, 0, 0, 0}
				var err error
				if route == "legacy" {
					err = s.handleGameCommand(session, byte(dnfenum.GameCmdCommand), 375, body)
				} else {
					request, buildErr := dnfproto.BuildChannelPacket(375, body, 0, dnfproto.DefaultChannelClassification)
					if buildErr != nil {
						t.Fatal(buildErr)
					}
					err = s.handleGameUpper(session, request)
				}
				if err != nil {
					t.Fatal(err)
				}
				packet, rest := splitCurrentGameServerUpperPacketAuto(t, session.conn.(*bufferConn).write.Bytes())
				if packet.Header.MsgID != 375 || packet.Header.Classification != 1 || len(rest) != 0 || len(packet.Body) != 63 || !bytes.Equal(packet.Body[:5], []byte{1, 29, 0, view, 1}) {
					t.Fatalf("reply=%+v", packet)
				}
				body = packet.Body[1:]
				if view == 5 {
					if body[4] != 1 || binary.LittleEndian.Uint32(body[6:]) != 16 || binary.LittleEndian.Uint32(body[33:]) != 7 || binary.LittleEndian.Uint32(body[37:]) != 2 ||
						!bytes.Equal(body[14:17], []byte{2, 1, 1}) || binary.LittleEndian.Uint32(body[17:]) != 2 || binary.LittleEndian.Uint32(body[29:]) != 6 {
						t.Fatalf("native record=%x", body)
					}
				} else if !bytes.Equal(body[4:], make([]byte, 58)) {
					t.Fatalf("invented old-season history=%x", body)
				}
			}
			for _, body := range [][]byte{{30, 0, 0, 5, 0, 0, 0, 0}, {29, 0, 0, 5, 0, 0, 0, 0}, {29, 0, 1, 6, 0, 0, 0, 0}, {29, 0, 1, 5, 3, 0, 0, 0}} {
				session.conn.(*bufferConn).write.Reset()
				if err := s.handleCurrentPVPRecord(session, body); err != nil {
					t.Fatal(err)
				}
				packet, _ := splitCurrentGameServerUpperPacketAuto(t, session.conn.(*bufferConn).write.Bytes())
				if !bytes.Equal(packet.Body, []byte{0, 1}) {
					t.Fatalf("invalid query accepted: %x", packet.Body)
				}
			}
			if err := s.handleCurrentPVPRecord(session, []byte{29, 0, 1, 5, 255, 255, 255, 255}); err != nil {
				t.Fatal(err)
			}
			peer := &gameSession{conn: &bufferConn{}, accountID: "pvp-30", selectedCharacterID: 30, channel: session.channel}
			s.bindGameSessionCharacter(session, 29)
			s.bindGameSessionCharacter(peer, 30)
			s.onlinePlayers = newOnlinePlayerManager()
			s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 29, TownID: 1, AreaID: 2, Session: session})
			s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 30, TownID: 1, AreaID: 2, Session: peer})
			session.conn.(*bufferConn).write.Reset()
			if err := s.handleCurrentPVPRecord(session, []byte{30, 0, 0, 5, 255, 255, 255, 255}); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, session.conn.(*bufferConn).write.Bytes())
			if len(rest) != 0 || len(packet.Body) != 63 || packet.Body[0] != 1 || binary.LittleEndian.Uint16(packet.Body[1:]) != 30 || binary.LittleEndian.Uint32(packet.Body[7:]) != 0 {
				t.Fatal("peer query did not use target record")
			}
		})
	}
}
