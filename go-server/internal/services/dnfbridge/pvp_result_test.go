package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func TestPVPDeathResultLifecycle(t *testing.T) {
	for _, route := range []string{"legacy", "upper"} {
		for _, winner := range []byte{0, 1} {
			t.Run(route+string(rune('0'+winner)), func(t *testing.T) {
				s := pvpChannelInfoTestService(t)
				queue := newFakeCurrentDungeonDeathTimerQueue()
				s.gameplayTimers = queue
				room := &currentPVPRoom{id: 1, channelID: 501, battleMode: 2, started: true,
					match: &currentPVPMatch{winner: 0xff}, seatState: [8]byte{1, 2, 3}}
				s.pvpRooms = map[uint16]*currentPVPRoom{1: room}
				s.pvpSubscribers = make(map[*gameSession]int)
				var players [4]*gameSession
				for seat := range players {
					player := &gameSession{conn: &bufferConn{}, selectedCharacterID: uint16(16 + seat*30), channel: s.catalog.Channels()[1]}
					player.channel.ID = 501
					players[seat] = player
					s.pvpSubscribers[player] = 501
					if seat < 3 {
						player.pvpRoom = room
						room.members[seat] = &currentPVPMember{session: player, characterID: player.selectedCharacterID,
							grade: byte(3 + seat), currencySlot2: uint32(123 + seat), rankPoint: 987}
					}
				}
				dispatch := func(player *gameSession, opcode uint16, body []byte) {
					t.Helper()
					var err error
					if route == "legacy" {
						err = s.handleGameCommand(player, 1, opcode, body)
					} else {
						wire, e := dnfproto.BuildChannelPacket(opcode, body, 0, 1)
						if e != nil {
							t.Fatal(e)
						}
						err = s.handleGameUpper(player, wire)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				loser := byte(1 - winner)
				body := []byte{byte(players[loser].selectedCharacterID), 0, byte(players[winner].selectedCharacterID), 0, 1, 100, 0}
				dispatch(players[winner], 55, body) // cannot report another actor's death
				dispatch(players[3], 55, body)
				dispatch(players[loser], 55, body[:6])
				invalid := append([]byte(nil), body...)
				invalid[2] = 99
				dispatch(players[loser], 55, invalid)
				dispatch(players[loser], 57, nil)
				for _, player := range players {
					if player.conn.(*bufferConn).write.Len() != 0 {
						t.Fatal("invalid report emitted packets")
					}
				}
				dispatch(players[loser], 55, body)
				if room.match.winner != winner || !room.match.resultSent || queue.scheduleCount != 1 {
					t.Fatalf("missing terminal result: %+v", room.match)
				}
				for seat, player := range players[:3] {
					conn := player.conn.(*bufferConn)
					death, rest := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
					if death.Header.Classification != 0 || death.Header.MsgID != 46 || !bytes.Equal(death.Body, []byte{loser, winner}) {
						t.Fatalf("death=%+v body=%x", death.Header, death.Body)
					}
					result, rest := splitCurrentGameServerUpperPacketAuto(t, rest)
					b := result.Body
					if result.Header.Classification != 0 || result.Header.MsgID != 47 || len(rest) != 0 || len(b) != 72 ||
						b[0] != winner || binary.LittleEndian.Uint32(b[1:]) != uint32(123+seat) || b[5] != byte(3+seat) || b[10] != 2 {
						t.Fatalf("current native result=%+v body=%x", result.Header, b)
					}
					for row := 0; row < 2; row++ {
						offset := 11 + 23*row
						kills := uint32(0)
						if byte(row) == winner {
							kills = 1
						}
						if binary.LittleEndian.Uint16(b[offset:]) != players[row].selectedCharacterID ||
							binary.LittleEndian.Uint32(b[offset+2:]) != kills || b[offset+6] != byte(3+row) ||
							binary.LittleEndian.Uint32(b[offset+11:]) != 987 || binary.LittleEndian.Uint32(b[offset+19:]) != kills {
							t.Fatalf("result row %d=%x", row, b[offset:offset+23])
						}
					}
					if !bytes.Equal(b[57:], []byte{0, 0, 255, 255, 0, 0, 0, 0, 0, 255, 0, 0, 0, 0, 0}) {
						t.Fatalf("native result tail=%x", b[57:])
					}
					conn.write.Reset()
				}
				if players[3].conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("lobby got match packets")
				}
				dispatch(players[loser], 55, body)
				for _, player := range players {
					if player.conn.(*bufferConn).write.Len() != 0 {
						t.Fatal("duplicate death settled twice")
					}
				}
				staleTimer := queue.tasks[0].callback
				dispatch(players[0], 57, nil)
				dispatch(players[1], 57, nil)
				if !room.started {
					t.Fatal("returned before observer closed result")
				}
				dispatch(players[2], 57, nil)
				if room.started || room.match != nil || room.members[1] == nil {
					t.Fatal("result did not retain waiting room")
				}
				for _, player := range players[:3] {
					wire := player.conn.(*bufferConn).write.Bytes()
					for _, opcode := range []uint16{44, 44, 44, 42} {
						packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
						if packet.Header.MsgID != opcode {
							t.Fatalf("return packet=%+v", packet.Header)
						}
						wire = rest
					}
					if len(wire) != 0 {
						t.Fatal("unexpected return packets")
					}
				}
				room.started = true
				room.match = &currentPVPMatch{winner: 0xff}
				staleTimer(time.Now())
				if !room.started {
					t.Fatal("old timeout ended a new match")
				}
				room.seatState[2] = room.seatState[loser]
				for _, player := range players {
					player.conn.(*bufferConn).write.Reset()
				}
				dispatch(players[loser], 55, body)
				if room.match.resultSent {
					t.Fatal("team still has a surviving member")
				}
				body[0] = byte(players[2].selectedCharacterID)
				dispatch(players[2], 55, body)
				if !room.match.resultSent {
					t.Fatal("last team death did not settle")
				}
				queue.tasks[len(queue.tasks)-1].callback(time.Now())
				if room.started {
					t.Fatal("missing result ACK left room stuck")
				}
			})
		}
	}
}
