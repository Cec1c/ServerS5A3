package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestCurrentAreaChatLiveMessageOnlyRequestAndCurrentMessageReader(t *testing.T) {
	// Metadata captured from the player's op17 request: 3, u16 actor=1,
	// u32 zero, DSTR length=1. A single test letter substitutes for chat text.
	body := []byte{3, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 'a'}
	request, err := decodeCurrentPeerChatRequest(body)
	if err != nil || request.MessageType != 3 || request.TargetID != 1 || !bytes.Equal(request.Message, []byte("a")) || len(request.TargetName) != 0 {
		t.Fatalf("request=%+v err=%v", request, err)
	}
	notice := buildCurrentAreaChatNoticeBody(42, 1, []byte("a"))
	if !bytes.Equal(notice[:4], []byte{3, 1, 0, 42}) {
		t.Fatalf("native fixed header=%x", notice[:4])
	}
	cursor := 4
	for index, want := range [][]byte{[]byte("a"), nil} {
		if cursor+4 > len(notice) {
			t.Fatalf("native DSTR %d missing", index)
		}
		n := int(binary.LittleEndian.Uint32(notice[cursor : cursor+4]))
		cursor += 4
		if cursor+n > len(notice) || !bytes.Equal(notice[cursor:cursor+n], want) {
			t.Fatalf("native DSTR %d length=%d", index, n)
		}
		cursor += n
	}
	if cursor != len(notice) {
		t.Fatalf("native reader trailing=%d", len(notice)-cursor)
	}
	for _, malformed := range [][]byte{body[:10], append(append([]byte(nil), body...), 0), {3, 1, 0, 0, 0, 0, 0, 255, 255, 255, 255}} {
		if _, err := decodeCurrentPeerChatRequest(malformed); err == nil {
			t.Fatalf("malformed accepted=%x", malformed)
		}
	}
}

func TestCurrentAreaChatEchoesAndIsolatesOtherChannelsAndPrivateRooms(t *testing.T) {
	for _, private := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared", true: "private"}[private], func(t *testing.T) {
			service, source, _, sourceConn, peerConn := newCurrentSocialPeerTest(t)
			foreignConn := &bufferConn{}
			foreign := &gameSession{conn: foreignConn, channel: channelcatalog.Channel{ID: 43}, accountID: "dnf:3", selectedCharacterID: 9}
			service.bindGameSessionCharacter(foreign, 9)
			service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 9, TownID: 1, AreaID: 1, Session: foreign})
			if private {
				service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 1, TownID: 1, AreaID: 1, Instance: 1, Session: source})
			}
			var request packetWriter
			request.writeByte(3)
			request.writeUint16(1)
			request.writeUint32(0)
			request.writeRawDstr([]byte("hello"))
			if err := service.handleCurrentPeerChat(source, 17, request.bytes()); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, sourceConn.write.Bytes())
			if ack.Header.MsgID != 17 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1}) {
				t.Fatalf("sender ack=%+v", ack)
			}
			echo, rest := splitGameServerUpperPacket(t, rest)
			want := buildCurrentAreaChatNoticeBody(42, 1, []byte("hello"))
			if echo.Header.MsgID != currentAreaChatNotifyOpcode || echo.Header.Classification != 0 || !bytes.Equal(echo.Body, want) || len(rest) != 0 {
				t.Fatalf("sender echo=%+v trailing=%d", echo, len(rest))
			}
			if dir := os.Getenv("DNFBRIDGE_CHAT_NATIVE_EXPORT"); dir != "" && !private {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "normal-message.bin"), echo.Body, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if private {
				if peerConn.write.Len() != 0 {
					t.Fatal("private room leaked to shared area")
				}
			} else {
				peer, rest := splitGameServerUpperPacket(t, peerConn.write.Bytes())
				if peer.Header.MsgID != currentAreaChatNotifyOpcode || !bytes.Equal(peer.Body, want) || len(rest) != 0 {
					t.Fatalf("peer notice=%+v", peer)
				}
			}
			if foreignConn.write.Len() != 0 {
				t.Fatal("chat leaked to another channel")
			}
		})
	}
}

func TestCurrentAreaChatRequiresAuthenticatedSelectedCharacter(t *testing.T) {
	service, source, _, sourceConn, peerConn := newCurrentSocialPeerTest(t)
	repositories, _ := service.repositoryGroup()
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "other", Name: "other"}); err != nil {
		t.Fatal(err)
	}
	if err := service.handleCurrentAreaChat(source, 17, currentPeerChatRequest{MessageType: 3, Message: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, sourceConn.write.Bytes())
	if !bytes.Equal(ack.Body, []byte{0, 1}) || len(rest) != 0 || peerConn.write.Len() != 0 {
		t.Fatalf("identity rejection=%x trailing=%d", ack.Body, len(rest))
	}
}
