package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
	"longheng.io/server/internal/modules/dnf/raid"
)

func currentRaidApplyFixture(target uint16) []byte {
	body := []byte{23, 0, 0, 0xff, 0xff, 0xff, 0xff, 10, 0, 0}
	binary.LittleEndian.PutUint16(body[1:], target)
	return body
}
func currentRaidApprovalFixture(target uint16) []byte {
	body := make([]byte, 7)
	binary.LittleEndian.PutUint16(body, target)
	body[2] = 10
	return body
}
func prepareCurrentRaidApplicationTest(t *testing.T) (*Service, *gameSession, *gameSession, *bufferConn, *bufferConn, runtimeRaidState) {
	t.Helper()
	s, a, b, ac, bc := prepareAccountFriendTest(t)
	a.channel = channelcatalog.Channel{ID: 23, Type: dnfenum.RaidChannelType}
	b.channel = a.channel
	if err := s.handleCurrentCreateRaid(a, 664, currentRaidRequestFixture(0, "raid", 0, 0, 0)); err != nil {
		t.Fatal(err)
	}
	state, _ := s.runtimeRaidForMember(a.selectedCharacterID)
	ac.write.Reset()
	bc.write.Reset()
	return s, a, b, ac, bc, state
}

func TestCurrentRaidApplicationActualDispatchRequiresApprovalAndJoinsOnlyOnce(t *testing.T) {
	s, a, b, ac, bc, before := prepareCurrentRaidApplicationTest(t)
	if err := s.handleGameCommand(b, 1, 820, currentRaidApplyFixture(a.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, bc.write.Bytes())
	notice, leaderRest := splitGameServerUpperPacket(t, ac.write.Bytes())
	if len(rest) != 0 || len(leaderRest) != 0 || ack.Header.MsgID != 820 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1}) || notice.Header.MsgID != 755 || notice.Header.Classification != 0 || len(notice.Body) != 51 || binary.LittleEndian.Uint16(notice.Body) != b.selectedCharacterID || notice.Body[4] != 23 || notice.Body[5] != 0 {
		t.Fatal("wrong native application projection")
	}
	row, _ := s.currentRaidApplicantRecord(b, raid.CurrentOtherChannelJoinRequest{})
	if binary.LittleEndian.Uint16(notice.Body[16:18]) != row.Level || uint32(row.Level) == binary.LittleEndian.Uint32(notice.Body[6:10]) {
		t.Fatal("untrusted statistic replaced real level")
	}
	if _, joined := s.runtimeRaidForMember(b.selectedCharacterID); joined {
		t.Fatal("application joined without approval")
	}
	if current, _ := s.runtimeRaidForMember(a.selectedCharacterID); len(current.Members) != len(before.Members) {
		t.Fatal("application changed existing membership")
	}
	ac.write.Reset()
	bc.write.Reset()
	if err := s.handleGameCommand(a, 1, 11, currentRaidApprovalFixture(b.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}
	after, joined := s.runtimeRaidForMember(b.selectedCharacterID)
	if !joined || after.RaidKey != before.RaidKey || len(after.Members) != 2 || len(s.currentRaidApplications) != 0 {
		t.Fatal("approved applicant not committed to actual raid")
	}
	ack, rest = splitGameServerUpperPacket(t, ac.write.Bytes())
	rest = consumeCurrentRaidProfiles(t, rest, after)
	leaderRoster, rest := splitGameServerUpperPacket(t, rest)
	metadata, appRest := splitGameServerUpperPacket(t, bc.write.Bytes())
	appRest = consumeCurrentRaidProfiles(t, appRest, after)
	appRoster, appRest := splitGameServerUpperPacket(t, appRest)
	wantAck := append([]byte{1}, party.BuildResponsePeerAckPayload(b.selectedCharacterID, 10)...)
	wantMetadata, _ := buildCurrentRuntimeRaidMetadata(after, 2)
	wantRoster, _ := s.buildRuntimeRaidRosterBody(after)
	if !bytes.Equal(ack.Body, wantAck) || len(rest) != 0 || len(appRest) != 0 || !bytes.Equal(metadata.Body, wantMetadata) || !bytes.Equal(appRoster.Body, wantRoster) || !bytes.Equal(leaderRoster.Body, wantRoster) {
		t.Fatal("approval did not project committed real roster")
	}
	ac.write.Reset()
	bc.write.Reset()
	if err := s.handleGameCommand(a, 1, 11, currentRaidApprovalFixture(b.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}
	duplicate, _ := splitGameServerUpperPacket(t, ac.write.Bytes())
	if duplicate.Body[0] != 0 || bc.write.Len() != 0 {
		t.Fatal("replay sent another successful approval")
	}
	after, _ = s.runtimeRaidForMember(b.selectedCharacterID)
	if len(after.Members) != 2 {
		t.Fatal("replay duplicated membership")
	}
}

func TestCurrentRaidApplicationRejectsUnownedOrUnavailableTargets(t *testing.T) {
	for _, kind := range []string{"unauth", "owner", "stale", "leader_offline", "foreign_server", "foreign_channel", "leader_channel", "self", "short", "trailing", "single", "started", "full", "already_member", "ordinary", "luke"} {
		t.Run(kind, func(t *testing.T) {
			s, a, b, ac, bc, state := prepareCurrentRaidApplicationTest(t)
			body := currentRaidApplyFixture(a.selectedCharacterID)
			switch kind {
			case "unauth":
				b.accountFromToken = false
			case "owner":
				b.accountID = "foreign"
			case "stale":
				s.unbindGameSession(b)
			case "leader_offline":
				s.unbindGameSession(a)
			case "foreign_server":
				body[9] = 1
			case "foreign_channel":
				body[0] = 24
			case "leader_channel":
				a.channel.ID = 24
			case "self":
				binary.LittleEndian.PutUint16(body[1:], b.selectedCharacterID)
			case "short":
				body = body[:9]
			case "trailing":
				body = append(body, 0)
			case "single":
				s.raids[state.RaidKey].CurrentUIFlags[0] = 1
			case "started":
				s.raids[state.RaidKey].Started = true
			case "full":
				for i := uint16(100); len(s.raids[state.RaidKey].Members) < 20; i++ {
					s.raids[state.RaidKey].Members = append(s.raids[state.RaidKey].Members, runtimeRaidMemberState{CharID: i})
				}
			case "already_member":
				s.raids[state.RaidKey].Members = append(s.raids[state.RaidKey].Members, runtimeRaidMemberState{CharID: b.selectedCharacterID})
			case "ordinary":
				b.channel.Type = 11
				a.channel.Type = 11
			case "luke":
				b.channel.Type = dnfenum.HiddenRaidType
				a.channel.Type = dnfenum.HiddenRaidType
			}
			if err := s.handleGameCommand(b, 1, 820, body); err != nil {
				t.Fatal(err)
			}
			ack, _ := splitGameServerUpperPacket(t, bc.write.Bytes())
			if ack.Body[0] != 0 || len(s.currentRaidApplications) != 0 || ac.write.Len() != 0 {
				t.Fatal("invalid application produced approval capability")
			}
		})
	}
}

func TestCurrentRaidApprovalRejectsChangedOwnersAndExpiredApplications(t *testing.T) {
	for _, kind := range []string{"missing", "expired", "applicant_stale", "leader_stale", "unauth", "wrong_owner", "started", "single", "full", "moved", "value", "wrong_target"} {
		t.Run(kind, func(t *testing.T) {
			s, a, b, ac, bc, state := prepareCurrentRaidApplicationTest(t)
			if err := s.handleCurrentRaidApplication(b, 820, raid.CurrentOtherChannelJoinRequest{TargetCharKey: a.selectedCharacterID, TargetChannel: 23}); err != nil {
				t.Fatal(err)
			}
			req := party.ResponsePeerRequest{TargetID: b.selectedCharacterID, Mode: 10}
			switch kind {
			case "missing":
				delete(s.currentRaidApplications, b.selectedCharacterID)
			case "expired":
				p := s.currentRaidApplications[b.selectedCharacterID]
				p.expires = time.Now().Add(-time.Second)
				s.currentRaidApplications[b.selectedCharacterID] = p
			case "applicant_stale":
				s.unbindGameSession(b)
			case "leader_stale":
				s.unbindGameSession(a)
			case "unauth":
				a.accountFromToken = false
			case "wrong_owner":
				a.accountID = "foreign"
			case "started":
				s.raids[state.RaidKey].Started = true
			case "single":
				s.raids[state.RaidKey].CurrentUIFlags[0] = 1
			case "full":
				for i := uint16(100); len(s.raids[state.RaidKey].Members) < 20; i++ {
					s.raids[state.RaidKey].Members = append(s.raids[state.RaidKey].Members, runtimeRaidMemberState{CharID: i})
				}
			case "moved":
				b.channel.ID = 24
			case "value":
				req.Value = 1
			case "wrong_target":
				req.TargetID = 999
			}
			ac.write.Reset()
			bc.write.Reset()
			if err := s.handleCurrentRaidApproval(a, 11, req); err != nil {
				t.Fatal(err)
			}
			ack, _ := splitGameServerUpperPacket(t, ac.write.Bytes())
			if ack.Body[0] != 0 || bc.write.Len() != 0 {
				t.Fatal("invalid approval sent successful projection")
			}
			if _, joined := s.runtimeRaidForMember(b.selectedCharacterID); joined {
				t.Fatal("invalid approval joined")
			}
		})
	}
}

func TestCurrentRaidRejoinCannotBypassApplication(t *testing.T) {
	s, a, b, _, _, state := prepareCurrentRaidApplicationTest(t)
	if _, ok := s.rejoinRuntimeRaid(state.RaidKey, b.selectedCharacterID); ok {
		t.Fatal("unapproved rejoin added member")
	}
	if _, ok := s.rejoinRuntimeRaid(state.RaidKey, a.selectedCharacterID); !ok {
		t.Fatal("existing member cannot restore")
	}
}
