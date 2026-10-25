package dnfbridge

import (
	"bytes"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func TestPVPHostReadyStartsNativeMatch(t *testing.T) {
	for _, route := range []string{"legacy", "upper"} {
		t.Run(route, func(t *testing.T) {
			s := pvpChannelInfoTestService(t)
			owner := &gameSession{conn: &bufferConn{}, selectedCharacterID: 29, channel: s.catalog.Channels()[1]}
			peer := &gameSession{conn: &bufferConn{}, selectedCharacterID: 30, channel: owner.channel}
			viewer := &gameSession{conn: &bufferConn{}, selectedCharacterID: 31, channel: owner.channel}
			seedPVPRoomTestSession(s, owner)
			room := owner.pvpRoom
			room.members[1] = &currentPVPMember{session: peer, characterID: 30}
			room.seatState[1] = 1
			peer.pvpRoom = room
			s.pvpSubscribers[peer] = owner.channel.ID
			s.pvpSubscribers[viewer] = owner.channel.ID
			var err error
			s.pvpMapOrder, err = currentPVPStartMapOrder(bridgePVFSource{"etc/pvpmapbasicdata.etc": "[pvp map order]\n50000 50042 50041\n[/pvp map order]"})
			if err != nil {
				t.Fatal(err)
			}
			start := func() {
				t.Helper()
				if route == "legacy" {
					err = s.handleGameCommand(owner, 1, uint16(dnfenum.CmdPacketSetPVPReadyState), []byte{1})
				} else {
					wire, e := dnfproto.BuildChannelPacket(uint16(dnfenum.CmdPacketSetPVPReadyState), []byte{1}, 0, 1)
					if e != nil {
						t.Fatal(e)
					}
					err = s.handleGameUpper(owner, wire)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			failure := func(code byte) {
				t.Helper()
				start()
				conn := owner.conn.(*bufferConn)
				p, rest := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
				if room.started || len(rest) != 0 || p.Header.Classification != 1 || p.Header.MsgID != 53 || !bytes.Equal(p.Body, []byte{0, code}) {
					t.Fatalf("start rejection=%+v body=%x", p.Header, p.Body)
				}
				conn.write.Reset()
				if peer.conn.(*bufferConn).write.Len() != 0 || viewer.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("rejected start was published")
				}
			}
			failure(22)
			room.ready[1] = 1
			failure(14)
			room.seatState[1] = 2
			room.request.mapIndex = 3
			failure(19)
			room.request.mapIndex = -1
			failure(19)
			room.request.mapIndex = 2
			start()
			if !room.started {
				t.Fatal("owner ready did not start match")
			}
			for _, player := range []*gameSession{owner, peer} {
				conn := player.conn.(*bufferConn)
				wire := conn.write.Bytes()
				for i, id := range []uint16{44, 45, 42} {
					p, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
					if p.Header.Classification != 0 || p.Header.MsgID != id {
						t.Fatalf("packet %d=%+v", i, p.Header)
					}
					if id == 45 && !bytes.Equal(p.Body, []byte{2, 2}) {
						t.Fatalf("native start body=%x", p.Body)
					}
					if id == 42 && p.Body[2] != 2 {
						t.Fatalf("room is not started: %x", p.Body)
					}
					wire = rest
				}
				if len(wire) != 0 {
					t.Fatal("unexpected start tail")
				}
				conn.write.Reset()
			}
			p, rest := splitCurrentGameServerUpperPacketAuto(t, viewer.conn.(*bufferConn).write.Bytes())
			if p.Header.MsgID != 42 || len(rest) != 0 {
				t.Fatal("lobby received a start/loading packet")
			}
			viewer.conn.(*bufferConn).write.Reset()
			start()
			if owner.conn.(*bufferConn).write.Len() != 0 || peer.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("duplicate click restarted the match")
			}
			if err := s.handleSetPVPMapIndex(owner, []byte{1, 0}, "test"); err != nil {
				t.Fatal(err)
			}
			if room.request.mapIndex != 2 {
				t.Fatal("map changed during match")
			}
			for _, signal := range []struct {
				op   uint16
				body []byte
			}{{298, nil}, {299, []byte{2, 0, 1, 1, 1}}} {
				if route == "legacy" {
					err = s.handleGameCommand(owner, 1, signal.op, signal.body)
				} else {
					wire, e := dnfproto.BuildChannelPacket(signal.op, signal.body, 0, 1)
					if e != nil {
						t.Fatal(e)
					}
					err = s.handleGameUpper(owner, wire)
				}
				if err != nil || owner.conn.(*bufferConn).write.Len() != 0 {
					t.Fatalf("normal-match loading signal generated a spurious response: %v", err)
				}
			}
			s.closeCurrentPVPSession(owner)
			if owner.pvpRoom != nil || peer.pvpRoom != nil || len(s.pvpRooms) != 0 {
				t.Fatal("interrupted match retained participants")
			}
		})
	}
}

func TestPVPStartMapOrderRejectsMissingOrInvalidData(t *testing.T) {
	for _, text := range []string{"", "[pvp map order]\n0", "[pvp map order]\n50000", "[pvp map order]\n50042 50041", "[pvp map order]\n50000 50000"} {
		if _, err := currentPVPStartMapOrder(bridgePVFSource{"etc/pvpmapbasicdata.etc": text}); err == nil {
			t.Fatal("accepted invalid map order")
		}
	}
}

func TestPVPRandomStartExcludesRandomRowAndEndIndex(t *testing.T) {
	for i := 0; i < 32; i++ {
		s := pvpChannelInfoTestService(t)
		owner := &gameSession{conn: &bufferConn{}, selectedCharacterID: 29, channel: s.catalog.Channels()[1]}
		peer := &gameSession{conn: &bufferConn{}, selectedCharacterID: 30, channel: owner.channel}
		seedPVPRoomTestSession(s, owner)
		room := owner.pvpRoom
		room.members[1] = &currentPVPMember{session: peer, characterID: 30}
		room.seatState[1], room.ready[1] = 2, 1
		s.pvpMapOrder = []int64{50000, 50042}
		if err := s.startCurrentPVPRoomLocked(room, owner); err != nil {
			t.Fatal(err)
		}
		_, rest := splitCurrentGameServerUpperPacketAuto(t, owner.conn.(*bufferConn).write.Bytes())
		start, _ := splitCurrentGameServerUpperPacketAuto(t, rest)
		if start.Header.MsgID != 45 || !bytes.Equal(start.Body, []byte{1, 2}) || room.request.mapIndex != 0 {
			t.Fatalf("random start must resolve the only actual map and retain room choice: %x", start.Body)
		}
	}
}
