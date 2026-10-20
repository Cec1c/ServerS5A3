package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func raidChatRequestFixture(typ byte, source uint16) []byte {
	// 19:15 public-v95 requests use type 55/56, selected sender u16,
	// zero u32 and a single message DSTR. Test text replaces player text.
	var w packetWriter
	w.writeByte(typ)
	w.writeUint16(source)
	w.writeUint32(0)
	w.writeRawDstr([]byte("hello"))
	return w.bytes()
}

func TestCurrentRaidChatMemberAndLeaderEchoOnlyRealRaidRecipients(t *testing.T) {
	for _, typ := range []byte{55, 56} {
		s, leader, member, lc, mc, state := prepareApprovedCurrentRaid(t)
		source, sourceConn, peerConn := member, mc, lc
		if typ == 56 {
			source, sourceConn, peerConn = leader, lc, mc
		}
		// A raid-wide message must reach members in separate scenes, with a
		// name in the packet rather than a dependency on town actor lookup.
		s.onlinePlayers.LeaveArea(member.selectedCharacterID)
		if err := s.handleCurrentPeerChat(source, 17, raidChatRequestFixture(typ, source.selectedCharacterID)); err != nil {
			t.Fatal(err)
		}
		ack, rest := splitGameServerUpperPacket(t, sourceConn.write.Bytes())
		echo, rest := splitGameServerUpperPacket(t, rest)
		peer, trailing := splitGameServerUpperPacket(t, peerConn.write.Bytes())
		name := state.Members[runtimeRaidMemberIndex(state, source.selectedCharacterID)].Name
		want := buildCurrentRaidChatNoticeBody(typ, 0, 23, source.selectedCharacterID, rosterNameBytes(name), []byte("hello"))
		if ack.Header.MsgID != 17 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1}) ||
			echo.Header.MsgID != 118 || echo.Header.Classification != 0 || !bytes.Equal(echo.Body, want) ||
			peer.Header.MsgID != 118 || !bytes.Equal(peer.Body, want) || len(rest) != 0 || len(trailing) != 0 {
			t.Fatal("missing sender echo or native raid notice")
		}
		if dir := os.Getenv("DNFBRIDGE_RAID_CHAT_NATIVE_EXPORT"); dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, map[byte]string{55: "raid-member.bin", 56: "raid-leader.bin"}[typ]), echo.Body, 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestCurrentRaidChatRejectsNonmemberNonleaderSpoofAndStaleOwner(t *testing.T) {
	for _, mode := range []string{"nonleader", "nonmember", "spoof", "unknown", "empty", "stale", "channel"} {
		t.Run(mode, func(t *testing.T) {
			s, leader, member, lc, mc, _ := prepareApprovedCurrentRaid(t)
			body := raidChatRequestFixture(55, member.selectedCharacterID)
			switch mode {
			case "nonleader":
				body[0] = 56
			case "nonmember":
				s.removeRuntimeRaidMember(member.selectedCharacterID)
			case "spoof":
				binary.LittleEndian.PutUint16(body[1:], leader.selectedCharacterID)
			case "unknown":
				body[3] = 1
			case "empty":
				body = body[:11]
				binary.LittleEndian.PutUint32(body[7:], 0)
			case "stale":
				s.unbindGameSession(member)
			case "channel":
				member.channel.ID = 24
			}
			if err := s.handleCurrentPeerChat(member, 17, body); err != nil {
				t.Fatal(err)
			}
			ack, trailing := splitGameServerUpperPacket(t, mc.write.Bytes())
			if !bytes.Equal(ack.Body, []byte{0, 1}) || len(trailing) != 0 || lc.write.Len() != 0 {
				t.Fatal("unauthorized raid chat forwarded")
			}
		})
	}
}

func TestCurrentRaidChatDoesNotForwardToMemberInAnotherChannel(t *testing.T) {
	s, leader, member, lc, mc, _ := prepareApprovedCurrentRaid(t)
	member.channel.ID = 24
	if err := s.handleCurrentPeerChat(leader, 17, raidChatRequestFixture(56, leader.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}
	_, rest := splitGameServerUpperPacket(t, lc.write.Bytes())
	_, rest = splitGameServerUpperPacket(t, rest)
	if mc.write.Len() != 0 || len(rest) != 0 {
		t.Fatal("raid text leaked across channel ownership")
	}
}
