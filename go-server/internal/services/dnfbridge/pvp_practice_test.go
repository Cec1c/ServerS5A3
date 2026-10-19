package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func TestCurrentPVPPracticeNativeLifecycle(t *testing.T) {
	for _, route := range []string{"legacy", "upper"} {
		for _, battleMode := range []byte{6, 10} {
			t.Run(route+strconv.Itoa(int(battleMode)), func(t *testing.T) {
				s := pvpRoomTestService(t)
				a := &gameSession{conn: &bufferConn{}, accountID: "pvp-29", selectedCharacterID: 29, channel: s.catalog.Channels()[1],
					townPositionSnapshot: currentTownPositionSnapshot{CharacterID: 29, TownID: 10, AreaID: 0, PositionX: 400, PositionY: 220, PositionValid: true}}
				a.channel.Type = 8
				if battleMode == 10 {
					a.channel.Type = 1
					a.townPositionSnapshot.AreaID = 4
				}
				b := &gameSession{conn: &bufferConn{}, accountID: "pvp-30", selectedCharacterID: 30, channel: a.channel,
					townPositionSnapshot: currentTownPositionSnapshot{CharacterID: 30, TownID: 10, AreaID: 0, PositionX: 400, PositionY: 220, PositionValid: true}}
				if err := s.sendCurrentPVPDirectory(b); err != nil {
					t.Fatal(err)
				}
				dispatch := func(op uint16, body []byte) error {
					if route == "legacy" {
						return s.handleGameCommand(a, byte(dnfenum.GameCmdCommand), op, body)
					}
					packet, err := dnfproto.BuildChannelPacket(op, body, 0, dnfproto.DefaultChannelClassification)
					if err != nil {
						return err
					}
					return s.handleGameUpper(a, packet)
				}
				// Captured native practice request, including its trailing zero.
				body := []byte{0, 12, 0, 0, 0, 0xbe, 0xf6, 0xb6, 0xb7, 0xb3, 0xa1, 0xc1, 0xb7, 0xcf, 0xb0, 0xd6, 0xd0, 0, 0, 0, 1, 0}
				for _, special := range []byte{0, 2, 3, 255} {
					if battleMode == 10 && special == 0 {
						continue
					}
					invalid := append([]byte(nil), body...)
					invalid[len(invalid)-2] = special
					if err := dispatch(50, invalid); err != nil {
						t.Fatal(err)
					}
					packet, _ := splitCurrentGameServerUpperPacketAuto(t, a.conn.(*bufferConn).write.Bytes())
					if a.pvpRoom != nil || !bytes.Equal(packet.Body, []byte{0, 19}) {
						t.Fatal("unsupported mode coerced to practice")
					}
					a.conn.(*bufferConn).write.Reset()
				}
				if battleMode == 10 {
					body[len(body)-2], body[len(body)-1] = 3, 1
				}
				s.pvpMatching = []*currentPVPMatchingRequest{{member: &currentPVPMember{session: a}}}
				if err := dispatch(50, body); err != nil {
					t.Fatal(err)
				}
				if a.pvpRoom != nil || len(s.pvpMatching) != 1 {
					t.Fatal("queued matching entered practice")
				}
				s.pvpMatching = nil
				a.conn.(*bufferConn).write.Reset()
				if err := dispatch(50, body); err != nil {
					t.Fatal(err)
				}
				room := a.pvpRoom
				if room == nil || room.battleMode != battleMode || room.started || room.match != nil {
					t.Fatalf("practice=%+v", room)
				}
				wire := a.conn.(*bufferConn).write.Bytes()
				for _, op := range []uint16{uint16(dnfenum.CmdPacketSetUDPIPPort), 41, 23, uint16(dnfenum.CmdPacketNotifyUserState), currentActorUserStateMsgID} {
					packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
					if packet.Header.MsgID != op || packet.Header.Classification != 0 {
						t.Fatalf("entry=%+v", packet)
					}
					if op == currentActorUserStateMsgID && !bytes.Equal(packet.Body, []byte{29, 0, 3}) {
						t.Fatalf("practice avatar visibility=%x", packet.Body)
					}
					wire = rest
				}
				if len(wire) != 0 || b.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("practice leaked to directory or started a ranked loader")
				}
				if err := s.sendCurrentPVPDirectory(b); err != nil {
					t.Fatal(err)
				}
				if b.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("practice in snapshot")
				}
				if err := s.handleEnterPVPRoom(b, []byte{byte(room.id), byte(room.id >> 8), 0}); err != nil {
					t.Fatal(err)
				}
				if b.pvpRoom != nil {
					t.Fatal("practice accepted peer")
				}
				if battleMode == 6 {
					packet, _ := splitCurrentGameServerUpperPacketAuto(t, b.conn.(*bufferConn).write.Bytes())
					if !bytes.Equal(packet.Body, []byte{0, 19}) {
						t.Fatal("practice peer rejection missing")
					}
				}
				b.conn.(*bufferConn).write.Reset()
				if err := dispatch(54, []byte{2}); err != nil {
					t.Fatal(err)
				}
				if room.battleMode != battleMode {
					t.Fatal("practice became a rewarded mode")
				}
				repos, _ := s.repositoryGroup()
				before, _, _ := repos.Character.Load(context.Background(), "29")
				room.match = &currentPVPMatch{startedAt: time.Now(), winner: 0}
				if err := s.settleCurrentPVPDaily(room); err != nil {
					t.Fatal(err)
				}
				after, _, _ := repos.Character.Load(context.Background(), "29")
				if !reflect.DeepEqual(before.Stats, after.Stats) {
					t.Fatal("practice rewarded")
				}
				room.match = nil
				a.conn.(*bufferConn).write.Reset()
				if err := dispatch(52, []byte{0, 254}); err != nil {
					t.Fatal(err)
				}
				wire = a.conn.(*bufferConn).write.Bytes()
				for _, op := range []uint16{23, 24, uint16(dnfenum.CmdPacketNotifyUserState), 43, 42} {
					packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
					if packet.Header.MsgID != op {
						t.Fatalf("exit=%+v", packet)
					}
					wire = rest
				}
				if len(wire) != 0 || a.pvpRoom != nil || len(s.pvpRooms) != 0 || b.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("practice exit retained membership or leaked")
				}
			})
		}
	}
}
