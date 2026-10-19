package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"

	"strconv"
	"testing"
	"time"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func TestPVPMatchingNativeLifecycle(t *testing.T) {
	for _, route := range []string{"legacy", "upper"} {
		for _, scenario := range []string{"result", "result_timeout", "cancel", "timeout", "disconnect", "pending_udp", "stale", "banned", "same_account", "other_channel", "manual"} {
			t.Run(route+"/"+scenario, func(t *testing.T) {
				s := pvpRoomTestService(t)
				s.questCatalog = buildPVPResultQuestCatalog(t)
				queue := newFakeCurrentDungeonDeathTimerQueue()
				s.gameplayTimers = queue
				s.pvpMapOrder = []int64{50000, 50042}
				s.gameSessions = make(map[uint16]*gameSession)
				newSession := func(id uint16) *gameSession {
					p := &gameSession{conn: &bufferConn{}, accountID: "pvp-" + strconv.Itoa(int(id)),
						selectedCharacterID: id, characterGeneration: uint64(id), channel: s.catalog.Channels()[1], sceneBootstrapTailSent: true}
					p.channel.ID, p.channel.Type = 501, 8
					p.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: id, TownID: 10, AreaID: 0, PositionX: 474, PositionY: 234, MovementCode: 5, PositionValid: true}
					p.partyPeer = currentPartyPeerEndpointRegistration{InnerIPv4: [4]byte{127, 0, 0, 1}, OuterIPv4: [4]byte{127, 0, 0, 1}, Port: 12000 + id, MTU: currentPartyPeerMinimumMTU}
					s.gameSessions[id] = p
					return p
				}
				a, b, viewer := newSession(29), newSession(30), newSession(31)
				dispatch := func(p *gameSession, op uint16, body []byte) {
					t.Helper()
					var err error
					if route == "legacy" {
						err = s.handleGameCommand(p, 1, op, body)
					} else {
						var wire []byte
						wire, err = dnfproto.BuildChannelPacket(op, body, 0, 1)
						if err == nil {
							err = s.handleGameUpper(p, wire)
						}
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				take := func(p *gameSession) []dnfproto.ChannelPacket {
					t.Helper()
					conn := p.conn.(*bufferConn)
					wire := append([]byte(nil), conn.write.Bytes()...)
					conn.write.Reset()
					var packets []dnfproto.ChannelPacket
					for len(wire) > 0 {
						packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
						packets = append(packets, packet)
						wire = rest
					}
					return packets
				}
				request := []byte{1, 0, 0, 0, 0, 255, 255, 255, 255, 255, 255, 255, 255, 0}
				cancel := []byte{1, 0, 0}
				if scenario == "manual" {
					a.channel.Type = 24
					dispatch(a, 520, request)
					if len(s.pvpMatching) != 0 || a.pvpRoom != nil {
						t.Fatal("自动匹配影响手动房间")
					}
					return
				}
				if scenario == "banned" {
					request[4], request[5], request[6] = 1, 0, 1
				}
				dispatch(a, 520, request)
				dispatch(a, 520, request)
				ack := take(a)
				if len(s.pvpMatching) != 1 || len(ack) != 2 {
					t.Fatal("duplicate request added a queue entry or lost its ACK")
				}
				for _, p := range ack {
					if p.Header.MsgID != 520 || p.Header.Classification != 1 || !bytes.Equal(p.Body, []byte{1, 1, 0, 0, 0}) {
						t.Fatalf("native search ACK=%+v", p)
					}
				}
				switch scenario {
				case "same_account":
					b.accountID = a.accountID
				case "other_channel":
					b.channel.ID++
				case "stale":
					a.characterGeneration++
				case "pending_udp":
					b.partyPeer.Port = 0
				}
				// Refresh the queued actor from persistent data at pairing time.
				repos, _ := s.repositoryGroup()
				character, _, err := repos.Character.Load(context.Background(), "29")
				if err != nil {
					t.Fatal(err)
				}
				character.Stats["pvp_rank_point"] = 1234
				character.Stats["pvp_grade"] = 9
				if err := repos.Character.Save(context.Background(), character); err != nil {
					t.Fatal(err)
				}
				if err := s.sendCurrentPVPDirectory(viewer); err != nil {
					t.Fatal(err)
				}
				dispatch(b, 520, request)
				if scenario == "banned" || scenario == "same_account" || scenario == "other_channel" || scenario == "stale" {
					if a.pvpRoom != nil || b.pvpRoom != nil {
						t.Fatal("ineligible pair created a room")
					}
					want := 2
					if scenario == "stale" {
						want = 1
					}
					if len(s.pvpMatching) != want {
						t.Fatalf("queue size=%d", len(s.pvpMatching))
					}
					dispatch(b, 521, cancel)
					s.closeCurrentPVPSession(a)
					if len(s.pvpMatching) != 0 {
						t.Fatal("queue retained cancelled/disconnected request")
					}
					return
				}
				if scenario == "pending_udp" {
					if a.pvpRoom != nil || b.pvpRoom != nil || len(s.pvpRooms) != 0 {
						t.Fatal("pending endpoint retained a partial room")
					}
					for _, p := range []*gameSession{a, b} {
						found := false
						for _, packet := range take(p) {
							if packet.Header.MsgID == 506 {
								found = true
							}
						}
						if !found {
							t.Fatal("pending endpoint left native search active")
						}
					}
					return
				}
				room := a.pvpRoom
				if room == nil || b.pvpRoom != room || room.started || len(s.pvpMatching) != 0 {
					t.Fatal("pair did not create one waiting room")
				}
				for _, p := range []*gameSession{a, b} {
					packets := take(p)
					endpoint, roster := -1, -1
					for i, packet := range packets {
						switch packet.Header.MsgID {
						case 11:
							endpoint = i
						case 41:
							roster = i
						case 51:
							if len(packet.Body) != 17 || packet.Body[0] != 1 {
								t.Fatal("missing native HP initialization")
							}
							for seat := 0; seat < 8; seat++ {
								if packet.Body[2+seat*2] != 100 {
									t.Fatal("zero starting HP")
								}
							}
						case 504:
							v := packet.Body
							if len(v) != 32 || v[0] != 2 || binary.LittleEndian.Uint32(v[1:]) != room.matchingToken || v[5] != 2 ||
								binary.LittleEndian.Uint16(v[6:]) != 29 || v[8] != 1 || binary.LittleEndian.Uint32(v[9:]) != 1234 || v[13] != 0 || binary.LittleEndian.Uint16(v[19:]) != 30 || v[21] != 2 {
								t.Fatalf("native matching roster=%x", v)
							}
						}
					}
					if endpoint < 0 || roster <= endpoint || len(packets) < 4 || packets[len(packets)-1].Header.MsgID != 505 {
						t.Fatal("native actor/endpoint/roster/completion order changed")
					}
				}
				if err := s.sendCurrentPVPDirectory(viewer); err != nil {
					t.Fatal(err)
				}
				if len(take(viewer)) != 0 {
					t.Fatal("matching room leaked into manual directory")
				}
				staleTimer := queue.tasks[0].callback
				switch scenario {
				case "cancel":
					dispatch(a, 521, cancel)
				case "timeout":
					staleTimer(time.Now())
				case "disconnect":
					s.closeCurrentPVPSession(a)
				case "result", "result_timeout", "reconnect_udp":
					token := binary.LittleEndian.AppendUint32(nil, room.matchingToken)
					dispatch(a, 570, token[:3])
					dispatch(a, 570, binary.LittleEndian.AppendUint32(nil, room.matchingToken+1))
					dispatch(viewer, 570, token)
					if room.matchingLoaded[0] {
						t.Fatal("invalid load completion accepted")
					}
					dispatch(a, 570, token)
					dispatch(a, 570, token)
					if room.started {
						t.Fatal("started before peer loaded")
					}
					dispatch(b, 570, token)
					if !room.started {
						t.Fatal("both clients loaded but match did not start")
					}
					staleTimer(time.Now())
					if !room.started {
						t.Fatal("stale load timeout aborted started match")
					}
					for _, p := range []*gameSession{a, b} {
						packets := take(p)
						if len(packets) != 4 || packets[0].Header.MsgID != 506 || !bytes.Equal(packets[0].Body, []byte{2}) || packets[2].Header.MsgID != 45 || !bytes.Equal(packets[2].Body, []byte{1, 2}) {
							t.Fatalf("native start packets=%+v", packets)
						}
					}
					dispatch(b, 55, []byte{30, 0, 29, 0, 1, 100, 0})
					if room.match == nil || !room.match.resultSent || room.match.winner != 0 {
						t.Fatal("matching death did not settle")
					}
					if scenario == "result_timeout" {
						queue.tasks[len(queue.tasks)-1].callback(time.Now())
					} else {
						dispatch(a, 57, nil)
						dispatch(b, 57, nil)
					}
				}
				if a.pvpRoom != nil || b.pvpRoom != nil || len(s.pvpRooms) != 0 {
					t.Fatal("terminal flow retained matching membership")
				}
				if len(take(viewer)) != 0 {
					t.Fatal("matching result reached unrelated viewer")
				}
				for _, p := range []*gameSession{a, b} {
					if scenario == "disconnect" && p == a {
						continue
					}
					failed, left, destroyed := false, false, false
					for _, packet := range take(p) {
						switch packet.Header.MsgID {
						case 506:
							failed = bytes.Equal(packet.Body, []byte{1})
						case 43:
							left = true
						case 42:
							destroyed = true
						}
					}
					completed := scenario == "result" || scenario == "result_timeout" || scenario == "reconnect_udp"
					if failed == completed || !left || !destroyed {
						t.Fatalf("close packets: completed=%t failure=%t seat=%t room=%t", completed, failed, left, destroyed)
					}
				}
				staleTimer(time.Now())
				if scenario == "result" || scenario == "result_timeout" || scenario == "reconnect_udp" {
					dispatch(a, 520, request)
					if len(s.pvpMatching) != 1 || s.pvpMatching[0].member.session != a {
						t.Fatal("settled player could not search again")
					}
				}
			})
		}
	}
}

func TestPVPMatchingRejectsMalformedRequest(t *testing.T) {
	valid := []byte{1, 0, 0, 0, 0, 255, 255, 255, 255, 255, 255, 255, 255, 0}
	for _, body := range [][]byte{nil, valid[:13], append(append([]byte(nil), valid...), 0)} {
		if _, err := parseCurrentPVPMatchingRequest(body); err == nil {
			t.Fatal("accepted invalid request length")
		}
	}
	for _, change := range [][2]byte{{0, 0}, {1, 1}, {2, 1}, {3, 2}, {4, 2}, {5, 0}, {13, 2}} {
		body := append([]byte(nil), valid...)
		body[change[0]] = change[1]
		if _, err := parseCurrentPVPMatchingRequest(body); err == nil {
			t.Fatalf("accepted invalid body=%x", body)
		}
	}
}
