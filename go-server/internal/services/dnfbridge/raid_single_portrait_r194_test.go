package dnfbridge

import (
	"encoding/binary"
	"testing"
)

// The native PartyFace producer consumes 591 mode3 and its fallback renderer
// uses the category1 profiles populated by 752. A one-person roster needs both
// producers, including its own real namespace, before it can render remotely.
func TestR194SingleRaidPublishesSelfProfileBeforeParticipant(t *testing.T) {
	s, leader, _, conn, other, state := prepareApprovedCurrentRaid(t)
	for _, member := range state.Members {
		if member.CharID == leader.selectedCharacterID {
			state.Members = []runtimeRaidMemberState{member}
			break
		}
	}
	if len(state.Members) != 1 || state.Members[0].GroupIndex == 0 {
		t.Fatal("single fixture must keep its real nonzero group")
	}
	conn.write.Reset()
	other.write.Reset()
	if err := s.broadcastRuntimeRaidRefresh(state); err != nil {
		t.Fatal(err)
	}
	packets := splitAllGameServerUpperPackets(t, conn.write.Bytes())
	if len(packets) < 2 || packets[0].Header.MsgID != 752 || packets[1].Header.MsgID != 591 {
		t.Fatalf("self profile must precede participant roster: %+v", packets)
	}
	profile, roster := packets[0].Body, packets[1].Body
	if len(profile) != 61 || binary.LittleEndian.Uint32(profile) != 1 || binary.LittleEndian.Uint16(profile[4:]) != leader.selectedCharacterID {
		t.Fatalf("missing singleton self profile: %x", profile)
	}
	if len(roster) < 11 || binary.LittleEndian.Uint32(roster) != state.RaidKey || binary.LittleEndian.Uint32(roster[4:]) != 3 || roster[8] != 1 || binary.LittleEndian.Uint16(roster[9:]) != leader.selectedCharacterID {
		t.Fatalf("missing singleton participant: %x", roster)
	}
	if profile[7] != byte(state.RaidKey>>16) || profile[12] != byte(state.RaidKey>>24) || profile[7] == 0 {
		t.Fatalf("profile must retain the roster namespace: %x", profile)
	}
	if other.write.Len() != 0 {
		t.Fatal("singleton projection leaked to removed member")
	}
}
