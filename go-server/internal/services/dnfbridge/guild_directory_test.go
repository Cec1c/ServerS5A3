package dnfbridge

import (
	"bytes"
	"context"
	"strconv"
	"testing"
)

// Directory IDs and persisted social-group IDs have separate native fields.
// Enabling a selectable guild region must not silently migrate player data or
// relax the existing cross-server identity check.
func TestGuildDirectoryChangePreservesSocialIdentity(t *testing.T) {
	s, a, b, ac, _ := prepareUserChannelTest(t)
	s.options.channelAdvertiseID = 1
	for _, group := range []byte{0, 1} {
		ac.write.Reset()
		handled, err := s.handleCurrentUserChannel(a, 804, 1, officialFriendRequest(291, 0, group, "来来来"))
		if !handled || err != nil {
			t.Fatalf("handled=%v err=%v", handled, err)
		}
		packet, rest := splitGameServerUpperPacket(t, ac.write.Bytes())
		var expected packetWriter
		if group == 0 {
			expected.writeByte(1)
			expected.writeByte(0)
			expected.writeRawDstr(rosterNameBytes("来来来"))
			expected.writeByte(1)
			expected.writeByte(byte(b.channel.ID))
		} else {
			expected.writeBytes([]byte{0, 0})
		}
		if len(rest) != 0 || !bytes.Equal(packet.Body, expected.bytes()) {
			t.Fatalf("group%d reply=%x", group, packet.Body)
		}
	}
	g, _ := s.repositoryGroup()
	for _, id := range []uint16{a.selectedCharacterID, b.selectedCharacterID} {
		c, found, err := g.Character.Load(context.Background(), strconv.Itoa(int(id)))
		if err != nil || !found || c.Stats["server_group_id"] != 0 {
			t.Fatalf("changed group: %+v %v", c, err)
		}
	}
}

func TestGuildLoginProjectionWaitsForNativeReadyBoundary(t *testing.T) {
	s, p, _ := guildFoundationFixture(t)
	if err := s.sendCurrentInitialTownGuildStateProjectionLocked(p[0], "pre_ready"); err != nil {
		t.Fatal(err)
	}
	if p[0].conn.(*bufferConn).write.Len() != 0 || p[0].initialTownGuildStateProjectionSent {
		t.Fatal("guild state escaped HUD-ready boundary")
	}
	p[0].initialTownLegacySceneReadyAccepted = true
	if err := s.sendCurrentInitialTownGuildStateProjectionLocked(p[0], "ready"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(guildReply(t, p[0], 308), []byte{0}) {
		t.Fatal("nonmember projected membership")
	}
}
