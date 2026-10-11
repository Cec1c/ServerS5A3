package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"errors"
	"longheng.io/server/internal/modules/dnf/party"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"sync"
	"testing"
	"time"
)

func prepareCurrentRaidInvitationTest(t *testing.T) (*Service, *gameSession, *gameSession, *bufferConn, *bufferConn, runtimeRaidState) {
	t.Helper()
	s, a, b, ac, bc, state := prepareCurrentRaidApplicationTest(t)
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: a.selectedCharacterID, TownID: 38, AreaID: 1, Session: a})
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: b.selectedCharacterID, TownID: 38, AreaID: 1, Session: b})
	return s, a, b, ac, bc, state
}
func currentRaidInviteFixture(target uint16) []byte {
	b := make([]byte, 13)
	binary.LittleEndian.PutUint16(b, target)
	b[2] = 10
	binary.LittleEndian.PutUint32(b[3:], 0x11223344)
	binary.LittleEndian.PutUint16(b[7:], 0x2345)
	return b
}
func TestCurrentRaidPeerCheckActualDispatch(t *testing.T) {
	s, a, b, ac, bc, _ := prepareCurrentRaidInvitationTest(t)
	body := []byte{23, 0, 0, byte(s.channelAdvertiseID())}
	binary.LittleEndian.PutUint16(body[1:], b.selectedCharacterID)
	if err := s.handleGameCommand(a, 1, 889, body); err != nil {
		t.Fatal(err)
	}
	if ac.write.Len() == 0 {
		t.Fatal("actual four-byte first-menu query received no ACK")
	}
	p, rest := splitGameServerUpperPacket(t, ac.write.Bytes())
	if p.Header.MsgID != 889 || p.Header.Classification != 1 || !bytes.Equal(p.Body, append([]byte{1}, make([]byte, 10)...)) || len(rest) != 0 || bc.write.Len() != 0 {
		t.Fatalf("wrong native first-menu ACK: %x", p.Body)
	}
}
func TestCurrentRaidInvitationActualDispatch(t *testing.T) {
	s, a, b, ac, bc, state := prepareCurrentRaidInvitationTest(t)
	body := currentRaidInviteFixture(b.selectedCharacterID)
	if err := s.handleGameCommand(a, 1, 10, body); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, ac.write.Bytes())
	if ack.Header.MsgID != 10 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1}) || len(rest) != 0 {
		t.Fatalf("real leader invite rejected/malformed: %x", ack.Body)
	}
	p, rest := splitGameServerUpperPacket(t, bc.write.Bytes())
	want := append([]byte(nil), body...)
	binary.LittleEndian.PutUint16(want, a.selectedCharacterID)
	if p.Header.MsgID != 7 || p.Header.Classification != 0 || !bytes.Equal(p.Body, want) || len(rest) != 0 {
		t.Fatalf("wrong actual recipient op7 mode10 layout: %x", p.Body)
	}
	if _, joined := s.runtimeRaidForMember(b.selectedCharacterID); joined {
		t.Fatal("invitation itself joined recipient")
	}
	ac.write.Reset()
	bc.write.Reset()
	if err := s.handleGameCommand(b, 1, 11, currentRaidApprovalFixture(a.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}
	after, joined := s.runtimeRaidForMember(b.selectedCharacterID)
	if !joined || after.RaidKey != state.RaidKey || len(after.Members) != 2 {
		t.Fatal("recipient acceptance did not join authoritative raid")
	}
	ack, rest = splitGameServerUpperPacket(t, bc.write.Bytes())
	wantAck := append([]byte{1}, party.BuildResponsePeerAckPayload(a.selectedCharacterID, 10)...)
	metadata, rest := splitGameServerUpperPacket(t, rest)
	rest = consumeCurrentRaidProfiles(t, rest, after)
	roster, rest := splitGameServerUpperPacket(t, rest)
	leaderRest := consumeCurrentRaidProfiles(t, ac.write.Bytes(), after)
	leaderRoster, leaderRest := splitGameServerUpperPacket(t, leaderRest)
	wantMetadata, _ := buildCurrentRuntimeRaidMetadata(after, 2)
	wantRoster, _ := s.buildRuntimeRaidRosterBody(after)
	if ack.Header.MsgID != 11 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, wantAck) || metadata.Header.MsgID != 591 || !bytes.Equal(metadata.Body, wantMetadata) || !bytes.Equal(roster.Body, wantRoster) || !bytes.Equal(leaderRoster.Body, wantRoster) || len(rest) != 0 || len(leaderRest) != 0 || len(s.currentRaidInvitations) != 0 || len(s.currentRaidApplications) != 0 {
		t.Fatal("acceptance did not emit exact committed metadata/rosters")
	}
	ac.write.Reset()
	bc.write.Reset()
	if err := s.handleGameCommand(b, 1, 11, currentRaidApprovalFixture(a.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}
	fail, tail := splitGameServerUpperPacket(t, bc.write.Bytes())
	if fail.Body[0] != 0 || len(tail) != 0 || ac.write.Len() != 0 {
		t.Fatal("replayed acceptance projected success")
	}
	replay, _ := s.runtimeRaidForMember(b.selectedCharacterID)
	if len(replay.Members) != 2 {
		t.Fatal("replay duplicated membership")
	}
}

func assertCurrentRaidFailureOnly(t *testing.T, c *bufferConn, op uint16) {
	t.Helper()
	p, rest := splitGameServerUpperPacket(t, c.write.Bytes())
	if p.Header.MsgID != op || !bytes.Equal(p.Body, []byte{0, 3}) || len(rest) != 0 {
		t.Fatalf("expected truthful failure op%d, got %d %x tail%d", op, p.Header.MsgID, p.Body, len(rest))
	}
}
func TestCurrentRaidPeerCheckRejectsUnverifiedTargets(t *testing.T) {
	for _, kind := range []string{"short", "trailing", "foreign_server", "foreign_channel", "own_target", "offline", "target_owner", "source_owner", "source_unbound", "target_unbound", "target_raid", "channel", "scene", "presence_session", "ordinary"} {
		t.Run(kind, func(t *testing.T) {
			s, a, b, ac, bc, state := prepareCurrentRaidInvitationTest(t)
			body := []byte{23, 0, 0, byte(s.channelAdvertiseID())}
			binary.LittleEndian.PutUint16(body[1:], b.selectedCharacterID)
			switch kind {
			case "short":
				body = body[:3]
			case "trailing":
				body = append(body, 0)
			case "foreign_server":
				body[3]++
			case "foreign_channel":
				body[0] = 24
			case "own_target":
				binary.LittleEndian.PutUint16(body[1:], a.selectedCharacterID)
			case "offline":
				binary.LittleEndian.PutUint16(body[1:], 999)
			case "target_owner":
				b.accountID = "foreign"
			case "source_owner":
				a.accountID = "foreign"
			case "source_unbound":
				s.unbindGameSession(a)
			case "target_unbound":
				s.unbindGameSession(b)
			case "target_raid":
				s.raids[state.RaidKey].Members = append(s.raids[state.RaidKey].Members, runtimeRaidMemberState{CharID: b.selectedCharacterID})
			case "channel":
				b.channel.ID = 24
			case "scene":
				s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: b.selectedCharacterID, TownID: 38, AreaID: 2, Session: b})
			case "presence_session":
				s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: b.selectedCharacterID, TownID: 38, AreaID: 1, ChannelID: 23, Session: &gameSession{}})
			case "ordinary":
				a.channel.Type = 11
				b.channel.Type = 11
			}
			if err := s.handleGameCommand(a, 1, 889, body); err != nil {
				t.Fatal(err)
			}
			assertCurrentRaidFailureOnly(t, ac, 889)
			if bc.write.Len() != 0 {
				t.Fatal("query sent target notification")
			}
		})
	}
}
func TestCurrentRaidInvitationRejectsUnsafeRequests(t *testing.T) {
	for _, kind := range []string{"single", "started", "full", "not_leader", "already_member", "target_offline", "source_owner", "target_owner", "source_stale", "target_stale", "channel", "scene", "presence_session", "self", "tail", "short", "trailing", "ordinary"} {
		t.Run(kind, func(t *testing.T) {
			s, a, b, ac, bc, state := prepareCurrentRaidInvitationTest(t)
			body := currentRaidInviteFixture(b.selectedCharacterID)
			switch kind {
			case "single":
				s.raids[state.RaidKey].CurrentUIFlags[0] = 1
			case "started":
				s.raids[state.RaidKey].Started = true
			case "full":
				fillCurrentRaidMembers(s, state.RaidKey, 20)
			case "not_leader":
				s.raids[state.RaidKey].LeaderID = 999
			case "already_member":
				s.raids[state.RaidKey].Members = append(s.raids[state.RaidKey].Members, runtimeRaidMemberState{CharID: b.selectedCharacterID})
			case "target_offline":
				binary.LittleEndian.PutUint16(body, 999)
			case "source_owner":
				a.accountID = "foreign"
			case "target_owner":
				b.accountID = "foreign"
			case "source_stale":
				s.unbindGameSession(a)
			case "target_stale":
				s.unbindGameSession(b)
			case "channel":
				b.channel.ID = 24
			case "scene":
				s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: b.selectedCharacterID, TownID: 38, AreaID: 2, Session: b})
			case "presence_session":
				s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: b.selectedCharacterID, TownID: 38, AreaID: 1, ChannelID: 23, Session: &gameSession{}})
			case "self":
				binary.LittleEndian.PutUint16(body, a.selectedCharacterID)
			case "tail":
				body[9] = 1
			case "short":
				body = body[:12]
			case "trailing":
				body = append(body, 0)
			case "ordinary":
				a.channel.Type = 11
				b.channel.Type = 11
			}
			if err := s.handleGameCommand(a, 1, 10, body); err != nil {
				t.Fatal(err)
			}
			assertCurrentRaidFailureOnly(t, ac, 10)
			if bc.write.Len() != 0 || len(s.currentRaidInvitations) != 0 || len(s.currentRaidApplications) != 0 {
				t.Fatal("invalid invitation created capability")
			}
		})
	}
}
func fillCurrentRaidMembers(s *Service, key uint32, count int) {
	for id := uint16(100); len(s.raids[key].Members) < count; id++ {
		s.raids[key].Members = append(s.raids[key].Members, runtimeRaidMemberState{CharID: id})
	}
}
func TestCurrentRaidInvitationAcceptanceRejectsChangedCapabilities(t *testing.T) {
	for _, kind := range []string{"missing", "expired", "source_offline", "target_offline", "source_owner", "target_owner", "recipient_changed_character", "inviter_changed_character", "recipient_generation", "inviter_generation", "recipient_replacement", "inviter_replacement", "wrong_target", "unknown_value", "single", "started", "full", "lost_leader", "lost_raid", "raid_replaced", "channel", "scene", "presence_session", "joined_elsewhere"} {
		t.Run(kind, func(t *testing.T) {
			s, a, b, ac, bc, state := prepareCurrentRaidInvitationTest(t)
			if err := s.handleGameCommand(a, 1, 10, currentRaidInviteFixture(b.selectedCharacterID)); err != nil {
				t.Fatal(err)
			}
			if len(s.currentRaidInvitations) != 1 {
				t.Fatal("missing fixture capability")
			}
			body := currentRaidApprovalFixture(a.selectedCharacterID)
			switch kind {
			case "missing":
				delete(s.currentRaidInvitations, b.selectedCharacterID)
			case "expired":
				pending := s.currentRaidInvitations[b.selectedCharacterID]
				pending.expires = time.Now().Add(-time.Second)
				s.currentRaidInvitations[b.selectedCharacterID] = pending
			case "source_offline":
				s.unbindGameSession(a)
			case "target_offline":
				s.unbindGameSession(b)
			case "source_owner":
				a.accountID = "foreign"
			case "target_owner":
				b.accountID = "foreign"
			case "recipient_changed_character":
				s.bindGameSessionCharacter(b, 8)
			case "inviter_changed_character":
				s.bindGameSessionCharacter(a, 8)
			case "recipient_replacement":
				s.unbindGameSession(b)
				replacementConn := &bufferConn{}
				replacement := &gameSession{conn: replacementConn, connID: "recipient-replacement", channel: b.channel, accountID: b.accountID, loginPacketSeen: true, accountFromToken: true}
				s.bindGameSessionCharacter(replacement, b.selectedCharacterID)
				s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: b.selectedCharacterID, TownID: 38, AreaID: 1, Session: replacement})
				b = replacement
				bc = replacementConn
			case "inviter_replacement":
				s.unbindGameSession(a)
				replacement := &gameSession{conn: &bufferConn{}, connID: "inviter-replacement", channel: a.channel, accountID: a.accountID, loginPacketSeen: true, accountFromToken: true}
				s.bindGameSessionCharacter(replacement, a.selectedCharacterID)
				s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: a.selectedCharacterID, TownID: 38, AreaID: 1, Session: replacement})
			case "recipient_generation":
				s.mu.Lock()
				s.allocateGameSessionCharacterGenerationLocked(b, nil)
				s.mu.Unlock()
			case "inviter_generation":
				s.mu.Lock()
				s.allocateGameSessionCharacterGenerationLocked(a, nil)
				s.mu.Unlock()
			case "wrong_target":
				binary.LittleEndian.PutUint16(body, 999)
			case "unknown_value":
				binary.LittleEndian.PutUint32(body[3:], 1)
			case "single":
				s.raids[state.RaidKey].CurrentUIFlags[0] = 1
			case "started":
				s.raids[state.RaidKey].Started = true
			case "full":
				fillCurrentRaidMembers(s, state.RaidKey, 20)
			case "lost_leader":
				s.raids[state.RaidKey].LeaderID = 999
			case "lost_raid":
				delete(s.raids, state.RaidKey)
			case "raid_replaced":
				replaced := cloneRuntimeRaidState(*s.raids[state.RaidKey])
				s.raids[state.RaidKey] = &replaced
			case "channel":
				b.channel.ID = 24
			case "scene":
				s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: b.selectedCharacterID, TownID: 38, AreaID: 2, Session: b})
			case "presence_session":
				s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: b.selectedCharacterID, TownID: 38, AreaID: 1, ChannelID: 23, Session: &gameSession{}})
			case "joined_elsewhere":
				s.raids[state.RaidKey+1] = &runtimeRaidState{RaidKey: state.RaidKey + 1, Members: []runtimeRaidMemberState{{CharID: b.selectedCharacterID}}}
			}
			ac.write.Reset()
			bc.write.Reset()
			if err := s.handleGameCommand(b, 1, 11, body); err != nil {
				t.Fatal(err)
			}
			assertCurrentRaidFailureOnly(t, bc, 11)
			if ac.write.Len() != 0 {
				t.Fatal("invalid acceptance notified inviter")
			}
			if current := s.raids[state.RaidKey]; current != nil {
				for _, m := range current.Members {
					if m.CharID == b.selectedCharacterID {
						t.Fatal("invalid acceptance added member")
					}
				}
			}
			if kind == "unknown_value" && len(s.currentRaidInvitations) != 1 {
				t.Fatal("unknown value consumed valid invite")
			}
		})
	}
}
func TestCurrentRaidInvitationLastSlotConcurrentAcceptance(t *testing.T) {
	s, a, b, ac, bc, state := prepareCurrentRaidInvitationTest(t)
	cc := &bufferConn{}
	c := &gameSession{conn: cc, connID: "raid-invite-third", channel: a.channel, accountID: b.accountID, loginPacketSeen: true, accountFromToken: true}
	s.bindGameSessionCharacter(c, 8)
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 8, TownID: 38, AreaID: 1, Session: c})
	for _, target := range []*gameSession{b, c} {
		if err := s.handleGameCommand(a, 1, 10, currentRaidInviteFixture(target.selectedCharacterID)); err != nil {
			t.Fatal(err)
		}
	}
	fillCurrentRaidMembers(s, state.RaidKey, 19)
	repos, _ := s.repositoryGroup()
	for _, member := range s.raids[state.RaidKey].Members {
		if member.CharID >= 100 {
			id := strconv.Itoa(int(member.CharID))
			if err := repos.Character.Save(nil, dnfrepo.CharacterRecord{CharacterID: id, AccountID: a.accountID, Name: "fixture" + id, Level: 85, Job: "0"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	ac.write.Reset()
	bc.write.Reset()
	cc.write.Reset()
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, recipient := range []*gameSession{b, c} {
		wg.Add(1)
		go func(r *gameSession) {
			defer wg.Done()
			<-start
			errs <- s.handleGameCommand(r, 1, 11, currentRaidApprovalFixture(a.selectedCharacterID))
		}(recipient)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	after, _ := s.runtimeRaidForMember(a.selectedCharacterID)
	count := 0
	for _, m := range after.Members {
		if m.CharID == b.selectedCharacterID || m.CharID == 8 {
			count++
		}
	}
	if len(after.Members) != 20 || count != 1 || len(s.currentRaidInvitations) != 1 {
		t.Fatal("last-slot competition admitted more than one recipient")
	}
	successes := 0
	failures := 0
	for _, conn := range []*bufferConn{bc, cc} {
		p, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if p.Header.MsgID != 11 {
			t.Fatal("missing acceptance response")
		}
		if p.Body[0] == 1 {
			successes++
			metadata, tail := splitGameServerUpperPacket(t, rest)
			want, _ := buildCurrentRuntimeRaidMetadata(after, 2)
			if metadata.Header.MsgID != 591 || !bytes.Equal(metadata.Body, want) {
				t.Fatal("winner metadata wrong")
			}
			tail = consumeCurrentRaidProfiles(t, tail, after)
			roster, tail := splitGameServerUpperPacket(t, tail)
			wantRoster, _ := s.buildRuntimeRaidRosterBody(after)
			if !bytes.Equal(roster.Body, wantRoster) || len(tail) != 0 {
				t.Fatal("winner roster wrong")
			}
		} else {
			failures++
			if !bytes.Equal(p.Body, []byte{0, 3}) || len(rest) != 0 {
				t.Fatal("loser projected membership")
			}
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatal("capacity competition replies not exact")
	}
}

type currentRaidInviteUnavailableConn struct{ *bufferConn }

func (c *currentRaidInviteUnavailableConn) Write(body []byte) (int, error) {
	return 0, errCurrentRaidInviteUnavailable
}

var errCurrentRaidInviteUnavailable = errors.New("fixture target connection unavailable")

func TestCurrentRaidInvitationNotificationFailureRollsBackOnlyPending(t *testing.T) {
	s, a, b, ac, bc, state := prepareCurrentRaidInvitationTest(t)
	b.conn = &currentRaidInviteUnavailableConn{bc}
	if err := s.handleGameCommand(a, 1, 10, currentRaidInviteFixture(b.selectedCharacterID)); err == nil {
		t.Fatal("unavailable recipient notice succeeded")
	}
	current, _ := s.runtimeRaidForMember(a.selectedCharacterID)
	if len(s.currentRaidInvitations) != 0 || len(s.currentRaidApplications) != 0 || len(current.Members) != len(state.Members) || ac.write.Len() != 0 || bc.write.Len() != 0 {
		t.Fatal("failed delivery committed invite or membership/success ACK")
	}
}
func TestCurrentRaidInvitationUnknownValueCanRetryActualAcceptance(t *testing.T) {
	s, a, b, ac, bc, _ := prepareCurrentRaidInvitationTest(t)
	if err := s.handleGameCommand(a, 1, 10, currentRaidInviteFixture(b.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}
	body := currentRaidApprovalFixture(a.selectedCharacterID)
	binary.LittleEndian.PutUint32(body[3:], 1)
	ac.write.Reset()
	bc.write.Reset()
	if err := s.handleGameCommand(b, 1, 11, body); err != nil {
		t.Fatal(err)
	}
	assertCurrentRaidFailureOnly(t, bc, 11)
	if len(s.currentRaidInvitations) != 1 || ac.write.Len() != 0 {
		t.Fatal("unknown value consumed invitation")
	}
	bc.write.Reset()
	binary.LittleEndian.PutUint32(body[3:], 0)
	if err := s.handleGameCommand(b, 1, 11, body); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, bc.write.Bytes())
	state, joined := s.runtimeRaidForMember(b.selectedCharacterID)
	if !joined || len(state.Members) != 2 || ack.Header.MsgID != 11 || !bytes.Equal(ack.Body, append([]byte{1}, party.BuildResponsePeerAckPayload(a.selectedCharacterID, 10)...)) || len(s.currentRaidInvitations) != 0 {
		t.Fatal("valid accepting value could not consume still-valid invitation")
	}
}
