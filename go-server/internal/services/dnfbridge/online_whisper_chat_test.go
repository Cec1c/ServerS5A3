package dnfbridge

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestOfficialWhisperUsesMessageRouteWithoutTradeState(t *testing.T) {
	service, source, target, sourceConn, targetConn := newCurrentSocialPeerTest(t)
	service.options.currentClientVersion = "official-24.9"
	// The current client's typed-name route uses a zero ID; the repository
	// resolves the actual recipient. Only this account receives the message.
	var request packetWriter
	request.writeByte(0x24)
	request.writeUint16(0)
	request.writeUint32(0)
	request.writeRawDstr([]byte("hello"))
	request.writeRawDstr(rosterNameBytes("来来来"))
	request.writeByte(7)
	if err := service.handleCurrentPeerChat(source, 17, request.bytes()); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, sourceConn.write.Bytes())
	if ack.Header.MsgID != 17 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1}) {
		t.Fatalf("ack=%+v", ack)
	}
	echo, rest := splitGameServerUpperPacket(t, rest)
	want := buildCurrentOfficialWhisperBody(42, "pouut", "来来来", []byte("hello"))
	if echo.Header.MsgID != 65 || echo.Header.Classification != 0 || !bytes.Equal(echo.Body, want) || len(rest) != 0 {
		t.Fatalf("echo=%+v trailing=%d", echo, len(rest))
	}
	incoming, rest := splitGameServerUpperPacket(t, targetConn.write.Bytes())
	want = buildCurrentOfficialWhisperBody(42, "pouut", "pouut", []byte("hello"))
	if incoming.Header.MsgID != 65 || incoming.Header.Classification != 0 || !bytes.Equal(incoming.Body, want) || len(rest) != 0 || selectedCharacterID(target) != 5 {
		t.Fatalf("incoming=%+v trailing=%d", incoming, len(rest))
	}
	if dir := os.Getenv("DNFBRIDGE_CHAT_NATIVE_EXPORT"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "whisper-message.bin"), incoming.Body, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "whisper-echo.bin"), echo.Body, 0644); err != nil {
			t.Fatal(err)
		}
	}
	// Reply using the conversation key the native receive path stored. A real
	// client submits type 0x28 with UID zero when sending through this window.
	sourceConn.write.Reset()
	targetConn.write.Reset()
	var reply packetWriter
	reply.writeByte(0x28)
	reply.writeUint16(0)
	reply.writeUint32(0)
	reply.writeRawDstr([]byte("reply"))
	reply.writeRawDstr(rosterNameBytes("pouut"))
	reply.writeByte(42)
	if err := service.handleCurrentPeerChat(target, 17, reply.bytes()); err != nil {
		t.Fatal(err)
	}
	receivedReply, trailing := splitGameServerUpperPacket(t, sourceConn.write.Bytes())
	if receivedReply.Header.MsgID != 65 || receivedReply.Header.Classification != 0 || len(trailing) != 0 ||
		!bytes.Equal(receivedReply.Body, buildCurrentOfficialWhisperBody(42, "来来来", "来来来", []byte("reply"))) {
		t.Fatalf("reply=%+v trailing=%d", receivedReply, len(trailing))
	}
}
