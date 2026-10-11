package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func currentIncomingWhisperReplyBody(target uint16, channel byte) []byte {
	var w packetWriter
	w.writeByte(0x28)
	w.writeUint16(target)
	w.writeUint32(0)
	w.writeRawDstr([]byte("reply!"))
	w.writeRawDstr([]byte("pouut"))
	w.writeByte(channel)
	return w.bytes()
}

func TestIncomingWhisperWindowReplyNativeShape(t *testing.T) {
	for _, uid := range []uint16{0, 5} {
		body := currentIncomingWhisperReplyBody(uid, 10)
		r, err := decodeCurrentPeerChatRequest(body)
		if err != nil || r.MessageType != 0x28 || r.TargetID != uid || r.Unknown != 0 || r.TrailingValue != 10 || !r.HasTrailingValue {
			t.Fatalf("reply decoded=%+v error=%v", r, err)
		}
		if dir := os.Getenv("DNF90_WHISPER_REPLY_NATIVE_DIR"); dir != "" {
			name := "whisper-reply-uid0.bin"
			if uid != 0 {
				name = "whisper-reply-uid5.bin"
			}
			native, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil || !bytes.Equal(native, body) {
				t.Fatalf("native fixture mismatch: %v", err)
			}
		}
		for _, malformed := range [][]byte{body[:len(body)-1], append(append([]byte(nil), body...), 0)} {
			if _, err := decodeCurrentPeerChatRequest(malformed); err == nil {
				t.Fatal("malformed channel trailer accepted")
			}
		}
		binary.LittleEndian.PutUint32(body[3:7], 1)
		if _, err := decodeCurrentPeerChatRequest(body); err == nil {
			t.Fatal("non-private context accepted")
		}
	}
}

func TestOfficialIncomingWhisperReplyAuthenticatesPeerAndChannel(t *testing.T) {
	for _, tc := range []struct {
		name      string
		uid       uint16
		channel   byte
		wrongName bool
		want      bool
	}{
		{"name_resolved", 0, 42, false, true},
		{"uid_and_name", 1, 42, false, true},
		{"stale_channel", 0, 43, false, false},
		{"wrong_uid", 99, 42, false, false},
		{"wrong_name", 1, 42, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, originalSender, replySender, senderConn, replyConn := newCurrentSocialPeerTest(t)
			s.options.currentClientVersion = "official-24.9"
			_ = originalSender
			body := currentIncomingWhisperReplyBody(tc.uid, tc.channel)
			if tc.wrongName {
				body[len(body)-2] = 'x'
			}
			if err := s.handleCurrentPeerChat(replySender, 17, body); err != nil {
				t.Fatal(err)
			}
			if !tc.want {
				if senderConn.write.Len() != 0 {
					t.Fatal("rejected reply reached peer")
				}
				return
			}
			incoming, rest := splitGameServerUpperPacket(t, senderConn.write.Bytes())
			if incoming.Header.MsgID != 65 || incoming.Header.Classification != 0 || len(rest) != 0 {
				t.Fatal("reply did not use verified message notification")
			}
			ack, rest := splitGameServerUpperPacket(t, replyConn.write.Bytes())
			if ack.Header.MsgID != 17 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1}) {
				t.Fatal("reply ACK invalid")
			}
			echo, rest := splitGameServerUpperPacket(t, rest)
			if echo.Header.MsgID != 65 || !bytes.Equal(echo.Body, buildCurrentOfficialWhisperBody(42, "来来来", "pouut", []byte("reply!"))) || len(rest) != 0 {
				t.Fatal("reply echo invalid")
			}
			if !bytes.Equal(incoming.Body, buildCurrentOfficialWhisperBody(42, "来来来", "来来来", []byte("reply!"))) {
				t.Fatal("real sender/receiver projection changed")
			}
		})
	}
}
