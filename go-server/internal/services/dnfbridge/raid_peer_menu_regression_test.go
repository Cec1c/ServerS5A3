package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestRaidPeerMenuSoloCanInspectRecruitingLeader(t *testing.T) {
	s, leader, solo, lc, sc, state := prepareCurrentRaidInvitationTest(t)
	request := []byte{23, 0, 0, byte(s.channelAdvertiseID())}
	binary.LittleEndian.PutUint16(request[1:3], leader.selectedCharacterID)
	if err := s.handleGameCommand(solo, 1, 889, request); err != nil {
		t.Fatal(err)
	}
	p, rest := splitGameServerUpperPacket(t, sc.write.Bytes())
	if p.Header.MsgID != 889 || p.Header.Classification != 1 || len(p.Body) != 11 || p.Body[0] != 1 || binary.LittleEndian.Uint32(p.Body[1:5]) != state.RaidKey || binary.LittleEndian.Uint16(p.Body[5:7]) != leader.selectedCharacterID || len(rest) != 0 {
		t.Fatalf("native target raid cache omitted: %x", p.Body)
	}
	if p.Body[7] != byte(len(state.Members)) || !bytes.Equal(p.Body[8:], make([]byte, 3)) {
		t.Fatalf("native join click requires authoritative nonzero member count: %x", p.Body)
	}
	if lc.write.Len() != 0 {
		t.Fatal("menu lookup notified the target")
	}
	if _, joined := s.runtimeRaidForMember(solo.selectedCharacterID); joined {
		t.Fatal("menu lookup admitted requester")
	}
}

func TestRaidPeerMenuTwoSolosCannotInviteToNonexistentRaid(t *testing.T) {
	s, a, b, ac, bc, state := prepareCurrentRaidInvitationTest(t)
	delete(s.raids, state.RaidKey)
	request := []byte{23, 0, 0, byte(s.channelAdvertiseID())}
	binary.LittleEndian.PutUint16(request[1:3], b.selectedCharacterID)
	if err := s.handleGameCommand(a, 1, 889, request); err != nil {
		t.Fatal(err)
	}
	assertCurrentRaidFailureOnly(t, ac, 889)
	if bc.write.Len() != 0 {
		t.Fatal("unassigned requester generated an invitation")
	}
}

func TestRaidPeerMenuSameRaidReturnsExistingKey(t *testing.T) {
	s, a, b, ac, _, state := prepareCurrentRaidInvitationTest(t)
	s.raids[state.RaidKey].Members = append(s.raids[state.RaidKey].Members, runtimeRaidMemberState{CharID: b.selectedCharacterID})
	request := []byte{23, 0, 0, byte(s.channelAdvertiseID())}
	binary.LittleEndian.PutUint16(request[1:3], b.selectedCharacterID)
	if err := s.handleGameCommand(a, 1, 889, request); err != nil {
		t.Fatal(err)
	}
	p, rest := splitGameServerUpperPacket(t, ac.write.Bytes())
	if len(p.Body) != 11 || p.Body[0] != 1 || binary.LittleEndian.Uint32(p.Body[1:5]) != state.RaidKey || p.Body[7] != 2 || !bytes.Equal(p.Body[8:], make([]byte, 3)) || len(rest) != 0 {
		t.Fatalf("existing raid query=%x", p.Body)
	}
}
