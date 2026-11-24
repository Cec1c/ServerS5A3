package dnfbridge

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

func TestPVPRelayNoRequestsUsesSeatOrder(t *testing.T) {
	s, room, players, q := relayTestRoom(t)
	for _, p := range players {
		relayTestCommand(t, s, p, "upper", 298, nil)
	}
	if !room.match.relay.pending {
		t.Fatal("all ready without a dice click did not start countdown")
	}
	q.now = q.now.Add(5 * time.Second)
	q.fire(q.task(t, 0), false)
	for _, p := range players {
		packets := totalTestPackets(t, p)
		if len(packets) != 1 || !bytes.Equal(packets[0].Body, []byte{2, 0, 0, 1, 0}) {
			t.Fatalf("expected seat-order selection without dice rows: %+v", packets)
		}
	}
	room.match.dead[1] = true
	if err := s.finishCurrentPVPRelayRoundLocked(room); err != nil {
		t.Fatal(err)
	}
	q.now = q.now.Add(5 * time.Second)
	q.fire(q.task(t, 1), false)
	for _, p := range players {
		packets := totalTestPackets(t, p)
		if len(packets) != 1 || !bytes.Equal(packets[0].Body, []byte{2, 255, 0, 3, 0}) {
			t.Fatalf("expected survivor retention and next living seat: %+v", packets)
		}
	}
}

func TestPVPRelayRequestStillRollsDice(t *testing.T) {
	s, _, players, q := relayTestRoom(t)
	for _, p := range players {
		relayTestCommand(t, s, p, "upper", 298, nil)
	}
	relayTestCommand(t, s, players[0], "upper", 112, nil)
	for _, p := range players {
		totalTestPackets(t, p)
	}
	q.now = q.now.Add(5 * time.Second)
	q.fire(q.task(t, 0), false)
	for _, p := range players {
		packets := totalTestPackets(t, p)
		if len(packets) != 1 || len(packets[0].Body) != 17 || packets[0].Body[2] != 2 || packets[0].Body[10] != 2 {
			t.Fatalf("active request must retain native dice rows: %+v", packets)
		}
	}
}

func TestPVPRelayStartAllowsUnequalNonemptyTeams(t *testing.T) {
	for _, mode := range []byte{2, 3, 5} {
		for red := 0; red <= 3; red++ {
			for blue := 0; blue <= 3; blue++ {
				t.Run(fmt.Sprintf("mode%d/%dv%d", mode, red, blue), func(t *testing.T) {
					s, room, players, _ := relayTestRoom(t)
					room.started, room.match, room.battleMode = false, nil, mode
					for i := range room.members {
						room.members[i] = nil
					}
					for i := 0; i < red+blue; i++ {
						p := players[0]
						if i != 0 {
							p = &gameSession{conn: &bufferConn{}, selectedCharacterID: uint16(60 + i), channel: players[0].channel, pvpRoom: room}
						}
						room.members[i] = &currentPVPMember{session: p, characterID: p.selectedCharacterID}
						room.seatState[i], room.ready[i] = 1, 1
						if i >= red {
							room.seatState[i] = 2
						}
					}
					if red+blue == 0 {
						return // No owner; start is not a valid command.
					}
					if err := s.startCurrentPVPRoomLocked(room, players[0]); err != nil {
						t.Fatal(err)
					}
					want := red > 0 && blue > 0 && (mode == 3 || red == blue)
					if room.started != want {
						t.Fatalf("started=%v want=%v", room.started, want)
					}
				})
			}
		}
	}
}
