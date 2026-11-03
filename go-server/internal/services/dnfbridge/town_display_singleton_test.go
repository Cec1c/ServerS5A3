package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

func TestTownDisplayOnlySoloStateRequiresExactSingleton(t *testing.T) {
	for _, row := range []struct {
		name  string
		state alignedcmd.PartyState
		want  bool
	}{
		{"no party", alignedcmd.PartyState{}, true},
		{"own singleton", alignedcmd.PartyState{PartyID: 7, UserID: 1001, Members: []alignedcmd.PartyMemberState{{UserID: 1001}}}, true},
		{"empty nonzero lobby", alignedcmd.PartyState{PartyID: 7, UserID: 1001}, false},
		{"foreign singleton", alignedcmd.PartyState{PartyID: 7, UserID: 1002, Members: []alignedcmd.PartyMemberState{{UserID: 1002}}}, false},
		{"wrong singleton leader", alignedcmd.PartyState{PartyID: 7, UserID: 1002, Members: []alignedcmd.PartyMemberState{{UserID: 1001}}}, false},
		{"two members", alignedcmd.PartyState{PartyID: 7, UserID: 1001, Members: []alignedcmd.PartyMemberState{{UserID: 1001}, {UserID: 1002}}}, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			if got := townDisplayOnlySoloState(row.state, 1001); got != row.want {
				t.Fatalf("solo=%t want=%t state=%+v", got, row.want, row.state)
			}
		})
	}
}

// Reproduce the real create/join -> member op13 -> retained manager lobby.
// Exercise both entry directions, retain the exact singleton op9 bytes and
// require full peer promotion when that same lobby gains a second member.
func TestTownDisplayOnlyRetainedSingletonEntryAndRejoin(t *testing.T) {
	for _, enteringID := range []uint16{1001, 1002} {
		t.Run(strconv.Itoa(int(enteringID)), func(t *testing.T) {
			_, s, leader, member, leaderConn, memberConn := newTownCoPresenceTestService(t)
			s.options.currentClientVersion = "official-24.9"
			startTownPeerTest(t, s, leader, nil)
			startTownPeerTest(t, s, member, nil)
			if ok, _, reason, _ := s.forceManagedRuntimeParty(1001, 1002); !ok {
				t.Fatal(reason)
			}
			if handled, err := s.handleOnlinePartyCommand(member, 13, nil); err != nil || !handled {
				t.Fatalf("member leave handled=%t error=%v", handled, err)
			}
			drainTownPeerTest(t, s, leader)
			drainTownPeerTest(t, s, member)
			manager := s.runtimePartyManagerForService()
			snapshot, found := manager.SnapshotByUser(1001, 0)
			if !found || len(snapshot.Members) != 1 || snapshot.Leader != 1001 {
				t.Fatalf("fixture did not retain the authoritative singleton: %+v", snapshot)
			}
			partyID := snapshot.ID
			actor, _ := s.onlinePlayers.PlayerForCharacter(enteringID)
			otherID := uint16(1001)
			if enteringID == otherID {
				otherID = 1002
			}
			peer, _ := s.onlinePlayers.PlayerForCharacter(otherID)
			connections := []*bufferConn{leaderConn, memberConn}
			var baseline [2][]upperFrame
			t.Setenv(townDisplayOnlyEnv, "off")
			leaderConn.write.Reset()
			memberConn.write.Reset()
			s.broadcastTownPlayerEnter(&actor, []onlinePlayerInfo{peer})
			drainTownPeerTest(t, s, leader)
			drainTownPeerTest(t, s, member)
			for index, conn := range connections {
				baseline[index] = townDisplayFrames(t, conn.write.Bytes())
				requireTownPartyActorHPFrameIDs(t, baseline[index], 2, 2, 9, 23)
				conn.write.Reset()
			}
			t.Setenv(townDisplayOnlyEnv, "on")
			s.broadcastTownPlayerEnter(&actor, []onlinePlayerInfo{peer})
			drainTownPeerTest(t, s, leader)
			drainTownPeerTest(t, s, member)
			for index, conn := range connections {
				frames := townDisplayFrames(t, conn.write.Bytes())
				requireTownPartyActorHPFrameIDs(t, frames, 2, 9, 23)
				if !bytes.Equal(frames[1].body, baseline[index][2].body) || !bytes.Equal(frames[2].body, baseline[index][3].body) {
					t.Fatal("singleton party identity or native area notification changed")
				}
				conn.write.Reset()
			}
			kept, found := manager.SnapshotByUser(1001, 0)
			if !found || kept.ID != partyID || len(kept.Members) != 1 || kept.Members[0].UserID != 1001 {
				t.Fatal("town optimization altered the retained lobby")
			}
			if ok, _, reason, _ := s.forceManagedRuntimeParty(1001, 1002); !ok {
				t.Fatal("rejoin retained lobby: " + reason)
			}
			for index, conn := range connections {
				peerID := uint16(1002 - index)
				mode1, roster := -1, -1
				for position, frame := range townDisplayFrames(t, conn.write.Bytes()) {
					if frame.msgID == 2 && len(frame.body) >= 23 && frame.body[0] == 1 && binary.LittleEndian.Uint16(frame.body[21:]) == peerID {
						mode1 = position
					}
					if roster < 0 && frame.msgID == 153 {
						for _, row := range parseRealtimeInfoRows(t, frame.body) {
							if row.uid == peerID {
								roster = position
							}
						}
					}
					if roster < 0 && frame.msgID == 9 && len(frame.body) > 9 && frame.body[9] == currentSceneOp9ActorDisplayKind {
						rows, _ := parseOp9PartyFrameForProfile(t, frame.body, false, true)
						for _, row := range rows {
							if row.uid == peerID {
								roster = position
							}
						}
					}
				}
				if mode1 < 0 || roster < 0 || mode1 >= roster {
					t.Fatalf("rejoin lacked full peer before HUD: mode1=%d roster=%d", mode1, roster)
				}
			}
		})
	}
}
