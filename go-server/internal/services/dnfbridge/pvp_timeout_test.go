package dnfbridge

import (
	"encoding/binary"
	"os"
	"testing"
	"time"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestPVPTimeoutUsesTakenDamageAndPVFTime(t *testing.T) {
	source := bridgePVFSource{"etc/pvp_ref.etc": "[pvp normal battle time]\n180000\n[pvp relay battle time]\n120000\n" +
		"[pvp normal battle time for tournament]\n240000\n[pvp relay battle time for tournament]\n180000\n"}
	for _, mode := range []byte{1, 2, 3, 5} {
		got, err := currentPVPMinimumMatchDuration(source, mode, 0)
		want := 180 * time.Second
		if mode == 3 || mode == 5 {
			want = 120 * time.Second
		}
		if err != nil || got != want {
			t.Fatalf("mode %d duration=%s err=%v", mode, got, err)
		}
		got, err = currentPVPMinimumMatchDuration(source, mode, 24)
		if err != nil || got != want+time.Minute {
			t.Fatalf("type24 mode %d duration=%s err=%v", mode, got, err)
		}
	}
	if _, err := currentPVPMinimumMatchDuration(bridgePVFSource{"etc/pvp_ref.etc": "[pvp normal battle time]\n0"}, 2, 0); err == nil {
		t.Fatal("accepted missing match duration")
	}
	room := &currentPVPRoom{battleMode: 2, seatState: [8]byte{1, 2, 3}}
	for i := 0; i < 3; i++ {
		room.members[i] = &currentPVPMember{}
	}
	// Real timeout report: seat 0 lost about 60.7%, seat 1 about 2.8%.
	if winner := currentPVPTimeoutWinner(room, [8]uint32{60703, 2782}); winner != 1 {
		t.Fatalf("winner=%d", winner)
	}
	if winner := currentPVPTimeoutWinner(room, [8]uint32{20000, 20000}); winner != 255 {
		t.Fatalf("draw=%d", winner)
	}
	room.seatState[2] = 1
	if winner := currentPVPTimeoutWinner(room, [8]uint32{60000, 20000, 0}); winner != 1 {
		t.Fatalf("team result=%d", winner)
	}
	room.battleMode = 1
	if winner := currentPVPTimeoutWinner(room, [8]uint32{60000, 20000, 1000}); winner != 2 {
		t.Fatalf("FFA result=%d", winner)
	}
}

func TestRealPVFPVPTimeoutDispatch(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE for native timeout dispatch")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"legacy", "upper"} {
		for _, draw := range []bool{false, true} {
			s := pvpChannelInfoTestService(t)
			s.initialEquipmentArchive = archive
			queue := newFakeCurrentDungeonDeathTimerQueue()
			s.gameplayTimers = queue
			room := &currentPVPRoom{id: 1, channelID: 501, battleMode: 2, started: true,
				match: &currentPVPMatch{winner: 255, startedAt: queue.now}, seatState: [8]byte{1, 2, 3}}
			s.pvpRooms = map[uint16]*currentPVPRoom{1: room}
			var players [3]*gameSession
			for seat := range players {
				players[seat] = &gameSession{conn: &bufferConn{}, selectedCharacterID: uint16(16 + seat*30),
					channel: s.catalog.Channels()[1], pvpRoom: room}
				// 此处验证56/47的原生分派，没有账号或资产仓储；使用非排位房间。
				// 手动及排位结算另由带完整仓储的daily/result专项验证。
				players[seat].channel.Type = 8
				room.members[seat] = &currentPVPMember{session: players[seat], characterID: players[seat].selectedCharacterID}
			}
			dispatch := func(player *gameSession, body []byte) {
				t.Helper()
				var err error
				if route == "legacy" {
					err = s.handleGameCommand(player, 1, 56, body)
				} else {
					wire, buildErr := dnfproto.BuildChannelPacket(56, body, 0, 1)
					if buildErr != nil {
						t.Fatal(buildErr)
					}
					err = s.handleGameUpper(player, wire)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			body := make([]byte, 32)
			binary.LittleEndian.PutUint32(body, 60703)
			binary.LittleEndian.PutUint32(body[4:], 2782)
			reporter, winner := 0, byte(1)
			if draw {
				binary.LittleEndian.PutUint32(body, 2782)
				reporter, winner = 1, 255
			}
			queue.now = queue.now.Add(239 * time.Second)
			dispatch(players[reporter], body)
			queue.now = queue.now.Add(time.Second)
			dispatch(players[1-reporter], body)
			dispatch(players[2], body)
			dispatch(players[reporter], body[:31])
			invalid := append([]byte(nil), body...)
			invalid[31] = 1
			dispatch(players[reporter], invalid)
			invalid = append([]byte(nil), body...)
			invalid[3] = 128
			dispatch(players[reporter], invalid)
			delete(s.pvpRooms, 1)
			dispatch(players[reporter], body)
			s.pvpRooms[1] = room
			for _, player := range players {
				if player.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("invalid timeout emitted a result")
				}
			}
			dispatch(players[reporter], body)
			if !room.match.resultSent || room.match.winner != winner || queue.scheduleCount != 1 {
				t.Fatalf("%s timeout result=%+v", route, room.match)
			}
			for _, player := range players {
				conn := player.conn.(*bufferConn)
				packet, rest := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
				if packet.Header.MsgID != 47 || packet.Body[0] != winner || len(rest) != 0 ||
					len(packet.Body) != 72 || (packet.Body[65] == 1) != draw {
					t.Fatalf("timeout packet=%+v %x", packet.Header, packet.Body)
				}
				conn.write.Reset()
			}
			dispatch(players[reporter], body)
			room.match = &currentPVPMatch{winner: 255, startedAt: queue.now}
			dispatch(players[reporter], body)
			for _, player := range players {
				if player.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("duplicate or old-match report emitted a result")
				}
			}
		}
	}
}
