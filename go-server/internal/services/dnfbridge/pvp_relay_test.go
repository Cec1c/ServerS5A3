package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func relayTestRoom(t *testing.T) (*Service, *currentPVPRoom, []*gameSession, *fakeCurrentDungeonDeathTimerQueue) {
	t.Helper()
	s := pvpRoomTestService(t)
	q := newFakeCurrentDungeonDeathTimerQueue()
	s.gameplayTimers = q
	players := make([]*gameSession, 5)
	for i := range players {
		players[i] = &gameSession{conn: &bufferConn{}, accountID: fmt.Sprintf("pvp-%d", 29+i), selectedCharacterID: uint16(29 + i), channel: s.catalog.Channels()[1]}
	}
	seedPVPRoomTestSession(s, players[0])
	room := players[0].pvpRoom
	room.battleMode = 3
	for i, p := range players {
		p.pvpRoom = room
		room.members[i] = &currentPVPMember{session: p, characterID: p.selectedCharacterID}
		room.seatState[i], room.ready[i] = byte(1+i%2), 1
	}
	room.seatState[4] = 3
	s.pvpMapOrder = []int64{50000, 50042}
	if err := s.startCurrentPVPRoomLocked(room, players[0]); err != nil {
		t.Fatal(err)
	}
	for _, p := range players {
		totalTestPackets(t, p)
	}
	return s, room, players, q
}

func relayTestCommand(t *testing.T, s *Service, p *gameSession, route string, op uint16, body []byte) {
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

// Decode the actual class-0/112 receiver's two groups. The chosen seat must
// belong to its team and have the highest published score.
func relayTestSelection(t *testing.T, players []*gameSession, counts [2]int) [2]byte {
	t.Helper()
	var wire []byte
	var selected [2]byte
	for _, p := range players {
		packets := totalTestPackets(t, p)
		if len(packets) != 1 || packets[0].Header.Classification != 0 || packets[0].Header.MsgID != 112 {
			t.Fatalf("missing relay dice notification: %+v", packets)
		}
		if wire == nil {
			wire = packets[0].Body
		} else if !bytes.Equal(wire, packets[0].Body) {
			t.Fatal("room members received different dice results")
		}
	}
	if len(wire) == 0 || wire[0] != 2 {
		t.Fatalf("invalid group count: %x", wire)
	}
	wire = wire[1:]
	for side, count := range counts {
		if len(wire) < 2 || int(wire[1]) != count || len(wire) < 2+3*count {
			t.Fatalf("invalid dice rows: %x", wire)
		}
		selected[side] = wire[0]
		best, bestSeat := uint16(0), byte(255)
		for i := 0; i < count; i++ {
			seat, score := wire[2+3*i], binary.LittleEndian.Uint16(wire[3+3*i:])
			if seat >= 4 || int(seat%2) != side || score < 1 || score > 100 {
				t.Fatalf("invalid dice seat/score: %x", wire)
			}
			if score > best {
				best, bestSeat = score, seat
			}
		}
		if count > 0 && selected[side] != bestSeat || count == 0 && selected[side] != 255 {
			t.Fatalf("selection does not match dice: %x", wire)
		}
		wire = wire[2+3*count:]
	}
	if len(wire) != 0 {
		t.Fatal("unexpected selection tail")
	}
	return selected
}

func TestPVPRelayLoadsRollsAndReplacesDefeatedFighter(t *testing.T) {
	for _, route := range []string{"legacy", "upper"} {
		t.Run(route, func(t *testing.T) {
			s, room, players, q := relayTestRoom(t)
			for _, seat := range []int{4, 0, 0, 1, 2} {
				relayTestCommand(t, s, players[seat], route, 298, nil)
			}
			if n, _, _ := q.counts(); n != 0 {
				t.Fatal("observer/duplicate load advanced selection before all fighters loaded")
			}
			relayTestCommand(t, s, players[3], route, 299, []byte{1, 0, 1})
			relayTestCommand(t, s, players[3], route, 298, []byte{0})
			if n, _, _ := q.counts(); n != 0 {
				t.Fatal("P2P report/malformed load advanced selection")
			}
			relayTestCommand(t, s, players[3], route, 298, nil)
			if n, _, _ := q.counts(); n != 1 {
				t.Fatalf("all fighters loaded but relay dice were never scheduled: %d", n)
			}
			q.now = q.now.Add(5 * time.Second)
			q.task(t, 0).callback(q.now)
			selected := relayTestSelection(t, players, [2]int{2, 2})
			for _, p := range players {
				relayTestCommand(t, s, p, route, 298, nil)
				if len(totalTestPackets(t, p)) != 0 {
					t.Fatal("duplicate load rerolled dice")
				}
			}
			victim, killer := players[selected[1]], players[selected[0]]
			death := make([]byte, 7)
			binary.LittleEndian.PutUint16(death, victim.selectedCharacterID)
			binary.LittleEndian.PutUint16(death[2:], killer.selectedCharacterID)
			death[5] = 80
			reserve := players[selected[1]^2]
			invalid := append([]byte(nil), death...)
			binary.LittleEndian.PutUint16(invalid, reserve.selectedCharacterID)
			relayTestCommand(t, s, reserve, route, 55, invalid)
			if room.match.dead[selected[1]^2] {
				t.Fatal("reserve reported a death while not fighting")
			}
			relayTestCommand(t, s, victim, route, 55, death)
			for _, p := range players {
				packets := totalTestPackets(t, p)
				if len(packets) != 1 || packets[0].Header.MsgID != 46 {
					t.Fatalf("expected death before replacement: %+v", packets)
				}
			}
			q.now = q.now.Add(5 * time.Second)
			q.task(t, 1).callback(q.now)
			next := relayTestSelection(t, players, [2]int{0, 1})
			if next[1] != selected[1]^2 || room.match.resultSent {
				t.Fatal("defeated fighter did not hand off to teammate")
			}
			relayTestCommand(t, s, victim, route, 55, death)
			binary.LittleEndian.PutUint16(death, reserve.selectedCharacterID)
			relayTestCommand(t, s, reserve, route, 55, death)
			if !room.match.resultSent || room.match.winner >= currentPVPRoomSeatCount || room.seatState[room.match.winner] != room.seatState[selected[0]] {
				t.Fatal("last defeated teammate did not end the match")
			}
		})
	}
}

func TestPVPRelayInitialRequestWithoutLoadPackets(t *testing.T) {
	for _, route := range []string{"legacy", "upper"} {
		t.Run(route, func(t *testing.T) {
			s, room, players, q := relayTestRoom(t)
			// The deployed client sends empty C2S112 after op45, without
			// C2S298/299. Observers and malformed requests cannot start dice.
			relayTestCommand(t, s, players[4], route, 112, nil)
			relayTestCommand(t, s, players[0], route, 112, []byte{1})
			if n, _, _ := q.counts(); n != 0 {
				t.Fatal("invalid initial request started selection")
			}
			relayTestCommand(t, s, players[0], route, 112, nil)
			if n, _, _ := q.counts(); n != 1 {
				t.Fatalf("initial native request did not schedule dice: %d", n)
			}
			for _, p := range players {
				packets := totalTestPackets(t, p)
				if len(packets) != 1 || packets[0].Header.MsgID != 113 || !bytes.Equal(packets[0].Body, []byte{0}) {
					t.Fatalf("initial request was not acknowledged: %+v", packets)
				}
			}
			// Replay the other player's request from the production trace;
			// neither a second request nor a later 298 resets the countdown.
			relayTestCommand(t, s, players[1], route, 112, nil)
			relayTestCommand(t, s, players[0], route, 298, nil)
			if n, _, _ := q.counts(); n != 1 {
				t.Fatal("additional request duplicated the selection timer")
			}
			for _, p := range players {
				totalTestPackets(t, p)
			}
			q.now = q.now.Add(5 * time.Second)
			q.fire(q.task(t, 0), false)
			selected := relayTestSelection(t, players, [2]int{2, 2})
			if room.match.relay.choosing {
				t.Fatal("native requests left match stuck choosing")
			}
			// Non-requesting teammates still receive the result and can
			// request a substitution later without a synthetic load packet.
			reserve := selected[0] ^ 2
			relayTestCommand(t, s, players[reserve], route, 112, nil)
			if n, _, _ := q.counts(); n != 2 {
				t.Fatal("native reserve request still requires a load packet")
			}
			for _, p := range players {
				packets := totalTestPackets(t, p)
				if len(packets) != 1 || packets[0].Header.MsgID != 113 || !bytes.Equal(packets[0].Body, []byte{reserve}) {
					t.Fatalf("native reserve request was not broadcast: %+v", packets)
				}
			}
		})
	}
}

func TestPVPRelayTwoPlayerNativeRequestTrace(t *testing.T) {
	s, room, players, q := relayTestRoom(t)
	room.members[2], room.members[3], room.members[4] = nil, nil, nil
	players = players[:2]
	// Exact empty-body C2S112 packets from the failed two-player match.
	for i, value := range []string{"0170000d0000001edd26c02800", "0170000d000000204593f92200"} {
		wire, err := hex.DecodeString(value)
		if err != nil {
			t.Fatal(err)
		}
		packet, err := dnfproto.ParseLegacyGamePacket(wire)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.handleGameCommand(players[i], packet.Header.Cmd, packet.Header.Type, packet.Body); err != nil {
			t.Fatal(err)
		}
	}
	if n, _, _ := q.counts(); n != 1 {
		t.Fatalf("native trace did not start exactly one selection: %d", n)
	}
	for _, p := range players {
		packets := totalTestPackets(t, p)
		if len(packets) != 2 || packets[0].Header.MsgID != 113 || packets[1].Header.MsgID != 113 {
			t.Fatalf("native requests were not acknowledged before dice: %+v", packets)
		}
	}
	q.now = q.now.Add(5 * time.Second)
	q.fire(q.task(t, 0), false)
	selected := relayTestSelection(t, players, [2]int{1, 1})
	if selected != [2]byte{0, 1} {
		t.Fatalf("wrong two-player selection: %v", selected)
	}
	for _, p := range players {
		relayTestCommand(t, s, p, "legacy", 112, nil)
		relayTestCommand(t, s, p, "legacy", 298, nil)
		if len(totalTestPackets(t, p)) != 0 {
			t.Fatal("selected fighter's repeated request restarted dice")
		}
	}
	if n, _, _ := q.counts(); n != 1 {
		t.Fatal("duplicate request created another selection")
	}
}

func TestPVPRelayReserveRequestsAndTimerLifecycle(t *testing.T) {
	s, room, players, q := relayTestRoom(t)
	for _, p := range players {
		relayTestCommand(t, s, p, "upper", 298, nil)
	}
	q.now = q.now.Add(5 * time.Second)
	q.fire(q.task(t, 0), false)
	selected := relayTestSelection(t, players, [2]int{2, 2})
	reserve := players[selected[0]^2]
	for _, p := range []*gameSession{players[4], players[selected[0]]} {
		relayTestCommand(t, s, p, "upper", 112, nil)
	}
	relayTestCommand(t, s, reserve, "upper", 112, []byte{1})
	for _, p := range players {
		if len(totalTestPackets(t, p)) != 0 {
			t.Fatal("observer/fighter/malformed request advanced relay")
		}
	}
	// A second click withdraws the request. A delayed callback then has no
	// candidates and must not reset either fighter.
	for i := 0; i < 2; i++ {
		relayTestCommand(t, s, reserve, "upper", 112, nil)
		for _, p := range players {
			packets := totalTestPackets(t, p)
			if len(packets) != 1 || packets[0].Header.Classification != 0 || packets[0].Header.MsgID != 113 || !bytes.Equal(packets[0].Body, []byte{selected[0] ^ 2}) {
				t.Fatalf("reserve request not broadcast: %+v", packets)
			}
		}
	}
	q.now = q.now.Add(5 * time.Second)
	q.fire(q.task(t, 1), false)
	if room.match.relay.selected != selected {
		t.Fatal("withdrawn request changed fighters")
	}
	for _, p := range players {
		if len(totalTestPackets(t, p)) != 0 {
			t.Fatal("withdrawn request emitted a selection")
		}
	}
	relayTestCommand(t, s, reserve, "upper", 112, nil)
	for _, p := range players {
		totalTestPackets(t, p)
	}
	q.now = q.now.Add(5 * time.Second)
	q.fire(q.task(t, 2), false)
	next := relayTestSelection(t, players, [2]int{1, 0})
	if next[0] != selected[0]^2 || room.match.relay.selected[1] != selected[1] {
		t.Fatal("substitution did not retain opposing fighter")
	}
	// A timeout from another battle mode cannot settle the relay roster.
	relayTestCommand(t, s, players[next[0]], "upper", 56, make([]byte, 32))
	if room.match.settlementPending {
		t.Fatal("ordinary timeout settled relay")
	}
	relayTestCommand(t, s, players[selected[0]], "upper", 112, nil)
	stale := q.task(t, 3)
	for _, p := range players {
		totalTestPackets(t, p)
	}
	s.closeCurrentPVPSession(players[0])
	if !stale.cancelled || room.match != nil {
		t.Fatal("aborted room retained relay timer")
	}
	for _, p := range players {
		totalTestPackets(t, p)
	}
	q.fire(stale, true)
	for _, p := range players {
		if len(totalTestPackets(t, p)) != 0 {
			t.Fatal("stale timer wrote to an aborted room")
		}
	}
}

func TestPVPRelayLateObserverReceivesCurrentFighters(t *testing.T) {
	s, room, players, q := relayTestRoom(t)
	for _, p := range players[:4] {
		relayTestCommand(t, s, p, "upper", 298, nil)
	}
	q.now = q.now.Add(5 * time.Second)
	q.fire(q.task(t, 0), false)
	selected := relayTestSelection(t, players[:4], [2]int{2, 2})
	reserve := players[selected[0]^2]
	relayTestCommand(t, s, reserve, "upper", 112, nil)
	for _, p := range players {
		totalTestPackets(t, p)
	}
	q.now = q.now.Add(5 * time.Second)
	q.fire(q.task(t, 1), false)
	relayTestSelection(t, players[:4], [2]int{1, 0})
	if len(totalTestPackets(t, players[4])) != 0 {
		t.Fatal("selection sent to an observer before loading")
	}
	relayTestCommand(t, s, players[4], "upper", 298, nil)
	packets := totalTestPackets(t, players[4])
	if len(packets) != 1 || packets[0].Header.MsgID != 112 || packets[0].Body[1] != room.match.relay.selected[0] || packets[0].Body[6] != selected[1] {
		t.Fatalf("late observer received retain sentinel without a previous fighter: %+v", packets)
	}
	relayTestCommand(t, s, players[4], "upper", 298, nil)
	for _, p := range players {
		if len(totalTestPackets(t, p)) != 0 {
			t.Fatal("late observer restarted selection")
		}
	}
}
