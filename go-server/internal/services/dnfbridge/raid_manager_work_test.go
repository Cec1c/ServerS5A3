package dnfbridge

import (
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func encodeRaidManagerWorkBody(action, memberCharID, targetGroup uint32) []byte {
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint32(buf[0:4], action)
	binary.LittleEndian.PutUint32(buf[4:8], memberCharID)
	binary.LittleEndian.PutUint32(buf[8:12], targetGroup)
	return buf
}

func TestRaidManagerWorkDisbandSquadAndDelegateLeader(t *testing.T) {
	s, leader, member, _, _, state := prepareApprovedCurrentRaid(t)
	leaderID := selectedCharacterID(leader)
	memberID := selectedCharacterID(member)

	// 1. 先把成员编入 1 队
	if err := s.handleOnlineRaidManagerWork(leader, uint16(dnfenum.CmdPacketRaidManagerWork), encodeRaidManagerWorkBody(0, uint32(memberID), 1)); err != nil {
		t.Fatalf("assign member failed: %v", err)
	}
	snap, ok := s.runtimeRaidForMember(leaderID)
	if !ok {
		t.Fatalf("raid not found after assign")
	}
	if idx := runtimeRaidMemberIndex(snap, memberID); idx < 0 || snap.Members[idx].GroupIndex != 1 {
		t.Fatalf("expected member in group 1, got %+v", snap.Members)
	}

	// 2. 解散 1 队：客户端发送 Action=0, TargetGroup=0
	if err := s.handleOnlineRaidManagerWork(leader, uint16(dnfenum.CmdPacketRaidManagerWork), encodeRaidManagerWorkBody(0, uint32(memberID), 0)); err != nil {
		t.Fatalf("unassign member failed: %v", err)
	}
	snap, ok = s.runtimeRaidForMember(leaderID)
	if !ok {
		t.Fatalf("raid not found after unassign")
	}
	if idx := runtimeRaidMemberIndex(snap, memberID); idx < 0 || snap.Members[idx].GroupIndex != 0 {
		t.Fatalf("expected member unassigned to group 0, got %+v", snap.Members)
	}

	// 3. 委任团长：客户端发送 Action=1, MemberCharKey=memberID, TargetGroup=0
	if err := s.handleOnlineRaidManagerWork(leader, uint16(dnfenum.CmdPacketRaidManagerWork), encodeRaidManagerWorkBody(1, uint32(memberID), 0)); err != nil {
		t.Fatalf("delegate leader failed: %v", err)
	}
	snap, ok = s.runtimeRaidForMember(memberID)
	if !ok || snap.RaidKey != state.RaidKey {
		t.Fatalf("raid not found after delegate")
	}
	if snap.LeaderID != memberID {
		t.Fatalf("expected new leader=%d, got=%d", memberID, snap.LeaderID)
	}
}
