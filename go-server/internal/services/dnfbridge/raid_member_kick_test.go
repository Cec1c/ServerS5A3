package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/raid"
)

func TestCurrentRaidKickRemovesClickedMemberAndKeepsLeader(t *testing.T) {
	for _, action := range []string{"kick", "nonleader", "foreign", "stale", "target_stale", "target_owner", "target_channel", "unauth", "short", "self", "zero_self"} {
		t.Run(action, func(t *testing.T) {
			s, leader, peer, lc, pc := prepareAccountFriendTest(t)
			peer.channel = leader.channel
			leader.channel.ID, leader.channel.Type = 23, 23
			peer.channel = leader.channel
			key := uint32(s.channelAdvertiseID())<<24 | uint32(23<<16) | uint32(leader.selectedCharacterID)
			s.raids = map[uint32]*runtimeRaidState{key: {RaidKey: key, CurrentLayout: true, LeaderID: leader.selectedCharacterID, Members: []runtimeRaidMemberState{{CharID: leader.selectedCharacterID}, {CharID: peer.selectedCharacterID}}}}
			target := peer.selectedCharacterID
			requester := leader
			switch action {
			case "nonleader":
				requester, target = peer, leader.selectedCharacterID
			case "foreign":
				target = 8
			case "stale":
				replacement := &gameSession{conn: &bufferConn{}, channel: leader.channel}
				s.bindGameSessionCharacter(replacement, leader.selectedCharacterID)
				// Binding an auxiliary connection preserves the resident owner.
				// Only promotion makes this an actual replaced-main-session test.
				s.promoteResidentGameSession(replacement, leader.selectedCharacterID)
				if _, current := s.boundGameSessionCharacterSnapshot(leader); current {
					t.Fatal("fixture did not retire the requesting main session")
				}
			case "target_stale":
				replacement := &gameSession{conn: &bufferConn{}, channel: leader.channel}
				s.bindGameSessionCharacter(replacement, peer.selectedCharacterID)
				s.promoteResidentGameSession(replacement, peer.selectedCharacterID)
				if _, current := s.boundGameSessionCharacterSnapshot(peer); current {
					t.Fatal("fixture did not retire the target main session")
				}
			case "target_owner":
				peer.accountID = "foreign"
			case "target_channel":
				peer.channel.ID++
			case "unauth":
				leader.accountFromToken = false
			case "self":
				target = leader.selectedCharacterID
			case "zero_self":
				target = 0
			}
			lc.write.Reset()
			pc.write.Reset()
			// Rebinding can itself invoke the existing presence/party lifecycle.
			// A rejected request must preserve the state that exists when the
			// request arrives, including that legitimate prior transition.
			before := make(map[uint32]runtimeRaidState)
			for key, value := range s.raids {
				before[key] = cloneRuntimeRaidState(*value)
			}
			if action == "stale" || action == "target_stale" {
				t.Logf("post-rebind raid state before command: %+v", before)
			}
			body := binary.LittleEndian.AppendUint16(nil, target)
			if action == "short" {
				body = []byte{byte(target)}
			}
			if err := s.handleGameCommand(requester, 1, 665, body); err != nil {
				t.Fatal(err)
			}
			_, leaderIn := s.runtimeRaidForMember(leader.selectedCharacterID)
			_, peerIn := s.runtimeRaidForMember(peer.selectedCharacterID)
			if action == "kick" {
				if !leaderIn || peerIn || s.raids[key].LeaderID != leader.selectedCharacterID {
					t.Fatal("kick removed requester or failed to remove target")
				}
				for _, f := range splitUpperFrames(t, lc.write.Bytes()) {
					if f.msgID == 665 && len(f.body) > 0 && f.body[0] == 1 {
						t.Fatal("leader received membership-clearing success ACK")
					}
				}
				ack, _ := splitGameServerUpperPacket(t, pc.write.Bytes())
				if ack.Header.MsgID != 665 || !bytes.Equal(ack.Body, raid.BuildLeaveRaidResultBody(true)) {
					t.Fatal("target did not receive forced-removal notification")
				}
			} else if action == "self" || action == "zero_self" {
				if leaderIn || !peerIn {
					t.Fatal("voluntary exit semantics changed")
				}
			} else {
				after := make(map[uint32]runtimeRaidState)
				for key, value := range s.raids {
					after[key] = cloneRuntimeRaidState(*value)
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("invalid kick request changed raid state: before=%+v after=%+v", before, after)
				}
			}
		})
	}
}

func TestCurrentRaidKickTargetWriteFailureDoesNotFailLeader(t *testing.T) {
	s, leader, peer, _, _, state := prepareApprovedCurrentRaid(t)
	peer.conn = &r160FailWritesConn{fail: true}
	body := binary.LittleEndian.AppendUint16(nil, peer.selectedCharacterID)
	if err := s.handleGameCommand(leader, 1, 665, body); err != nil {
		t.Fatal("target's broken transport escaped into the leader event loop", err)
	}
	latest, leaderIn := s.runtimeRaidForMember(leader.selectedCharacterID)
	_, peerIn := s.runtimeRaidForMember(peer.selectedCharacterID)
	if !leaderIn || peerIn || latest.RaidKey != state.RaidKey || latest.LeaderID != leader.selectedCharacterID || !s.currentRaidAuthenticated(leader) {
		t.Fatal("committed kick changed the leader or retained the failed target")
	}
}
