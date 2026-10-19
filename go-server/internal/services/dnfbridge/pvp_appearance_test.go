package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"strconv"
	"testing"
)

func TestPVPRoomAvatarVisibilityAfterBindingAndPeerJoin(t *testing.T) {
	for _, hostBits := range []byte{3, 11, 19, 27} {
		t.Run(strconv.Itoa(int(hostBits)), func(t *testing.T) {
			s := pvpRoomTestService(t)
			newSession := func(id uint16) *gameSession {
				return &gameSession{conn: &bufferConn{}, selectedCharacterID: id,
					accountID: "pvp-" + strconv.Itoa(int(id)), channel: s.catalog.Channels()[1],
					townPositionSnapshot: currentTownPositionSnapshot{CharacterID: id, TownID: 10, PositionX: 400, PositionY: 220, PositionValid: true},
					partyPeer:            currentPartyPeerEndpointRegistration{InnerIPv4: [4]byte{127, 0, 0, 1}, OuterIPv4: [4]byte{127, 0, 0, 1}, Port: 12000 + id, MTU: currentPartyPeerMinimumMTU}}
			}
			repos, _ := s.repositoryGroup()
			setBits := func(id uint16, bits byte) {
				t.Helper()
				c, found, err := repos.Character.Load(context.Background(), strconv.Itoa(int(id)))
				if err != nil || !found {
					t.Fatalf("character %d: found=%v err=%v", id, found, err)
				}
				c.Stats["user_state_bits"] = int64(bits)
				if err := repos.Character.Save(context.Background(), c); err != nil {
					t.Fatal(err)
				}
			}
			check := func(peer *gameSession, want map[uint16]byte) {
				t.Helper()
				conn := peer.conn.(*bufferConn)
				wire := conn.write.Bytes()
				got := make(map[uint16]byte)
				bound := false
				states := make(map[uint16]bool)
				for len(wire) > 0 {
					p, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
					wire = rest
					if p.Header.Classification != 0 {
						continue
					}
					switch p.Header.MsgID {
					case 41:
						bound = true
					case 3:
						if len(p.Body) == 4 && p.Body[0] == 1 {
							states[binary.LittleEndian.Uint16(p.Body[1:3])] = true
						}
					case 357:
						if len(p.Body) != 3 {
							t.Fatalf("visibility body=%x", p.Body)
						}
						id := binary.LittleEndian.Uint16(p.Body)
						if !bound || !states[id] {
							t.Fatalf("actor %d visibility sent before room binding/state", id)
						}
						if _, duplicate := got[id]; duplicate {
							t.Fatalf("duplicate visibility for actor %d", id)
						}
						got[id] = p.Body[2]
					}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("receiver %d visibility=%v want=%v", peer.selectedCharacterID, got, want)
				}
				conn.write.Reset()
			}
			a, b, lobby := newSession(29), newSession(30), newSession(31)
			setBits(29, hostBits)
			if err := s.sendCurrentPVPDirectory(lobby); err != nil {
				t.Fatal(err)
			}
			if err := s.handleMakePVPRoom(a, []byte{5, 0, 0, 0, 0, 0}, "test"); err != nil {
				t.Fatal(err)
			}
			check(a, map[uint16]byte{29: hostBits})
			check(lobby, map[uint16]byte{})
			// A changes the skin option after creating the room. B must receive
			// the saved current option, not A's stale entry-time snapshot.
			updated := hostBits ^ 8
			setBits(29, updated)
			setBits(30, 19)
			id := a.pvpRoom.id
			if err := s.handleEnterPVPRoom(b, []byte{byte(id), byte(id >> 8), 0}); err != nil {
				t.Fatal(err)
			}
			check(a, map[uint16]byte{30: 19})
			check(b, map[uint16]byte{29: updated, 30: 19})
			check(lobby, map[uint16]byte{})
		})
	}
}
