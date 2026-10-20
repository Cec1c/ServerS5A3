package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestCurrentRaidVoluntaryLeaveRefreshesSuccessorBeforeRoster(t *testing.T) {
	s, leader, member, lc, mc, before := prepareApprovedCurrentRaid(t)
	leave := []byte{0, 0}
	if err := s.handleGameCommand(leader, 1, 665, leave); err != nil {
		t.Fatal(err)
	}
	if _, found := s.runtimeRaidForMember(leader.selectedCharacterID); found {
		t.Fatal("departed leader retained membership")
	}
	state, found := s.runtimeRaidForMember(member.selectedCharacterID)
	if !found || state.RaidKey != before.RaidKey || state.LeaderID != member.selectedCharacterID || len(state.Members) != 1 {
		t.Fatal("successor did not receive the surviving raid", state)
	}
	ack, _ := splitGameServerUpperPacket(t, lc.write.Bytes())
	if ack.Header.MsgID != 665 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1, 0}) {
		t.Fatal("voluntary leader leave produced forced removal", ack)
	}
	raw := mc.write.Bytes()
	metadata, rest := splitGameServerUpperPacket(t, raw)
	// Removing the departing character also publishes the ordinary party
	// manager's snapshot. The attack-party owner must still precede 752/591.
	for metadata.Header.MsgID == 9 {
		metadata, rest = splitGameServerUpperPacket(t, rest)
	}
	want, err := buildCurrentRuntimeRaidMetadata(state, 2)
	if err != nil || metadata.Header.MsgID != 591 || metadata.Header.Classification != 0 || !bytes.Equal(metadata.Body, want) || binary.LittleEndian.Uint32(metadata.Body[4:8]) != 2 {
		t.Fatal("successor metadata must precede roster", metadata)
	}
	rest = consumeCurrentRaidProfiles(t, rest, state)
	roster, rest := splitGameServerUpperPacket(t, rest)
	wantRoster, _ := s.buildRuntimeRaidRosterBody(state)
	if len(rest) != 0 || roster.Header.MsgID != 591 || !bytes.Equal(roster.Body, wantRoster) {
		t.Fatal("successor roster missing or out of order")
	}
	mc.write.Reset()
	if err := s.handleGameCommand(member, 1, 665, leave); err != nil {
		t.Fatal(err)
	}
	ack, _ = splitGameServerUpperPacket(t, mc.write.Bytes())
	if len(s.raids) != 0 || !bytes.Equal(ack.Body, []byte{1, 0}) {
		t.Fatal("last voluntary exit must remove directory without forced warning", ack)
	}
	if dir := os.Getenv("DNF90_CURRENT_RAID_LEAVE_NATIVE_DIR"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, "voluntary-last-leave.bin"), ack.Body, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "successor-metadata.bin"), metadata.Body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// A repeated command cannot recreate the ended raid.
	mc.write.Reset()
	if err := s.handleGameCommand(member, 1, 665, leave); err != nil {
		t.Fatal(err)
	}
	ack, _ = splitGameServerUpperPacket(t, mc.write.Bytes())
	if len(s.raids) != 0 || ack.Body[0] != 0 {
		t.Fatal("repeated leave recreated membership")
	}
}

func TestCurrentRaidLeaveDoesNotPushLeadershipAcrossContext(t *testing.T) {
	for _, kind := range []string{"channel", "server", "account", "retired"} {
		t.Run(kind, func(t *testing.T) {
			s, leader, member, _, mc, _ := prepareApprovedCurrentRaid(t)
			switch kind {
			case "channel":
				member.channel.ID++
			case "server":
				s.options.channelAdvertiseID++
			case "account":
				member.accountID = "foreign"
			case "retired":
				s.unbindGameSession(member)
			}
			if err := s.handleGameCommand(leader, 1, 665, []byte{0, 0}); err != nil {
				t.Fatal(err)
			}
			raw := mc.write.Bytes()
			for len(raw) != 0 {
				packet, rest := splitGameServerUpperPacket(t, raw)
				if packet.Header.MsgID == 591 && len(packet.Body) >= 8 && binary.LittleEndian.Uint32(packet.Body[4:8]) == 2 {
					t.Fatal("leadership metadata crossed authenticated context")
				}
				raw = rest
			}
		})
	}
}
