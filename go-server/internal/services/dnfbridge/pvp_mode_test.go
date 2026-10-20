package dnfbridge

import (
	"bytes"
	"fmt"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func TestPVPModeSwitchSynchronizesWholeRoom(t *testing.T) {
	for _, route := range []string{"legacy", "upper"} {
		t.Run(route, func(t *testing.T) {
			s := pvpChannelInfoTestService(t)
			owner := &gameSession{conn: &bufferConn{}, selectedCharacterID: 29, channel: s.catalog.Channels()[1]}
			seedPVPRoomTestSession(s, owner)
			room := owner.pvpRoom
			players := []*gameSession{owner}
			for seat := 1; seat < 4; seat++ {
				peer := &gameSession{conn: &bufferConn{}, selectedCharacterID: uint16(29 + seat), channel: owner.channel, pvpRoom: room}
				room.members[seat] = &currentPVPMember{session: peer, characterID: peer.selectedCharacterID}
				s.pvpSubscribers[peer] = room.channelID
				players = append(players, peer)
			}
			lobby := &gameSession{conn: &bufferConn{}, channel: owner.channel}
			s.pvpSubscribers[lobby] = room.channelID
			room.seatState = [8]byte{1, 2, 1, 3}
			dispatch := func(mode byte) {
				t.Helper()
				var err error
				if route == "legacy" {
					err = s.handleGameCommand(owner, 1, uint16(dnfenum.CmdPacketSetPVPTeamMode), []byte{mode})
				} else {
					wire, e := dnfproto.BuildChannelPacket(uint16(dnfenum.CmdPacketSetPVPTeamMode), []byte{mode}, 0, 1)
					if e != nil {
						t.Fatal(e)
					}
					err = s.handleGameUpper(owner, wire)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			// Return from personal to teams, relay, total match and back. The
			// native room's team predicate reads seats, not just battleMode.
			for _, step := range []struct {
				mode   byte
				states [4]byte
			}{
				{1, [4]byte{0, 0, 0, 4}},
				{2, [4]byte{1, 2, 1, 3}},
				{3, [4]byte{1, 2, 1, 3}},
				{8, [4]byte{1, 2, 1, 3}},
				{1, [4]byte{0, 0, 0, 4}},
			} {
				room.ready[1], room.ready[2] = 1, 1
				dispatch(step.mode)
				if room.battleMode != step.mode || room.ready != [8]byte{} {
					t.Fatalf("mode=%d ready=%v after request %d", room.battleMode, room.ready, step.mode)
				}
				want := []byte{1, 0, step.mode, 4}
				for seat, state := range step.states {
					if room.seatState[seat] != state {
						t.Fatalf("mode %d seat %d=%d want %d", step.mode, seat, room.seatState[seat], state)
					}
					want = append(want, byte(seat), state, byte(29+seat), 0)
				}
				for _, target := range append(append([]*gameSession(nil), players...), lobby) {
					conn := target.conn.(*bufferConn)
					packet, rest := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
					if packet.Header.MsgID != 43 || packet.Header.Classification != 0 || !bytes.Equal(packet.Body, want) {
						t.Fatalf("mode %d did not publish full roster: %+v %x want %x", step.mode, packet.Header, packet.Body, want)
					}
					if target != lobby {
						for _, seat := range []byte{1, 2} {
							packet, rest = splitCurrentGameServerUpperPacketAuto(t, rest)
							if packet.Header.MsgID != 44 || !bytes.Equal(packet.Body, []byte{seat, 0}) {
								t.Fatalf("ready reset=%+v %x", packet.Header, packet.Body)
							}
						}
					}
					if len(rest) != 0 {
						t.Fatalf("unexpected mode tail %x", rest)
					}
					conn.write.Reset()
				}
			}
			// Repeating a selection must not cancel another player's new ready.
			room.ready[1] = 1
			dispatch(1)
			if room.ready[1] != 1 || owner.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("duplicate selection reset ready state")
			}
		})
	}
}

func TestPVPModeSwitchRestrictions(t *testing.T) {
	for _, name := range []string{"non-owner", "started", "matching", "practice", "dojo", "invalid-mode", "trailing-body"} {
		t.Run(name, func(t *testing.T) {
			s := pvpChannelInfoTestService(t)
			owner := &gameSession{conn: &bufferConn{}, selectedCharacterID: 29, channel: s.catalog.Channels()[1]}
			seedPVPRoomTestSession(s, owner)
			room := owner.pvpRoom
			caller, body := owner, []byte{8}
			switch name {
			case "non-owner":
				caller = &gameSession{conn: &bufferConn{}, channel: owner.channel, pvpRoom: room}
			case "started":
				room.started = true
			case "matching":
				room.matchingToken = 1
			case "practice":
				room.battleMode = 6
			case "dojo":
				room.battleMode = 10
			case "invalid-mode":
				body = []byte{7}
			case "trailing-body":
				body = []byte{8, 0}
			}
			before := room.battleMode
			if err := s.handleSetPVPTeamMode(caller, body, "test"); err != nil {
				t.Fatal(err)
			}
			if room.battleMode != before || owner.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("restricted switch changed room")
			}
		})
	}
	if normalizeCurrentPVPBattleMode(8) != 8 {
		t.Fatal("native total-match mode fell back to teams")
	}
}

func TestPVPModeJoinAndSeatChanges(t *testing.T) {
	for _, mode := range []byte{1, 2, 3, 4, 5, 8} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			s := pvpRoomTestService(t)
			newSession := func(id uint16) *gameSession {
				peer := &gameSession{conn: &bufferConn{}, selectedCharacterID: id, accountID: "pvp-" + strconv.Itoa(int(id)), channel: s.catalog.Channels()[1], sceneBootstrapTailSent: true}
				peer.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: id, TownID: 10, PositionX: 474, PositionY: 234, MovementCode: 5, PositionValid: true}
				peer.partyPeer = currentPartyPeerEndpointRegistration{InnerIPv4: [4]byte{127, 0, 0, 1}, OuterIPv4: [4]byte{127, 0, 0, 1}, Port: 12000 + id, MTU: currentPartyPeerMinimumMTU}
				return peer
			}
			owner, peer := newSession(29), newSession(30)
			if err := s.handleMakePVPRoom(owner, []byte{5, 0, 0, 0, 0, 0}, "test"); err != nil {
				t.Fatal(err)
			}
			if err := s.handleSetPVPTeamMode(owner, []byte{mode}, "test"); err != nil {
				t.Fatal(err)
			}
			room := owner.pvpRoom
			if err := s.handleEnterPVPRoom(peer, []byte{byte(room.id), byte(room.id >> 8), 0}); err != nil {
				t.Fatal(err)
			}
			personal := mode == 1 || mode == 4
			if peer.pvpRoom != room || (personal && room.seatState[1] != 0) || (!personal && room.seatState[1] != 2) {
				t.Fatalf("join mode %d seats=%v", mode, room.seatState)
			}
			for _, requested := range []byte{3, 4, 1, 2, 0} {
				owner.conn.(*bufferConn).write.Reset()
				peer.conn.(*bufferConn).write.Reset()
				room.ready[1] = 1
				if err := s.handleSetPVPSeatState(peer, []byte{1, requested}, "test"); err != nil {
					t.Fatal(err)
				}
				state := room.seatState[1]
				if personal && ((requested >= 3 && state != 4) || (requested < 3 && state != 0)) {
					t.Fatalf("personal seat request %d became %d", requested, state)
				}
				if !personal && ((requested >= 3 && state != 3) || (requested < 3 && state != 1 && state != 2)) {
					t.Fatalf("team seat request %d became %d", requested, state)
				}
				if room.ready[1] != 0 {
					t.Fatal("seat change retained ready state")
				}
			}
		})
	}
}

func TestPVPTotalMatchDoesNotStartWithoutNativeRoster(t *testing.T) {
	s := pvpChannelInfoTestService(t)
	owner := &gameSession{conn: &bufferConn{}, selectedCharacterID: 29, channel: s.catalog.Channels()[1]}
	seedPVPRoomTestSession(s, owner)
	room := owner.pvpRoom
	room.battleMode = 8
	peer := &gameSession{conn: &bufferConn{}, channel: owner.channel, pvpRoom: room}
	room.members[1] = &currentPVPMember{session: peer, characterID: 30}
	room.seatState[1], room.ready[1] = 2, 1
	s.pvpMapOrder = []int64{50000, 50042}
	if err := s.handleSetPVPReadyState(owner, []byte{1}); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitCurrentGameServerUpperPacketAuto(t, owner.conn.(*bufferConn).write.Bytes())
	if room.started || room.match != nil || len(rest) != 0 || packet.Header.Classification != 1 || packet.Header.MsgID != 53 || !bytes.Equal(packet.Body, []byte{0, 19}) {
		t.Fatalf("total match used ordinary match start: %+v %x", packet.Header, packet.Body)
	}
}
