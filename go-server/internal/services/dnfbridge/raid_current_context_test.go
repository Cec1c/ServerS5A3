package dnfbridge

import (
	"encoding/binary"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

// The actual CHANNELINFO parser stores field8 at global56A1E80 and field7
// at global56A1E74. Native 591 and 755 use channel first, server second.
// Keep the directory, raid key and applicant identity tied to that boundary.
func TestCurrentRaidContextsMatchActualChannelDirectory(t *testing.T) {
	for _, server := range []int{0, 7, 255} {
		s, a, b, _, _, state := prepareCurrentRaidApplicationTest(t)
		s.options.channelAdvertiseID = server
		a.channel.Port = 10023
		notice, err := s.buildCurrentChannelResidentNotice(a.channel, time.Unix(1_721_020_000, 0))
		if err != nil {
			t.Fatal(err)
		}
		fields, _ := consumeChannelInfoProto(t, notice[4:])
		channelContext, serverContext := fields[8][0], fields[7][0]
		if channelContext != 23 || serverContext != uint64(server) {
			t.Fatalf("actual directory contexts=%v", fields)
		}
		s.raidMu.Lock()
		key := s.nextRuntimeRaidKeyLocked(a, a.selectedCharacterID)
		s.raidMu.Unlock()
		if byte(key>>16) != byte(channelContext) || byte(key>>24) != byte(serverContext) {
			t.Fatalf("raid key %#x disagrees with directory contexts", key)
		}
		row, ok := s.currentRaidApplicantRecord(b, raid.CurrentOtherChannelJoinRequest{})
		body, err := raid.BuildCurrentRaidApplicationBody(row)
		if !ok || err != nil || body[4] != byte(channelContext) || body[5] != byte(serverContext) {
			t.Fatalf("applicant contexts=%x err=%v", body, err)
		}
		state.RaidKey = key
		if !s.currentRaidInfoVisible(a, state) {
			t.Fatal("directory-matching owner rejected")
		}
	}
}

func TestCurrentRaidRejectsHistoricalReversedOwnedCapture(t *testing.T) {
	s, a, b, ac, bc, _ := prepareCurrentRaidApplicationTest(t)
	body := []byte{0, 0, 0, 0, 0, 0, 0, 10, 0, 23}
	binary.LittleEndian.PutUint16(body[1:], a.selectedCharacterID)
	if err := s.handleGameCommand(b, 1, 820, body); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, bc.write.Bytes())
	if ack.Body[0] != 0 || ac.write.Len() != 0 || len(s.currentRaidApplications) != 0 {
		t.Fatal("reversed contexts admitted a player in another namespace")
	}
}
