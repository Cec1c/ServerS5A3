package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raid"
)

func prepareApprovedCurrentRaid(t *testing.T) (*Service, *gameSession, *gameSession, *bufferConn, *bufferConn, runtimeRaidState) {
	t.Helper()
	s, leader, member, lc, mc, _ := prepareCurrentRaidApplicationTest(t)
	if err := s.handleGameCommand(member, 1, 820, currentRaidApplyFixture(leader.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameCommand(leader, 1, 11, currentRaidApprovalFixture(member.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}
	state, found := s.runtimeRaidForMember(member.selectedCharacterID)
	if !found {
		t.Fatal("missing approved membership")
	}
	lc.write.Reset()
	mc.write.Reset()
	return s, leader, member, lc, mc, state
}

func TestCurrentRaidRetainedMemberQueryRestoresOnlyItsOwnClient(t *testing.T) {
	for _, asLeader := range []bool{false, true} {
		for _, selector := range []byte{1, 2} {
			s, leader, member, lc, mc, state := prepareApprovedCurrentRaid(t)
			old, peer, pc := member, leader, lc
			if asLeader {
				old, peer, pc = leader, member, mc
			}
			// A new authenticated connection binds the same repository owner;
			// its predecessor is no longer authorized to restore anything.
			conn := &bufferConn{}
			fresh := &gameSession{conn: conn, accountID: old.accountID, selectedCharacterID: old.selectedCharacterID, channel: old.channel, loginPacketSeen: true, accountFromToken: true}
			s.unbindGameSession(old)
			s.bindGameSessionCharacter(fresh, fresh.selectedCharacterID)
			req := []byte{selector, 0, 0, 0, 0}
			binary.LittleEndian.PutUint32(req[1:], state.RaidKey)
			wantInfo, _ := raid.BuildCurrentInfoReplyBody(currentRuntimeRaidMetadata(state), 2)
			wantRoster, _ := s.buildRuntimeRaidRosterBody(state)
			for repeat := 0; repeat < 2; repeat++ {
				conn.write.Reset()
				if err := s.handleOnlineRequestRaidInfo(fresh, req); err != nil {
					t.Fatal(err)
				}
				raw := consumeCurrentRaidProfiles(t, conn.write.Bytes(), state)
				ack, rest := splitGameServerUpperPacket(t, raw)
				roster, rest := splitGameServerUpperPacket(t, rest)
				if len(rest) != 0 || ack.Header.MsgID != 1359 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, wantInfo) || roster.Header.MsgID != 591 || roster.Header.Classification != 0 || !bytes.Equal(roster.Body, wantRoster) {
					t.Fatal("restored member requires complete object before own-raid roster")
				}
				if dir := os.Getenv("DNF90_CURRENT_RAID_RESTORE_NATIVE_DIR"); dir != "" && !asLeader && repeat == 0 && selector == 2 {
					for name, body := range map[string][]byte{"owned-info.bin": ack.Body, "owned-roster.bin": roster.Body} {
						if err := os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			after, _ := s.runtimeRaidForMember(fresh.selectedCharacterID)
			if !reflect.DeepEqual(state, after) || pc.write.Len() != 0 || selectedCharacterID(peer) == 0 {
				t.Fatal("query changed raid state or sent membership to another client")
			}
			old.conn.(*bufferConn).write.Reset()
			if err := s.handleOnlineRequestRaidInfo(old, req); err != nil {
				t.Fatal(err)
			}
			if old.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("retired connection restored membership")
			}
		}
	}
}

func TestCurrentRaidRetainedMembershipCannotBypassContextOrOwner(t *testing.T) {
	for _, kind := range []string{"channel", "server", "ordinary", "luke", "legacy_object", "wrong_account", "unauthenticated"} {
		t.Run(kind, func(t *testing.T) {
			s, _, member, _, mc, state := prepareApprovedCurrentRaid(t)
			switch kind {
			case "channel":
				member.channel.ID = 24
			case "server":
				s.options.channelAdvertiseID = 7
			case "ordinary":
				member.channel.Type = 11
			case "luke":
				member.channel.Type = dnfenum.HiddenRaidType
			case "legacy_object":
				s.raids[state.RaidKey].CurrentLayout = false
			case "wrong_account":
				member.accountID = "foreign"
			case "unauthenticated":
				member.accountFromToken = false
			}
			req := []byte{2, 0, 0, 0, 0}
			binary.LittleEndian.PutUint32(req[1:], state.RaidKey)
			if err := s.handleOnlineRequestRaidInfo(member, req); err != nil {
				t.Fatal(err)
			}
			if mc.write.Len() != 0 {
				t.Fatal("retained membership bypassed namespace/account authorization")
			}
		})
	}
}
