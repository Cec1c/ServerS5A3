package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func pvpRoomTestService(t *testing.T) *Service {
	t.Helper()
	s := pvpChannelInfoTestService(t)
	s.onlinePlayers = newOnlinePlayerManager()
	s.pvpDailyCatalog = pvpDailyTestCatalog()
	repos := dnfrepomemory.NewMemoryGroup()
	for id := 29; id <= 38; id++ {
		if err := repos.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: "pvp-" + strconv.Itoa(id)}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{
			CharacterID: strconv.Itoa(id), AccountID: "pvp-" + strconv.Itoa(id),
			Name: "player" + strconv.Itoa(id), Job: "0", Level: 90,
			Stats: map[string]int64{"grow_type": 1},
		}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Skill.Save(context.Background(), dnfrepo.SkillRecord{
			CharacterID: strconv.Itoa(id), Skills: map[int64]dnfrepo.SkillState{46: {Level: id - 20, Enabled: true}},
			Trees: dnfrepo.SkillTreeState{Other: dnfrepo.SkillProfile{Skills: map[int64]dnfrepo.SkillState{46: {Level: 1, Enabled: true}}}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	return s
}

func seedPVPRoomTestSession(service *Service, session *gameSession) {
	room := &currentPVPRoom{id: 1, channelID: session.channel.ID, battleMode: 2}
	room.request.roomNameType = 5
	room.members[0] = &currentPVPMember{session: session, characterID: session.selectedCharacterID, position: session.townPositionSnapshot}
	room.seatState[0] = 1
	session.pvpRoom = room
	service.pvpRooms = map[uint16]*currentPVPRoom{1: room}
	service.pvpSubscribers = map[*gameSession]int{session: session.channel.ID}
}

func TestParseCurrentPVPMakeRoomRequest(t *testing.T) {
	for _, body := range [][]byte{
		{5, 0, 0, 0, 0, 0},
		{12, 0, 0, 0, 0, 0},
	} {
		request, err := parseCurrentPVPMakeRoomRequest(body)
		if err != nil || request.roomNameType == 0 || request.mapIndex != 0 || request.hasPassword {
			t.Fatalf("default pvp room request %x parsed as %+v, err=%v", body, request, err)
		}
	}

	body := []byte{0, 3, 0, 0, 0, 'a', 'b', 'c', 2, 0, 1, 2, 0, 0, 0, 'x', 'y', 1}
	request, err := parseCurrentPVPMakeRoomRequest(body)
	if err != nil || !bytes.Equal(request.roomName, []byte("abc")) || request.mapIndex != 2 || !request.hasPassword || !bytes.Equal(request.password, []byte("xy")) || request.specialBattleMode != 1 {
		t.Fatalf("named pvp room request parsed as %+v, err=%v", request, err)
	}
}

func TestHandleMakePVPRoomSendsCurrentSequence(t *testing.T) {
	service := pvpRoomTestService(t)
	conn := &bufferConn{}
	session := &gameSession{
		conn:                conn,
		accountID:           "pvp-29",
		selectedCharacterID: 29,
		channel:             service.catalog.Channels()[1],
		townPositionSnapshot: currentTownPositionSnapshot{
			CharacterID:   29,
			TownID:        newCharacterInitialTownID,
			AreaID:        newCharacterInitialAreaID,
			PositionX:     newCharacterInitialPosX,
			PositionY:     newCharacterInitialPosY,
			MovementCode:  newCharacterInitialDirection,
			PositionValid: true,
		},
	}
	if err := service.handleMakePVPRoom(session, []byte{5, 0, 0, 0, 0, 0}, "test"); err != nil {
		t.Fatalf("handle make pvp room: %v", err)
	}

	stream := conn.write.Bytes()
	wantIDs := []uint16{
		uint16(dnfenum.CmdPacketSetUDPIPPort),
		currentPVPRoomInfoMsgID,
		currentPVPUserAreaMsgID,
		uint16(dnfenum.CmdPacketNotifyUserState),
		currentActorUserStateMsgID,
	}
	for index, wantID := range wantIDs {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
		if packet.Header.Classification != 0 || packet.Header.MsgID != wantID {
			t.Fatalf("packet %d = class=%d msg=%d, want msg=%d", index, packet.Header.Classification, packet.Header.MsgID, wantID)
		}
		if index == 1 {
			if len(packet.Body) != 39 || binary.LittleEndian.Uint16(packet.Body[:2]) != 1 || binary.LittleEndian.Uint16(packet.Body[2:4]) != currentPVPEntireRoomID {
				t.Fatalf("room info body=%x", packet.Body)
			}
			// sub_1D67AF0 consumes eight three-byte seats, then password + matching.
			if !bytes.Equal(packet.Body[10:13], []byte{1, 29, 0}) ||
				!bytes.Equal(packet.Body[13:34], bytes.Repeat([]byte{0xff}, 21)) ||
				!bytes.Equal(packet.Body[34:], make([]byte, 5)) {
				t.Fatalf("native seat/tail layout=%x", packet.Body[10:])
			}
		}
		if index == 3 && !bytes.Equal(packet.Body, []byte{1, 29, 0, currentPVPUserState}) {
			t.Fatalf("user state body=%x", packet.Body)
		}
		if index == 4 && !bytes.Equal(packet.Body, []byte{29, 0, 3}) {
			t.Fatalf("default avatar visibility body=%x", packet.Body)
		}
		stream = rest
	}
	if len(stream) != 0 {
		t.Fatalf("unexpected trailing pvp packets: %x", stream)
	}
}

func TestHandlePVPSeatStateExitSendsRoomTeardownSequence(t *testing.T) {
	for _, route := range []string{"legacy", "upper"} {
		t.Run(route, func(t *testing.T) {
			service := pvpChannelInfoTestService(t)
			conn := &bufferConn{}
			session := &gameSession{
				conn:                conn,
				selectedCharacterID: 29,
				channel:             service.catalog.Channels()[1],
				townPositionSnapshot: currentTownPositionSnapshot{
					CharacterID:   29,
					TownID:        newCharacterInitialTownID,
					AreaID:        newCharacterInitialAreaID,
					PositionX:     newCharacterInitialPosX,
					PositionY:     newCharacterInitialPosY,
					MovementCode:  newCharacterInitialDirection,
					PositionValid: true,
				},
			}
			seedPVPRoomTestSession(service, session)
			body := []byte{0, 0xFE}
			if route == "legacy" {
				if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketSetPVPSeatState), body); err != nil {
					t.Fatalf("handle legacy pvp exit: %v", err)
				}
			} else {
				request, err := dnfproto.BuildChannelPacket(uint16(dnfenum.CmdPacketSetPVPSeatState), body, 0, dnfproto.DefaultChannelClassification)
				if err != nil {
					t.Fatal(err)
				}
				if err := service.handleGameUpper(session, request); err != nil {
					t.Fatalf("handle upper pvp exit: %v", err)
				}
			}

			stream := conn.write.Bytes()
			wantIDs := []uint16{currentPVPUserAreaMsgID, currentSceneTransitionMsgID, uint16(dnfenum.CmdPacketNotifyUserState), currentPVPSeatStateMsgID, currentPVPRoomStateMsgID}
			for index, wantID := range wantIDs {
				packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
				if packet.Header.Classification != 0 || packet.Header.MsgID != wantID {
					t.Fatalf("packet %d = class=%d msg=%d, want msg=%d", index, packet.Header.Classification, packet.Header.MsgID, wantID)
				}
				switch index {
				case 0:
					if packet.Body[2] != newCharacterInitialTownID || packet.Body[3] != newCharacterInitialAreaID {
						t.Fatalf("town return area body=%x", packet.Body)
					}
				case 1:
					if len(packet.Body) != 13 || packet.Body[0] != newCharacterInitialTownID || packet.Body[1] != newCharacterInitialAreaID || binary.LittleEndian.Uint16(packet.Body[4:6]) != 29 {
						t.Fatalf("town scene transition body=%x", packet.Body)
					}
				case 2:
					if !bytes.Equal(packet.Body, []byte{1, 29, 0, 0}) {
						t.Fatalf("returned user state body=%x", packet.Body)
					}
				case 3:
					if !bytes.Equal(packet.Body, []byte{1, 0, 2, 1, 0, 0xff, 0xff, 0xff}) {
						t.Fatalf("native room pointer must be cleared before destruction: %x", packet.Body)
					}
				case 4:
					if !bytes.Equal(packet.Body, []byte{1, 0, 0, 0, 0, 0, 2, 0, 0, 0, 0}) {
						t.Fatalf("destroyed room body=%x", packet.Body)
					}
				}
				stream = rest
			}
			if len(stream) != 0 {
				t.Fatalf("unexpected trailing pvp exit packets: %x", stream)
			}
			if session.pvpRoom != nil || len(service.pvpRooms) != 0 {
				t.Fatal("PVP room state survived exit")
			}
		})
	}
}

func TestHandlePVPSeatStatePublishesSingleSeatForEnter(t *testing.T) {
	for _, route := range []string{"legacy", "upper"} {
		t.Run(route, func(t *testing.T) {
			service := pvpChannelInfoTestService(t)
			conn := &bufferConn{}
			session := &gameSession{
				conn:                conn,
				selectedCharacterID: 29,
				channel:             service.catalog.Channels()[1],
			}
			seedPVPRoomTestSession(service, session)
			body := []byte{0, 2}
			if route == "legacy" {
				if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketSetPVPSeatState), body); err != nil {
					t.Fatalf("handle legacy pvp seat state: %v", err)
				}
			} else {
				request, err := dnfproto.BuildChannelPacket(uint16(dnfenum.CmdPacketSetPVPSeatState), body, 0, dnfproto.DefaultChannelClassification)
				if err != nil {
					t.Fatal(err)
				}
				if err := service.handleGameUpper(session, request); err != nil {
					t.Fatalf("handle upper pvp seat state: %v", err)
				}
			}
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
			if len(rest) != 0 || packet.Header.MsgID != currentPVPSeatStateMsgID || !bytes.Equal(packet.Body, []byte{1, 0, 2, 1, 0, 2, 29, 0}) {
				t.Fatalf("seat response header=%+v body=%x rest=%x", packet.Header, packet.Body, rest)
			}
		})
	}
}

func TestHandlePVPTeamModeAndMapIndexPublishRoomState(t *testing.T) {
	for _, route := range []string{"legacy", "upper"} {
		t.Run(route, func(t *testing.T) {
			service := pvpChannelInfoTestService(t)
			conn := &bufferConn{}
			session := &gameSession{
				conn:                conn,
				selectedCharacterID: 29,
				channel:             service.catalog.Channels()[1],
			}
			seedPVPRoomTestSession(service, session)
			dispatch := func(msgID uint16, body []byte) {
				if route == "legacy" {
					if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), msgID, body); err != nil {
						t.Fatalf("handle legacy pvp command %d: %v", msgID, err)
					}
					return
				}
				request, err := dnfproto.BuildChannelPacket(msgID, body, 0, dnfproto.DefaultChannelClassification)
				if err != nil {
					t.Fatal(err)
				}
				if err := service.handleGameUpper(session, request); err != nil {
					t.Fatalf("handle upper pvp command %d: %v", msgID, err)
				}
			}

			dispatch(uint16(dnfenum.CmdPacketSetPVPTeamMode), []byte{3})
			modeStream := conn.write.Bytes()
			modePacket, _ := splitCurrentGameServerUpperPacketAuto(t, modeStream)
			if modePacket.Header.MsgID != currentPVPSeatStateMsgID || !bytes.Equal(modePacket.Body, []byte{1, 0, 3, 1, 0, 1, 29, 0}) {
				t.Fatalf("team-mode response header=%+v body=%x", modePacket.Header, modePacket.Body)
			}

			dispatch(uint16(dnfenum.CmdPacketSetPVPMapIndex), []byte{5, 0})
			mapPacket, trailing := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes()[len(modeStream):])
			if len(trailing) != 0 || mapPacket.Header.MsgID != currentPVPRoomStateMsgID || !bytes.Equal(mapPacket.Body, []byte{1, 0, 1, 0, 5, 0, 3, 0, 0, 0, 0}) {
				t.Fatalf("map response header=%+v body=%x trailing=%x", mapPacket.Header, mapPacket.Body, trailing)
			}
		})
	}
}

func TestPVPSharedDirectoryAndMembership(t *testing.T) {
	for _, route := range []string{"legacy", "upper"} {
		t.Run(route, func(t *testing.T) {
			s := pvpRoomTestService(t)
			newSession := func(id uint16, channel int) *gameSession {
				peer := &gameSession{conn: &bufferConn{}, selectedCharacterID: id, accountID: "pvp-" + strconv.Itoa(int(id)), channel: s.catalog.Channels()[1], sceneBootstrapTailSent: true}
				peer.channel.ID = channel
				peer.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: id, TownID: 10, AreaID: 0, PositionX: 474, PositionY: 234, MovementCode: 5, PositionValid: true}
				peer.partyPeer = currentPartyPeerEndpointRegistration{InnerIPv4: [4]byte{127, 0, 0, 1}, OuterIPv4: [4]byte{127, 0, 0, 1}, Port: 12000 + id, MTU: currentPartyPeerMinimumMTU}
				return peer
			}
			dispatch := func(peer *gameSession, op dnfenum.CmdPacket, body []byte) {
				t.Helper()
				var err error
				if route == "legacy" {
					err = s.handleGameCommand(peer, byte(dnfenum.GameCmdCommand), uint16(op), body)
				} else {
					wire, buildErr := dnfproto.BuildChannelPacket(uint16(op), body, 0, dnfproto.DefaultChannelClassification)
					if buildErr != nil {
						t.Fatal(buildErr)
					}
					err = s.handleGameUpper(peer, wire)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			take := func(peer *gameSession) []dnfproto.ChannelPacket {
				t.Helper()
				conn := peer.conn.(*bufferConn)
				wire := append([]byte(nil), conn.write.Bytes()...)
				conn.write.Reset()
				var packets []dnfproto.ChannelPacket
				for len(wire) > 0 {
					p, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
					packets = append(packets, p)
					wire = rest
				}
				return packets
			}
			a, b, other := newSession(29, 501), newSession(30, 501), newSession(31, 502)
			if err := s.sendCurrentPVPDirectory(b); err != nil {
				t.Fatal(err)
			}
			if err := s.sendCurrentPVPDirectory(other); err != nil {
				t.Fatal(err)
			}
			dispatch(a, dnfenum.CmdPacketMakePVPRoom, []byte{5, 0, 0, 0, 0, 0})
			packets := take(b)
			if len(packets) != 1 || packets[0].Header.MsgID != 41 || binary.LittleEndian.Uint16(packets[0].Body[2:]) != a.pvpRoom.id {
				t.Fatalf("B did not receive A's room: %+v", packets)
			}
			if len(take(other)) != 0 {
				t.Fatal("room crossed channel boundary")
			}
			late := newSession(32, 501)
			if _, err := s.handleRuntimePartyDirectoryRefresh(late, []byte{0}); err != nil {
				t.Fatal(err)
			}
			if packets = take(late); len(packets) != 1 || packets[0].Header.MsgID != 41 {
				t.Fatalf("late subscriber missed room: %+v", packets)
			}
			take(a)
			roomID := a.pvpRoom.id
			join := []byte{byte(roomID), byte(roomID >> 8), 0}
			dispatch(other, dnfenum.CmdPacketEnterPVPRoom, join)
			if other.pvpRoom != nil {
				t.Fatal("cross-channel join accepted")
			}
			for _, malformed := range [][]byte{nil, {1, 0}, {1, 0, 2}, {1, 0, 1, 9, 0, 0, 0}, {1, 0, 0, 1}} {
				dispatch(b, dnfenum.CmdPacketEnterPVPRoom, malformed)
				if b.pvpRoom != nil {
					t.Fatal("malformed join accepted")
				}
			}
			take(b)
			port := b.partyPeer.Port
			b.partyPeer.Port = 0
			dispatch(b, dnfenum.CmdPacketEnterPVPRoom, join)
			if b.pvpRoom != nil {
				t.Fatal("membership published before UDP endpoint discovery")
			}
			b.partyPeer.Port = port
			take(b)
			a.pvpRoom.request.hasPassword = true
			a.pvpRoom.request.password = []byte("x")
			dispatch(b, dnfenum.CmdPacketEnterPVPRoom, join)
			if b.pvpRoom != nil {
				t.Fatal("password-free join entered protected room")
			}
			take(b)
			join = []byte{byte(roomID), byte(roomID >> 8), 1, 1, 0, 0, 0, 'x'}
			dispatch(b, dnfenum.CmdPacketEnterPVPRoom, join)
			if b.pvpRoom != a.pvpRoom || a.pvpRoom.seatFor(b) != 1 {
				t.Fatal("join did not use shared room")
			}
			packets = take(b)
			endpointIndex, roomIndex := -1, -1
			for index, packet := range packets {
				if packet.Header.Classification == 0 && packet.Header.MsgID == 11 {
					endpointIndex = index
					if len(packet.Body) != 23 || packet.Body[0] != 1 || binary.LittleEndian.Uint16(packet.Body[1:3]) != 29 ||
						binary.BigEndian.Uint16(packet.Body[11:13]) != 12029 || packet.Body[17] != 1 ||
						binary.LittleEndian.Uint32(packet.Body[13:17]) != s.partyPeerRowAccountID(29, 29) {
						t.Fatalf("peer endpoint body=%x", packet.Body)
					}
				}
				if packet.Header.Classification == 0 && packet.Header.MsgID == 41 {
					roomIndex = index
				}
			}
			if endpointIndex < 0 || roomIndex <= endpointIndex {
				t.Fatal("PVP membership started before the native UDP endpoint table")
			}
			last := packets[len(packets)-1]
			if last.Header.Classification != 1 || last.Header.MsgID != 51 || len(last.Body) != 17 || last.Body[0] != 1 {
				t.Fatalf("native join ack=%+v %x", last.Header, last.Body)
			}
			for seat := 0; seat < currentPVPRoomSeatCount; seat++ {
				if last.Body[1+seat*2] != a.pvpRoom.ready[seat] || last.Body[2+seat*2] != 100 {
					t.Fatalf("seat %d must retain readiness and start at full HP: ack=%x", seat, last.Body)
				}
			}
			if len(packets) < 7 || packets[0].Header.MsgID != 2 || packets[0].Body[0] != 0 || packets[1].Body[0] != 1 {
				t.Fatal("peer actor missing before room membership")
			}
			take(a)
			take(late)
			dispatch(b, dnfenum.CmdPacketSetPVPReadyState, []byte{1})
			for _, peer := range []*gameSession{a, b} {
				ready := take(peer)
				if len(ready) != 1 || ready[0].Header.MsgID != 44 || !bytes.Equal(ready[0].Body, []byte{1, 1}) {
					t.Fatalf("ready notification=%+v", ready)
				}
			}
			if len(take(late)) != 0 {
				t.Fatal("ready state leaked to another room/lobby")
			}
			dispatch(b, dnfenum.CmdPacketSetPVPMapIndex, []byte{3, 0})
			if a.pvpRoom.request.mapIndex != 0 {
				t.Fatal("non-owner changed map")
			}
			dispatch(a, dnfenum.CmdPacketSetPVPMapIndex, []byte{5, 0})
			if a.pvpRoom.request.mapIndex != 5 {
				t.Fatal("room map not shared")
			}
			dispatch(late, dnfenum.CmdPacketMakePVPRoom, []byte{6, 0, 0, 0, 0, 0})
			if late.pvpRoom.id == roomID {
				t.Fatal("multiple rooms reused ID")
			}
			take(b)
			dispatch(a, dnfenum.CmdPacketSetPVPSeatState, []byte{0, 0xfe})
			if a.pvpRoom != nil || b.pvpRoom == nil || b.pvpRoom.manager != 1 {
				t.Fatal("owner departure did not preserve shared room")
			}
			take(late)
			s.unbindGameSession(b)
			if s.pvpRooms[roomID] != nil {
				t.Fatal("disconnected room survived")
			}
			packets = take(late)
			if len(packets) != 2 || packets[0].Header.MsgID != 43 || packets[1].Header.MsgID != 42 {
				t.Fatalf("directory teardown=%+v", packets)
			}
			s.resetGameSessionForCharacterSelect(late)
			if len(s.pvpRooms) != 0 {
				t.Fatal("reselect left a ghost room")
			}
		})
	}
}
