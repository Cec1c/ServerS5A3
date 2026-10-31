package dnfbridge

import (
	"bytes"
	"strings"
	"testing"
)

// The exact-client companion enables shared NOTI65. Verify the existing
// private whisper route obeys that reader's 0x100-byte message capacity too.
func TestGuildChatCompanionWhisperNativeMessageCapacity(t *testing.T) {
	for _, message := range []string{strings.Repeat("a", 255), strings.Repeat("a", 256), "hello\x00hidden"} {
		s, source, _, sourceConn, targetConn := newCurrentSocialPeerTest(t)
		s.options.currentClientVersion = "official-24.9"
		var w packetWriter
		w.writeByte(0x24)
		w.writeUint16(0)
		w.writeUint32(0)
		w.writeRawDstr([]byte(message))
		w.writeRawDstr(rosterNameBytes("来来来"))
		w.writeByte(0) // Authoritative character group of this fixture.
		if err := s.handleCurrentPeerChat(source, 17, w.bytes()); err != nil {
			t.Fatal(err)
		}
		packet, rest := splitGameServerUpperPacket(t, sourceConn.write.Bytes())
		if len(message) == 255 {
			if !bytes.Equal(packet.Body, []byte{1}) || targetConn.write.Len() == 0 || len(rest) == 0 {
				t.Fatal("maximum native whisper rejected")
			}
		} else if !bytes.Equal(packet.Body, []byte{0, 1}) || targetConn.write.Len() != 0 || len(rest) != 0 {
			t.Fatal("invalid native whisper reached NOTI65")
		}
	}
}
