package dnfbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func currentNativeWhisperFixture(target byte) []byte {
	// Produced by current EXE 0x201BFF7 / 0x348F65B with native scalar
	// writers; synthetic message and GBK recipient name, final opaque u8=7.
	body, _ := hex.DecodeString("240000000000001300000068656c6c6f203c6974656d3a3930303030313e06000000c0b4c0b4c0b407")
	body[1] = target
	return body
}

func TestCurrentNativeWhisperShapeAndExactTrailer(t *testing.T) {
	for _, target := range []byte{0, 5} {
		body := currentNativeWhisperFixture(target)
		request, err := decodeCurrentPeerChatRequest(body)
		if err != nil || request.MessageType != 0x24 || request.TargetID != uint16(target) || !request.HasTrailingValue || request.TrailingValue != 7 || !bytes.Equal(request.Message, []byte("hello <item:900001>")) || !bytes.Equal(request.TargetName, rosterNameBytes("来来来")) {
			t.Fatalf("native request %+v error %v", request, err)
		}
		if dir := os.Getenv("DNF90_CHAT_NATIVE_FIXTURE_DIR"); dir != "" {
			file := "v77-chat-typed_name_target_zero.bin"
			if target != 0 {
				file = "v77-chat-menu_target_id.bin"
			}
			native, err := os.ReadFile(filepath.Join(dir, file))
			if err != nil || !bytes.Equal(native, body) {
				t.Fatalf("actual native producer fixture differs: %v", err)
			}
		}
		if _, err := decodeCurrentPeerChatRequest(append(body, 0)); err == nil {
			t.Fatal("extra byte accepted")
		}
		if target == 0 {
			if _, err := decodeCurrentPeerChatRequest(body[:len(body)-1]); err == nil {
				t.Fatal("name-only native ID0 without trailer accepted")
			}
		}
	}
}

func TestCurrentNativeWhisperResolvesNameAcrossAreasAndDoesNotBroadcast(t *testing.T) {
	for _, targetID := range []byte{0, 5} {
		service, source, target, sourceConn, targetConn := newCurrentSocialPeerTest(t)
		service.options.currentClientVersion = "official-24.9"
		target.channel = channelcatalog.Channel{ID: 43}
		service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 5, TownID: 2, AreaID: 2, Session: target})
		spectatorConn := &bufferConn{}
		spectator := &gameSession{conn: spectatorConn, channel: source.channel, accountID: "dnf:3", selectedCharacterID: 9}
		service.bindGameSessionCharacter(spectator, 9)
		service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 9, TownID: 1, AreaID: 1, Session: spectator})
		if err := service.handleCurrentPeerChat(source, 17, currentNativeWhisperFixture(targetID)); err != nil {
			t.Fatal(err)
		}
		ack, rest := splitGameServerUpperPacket(t, sourceConn.write.Bytes())
		if ack.Header.Classification != 1 || ack.Header.MsgID != 17 || !bytes.Equal(ack.Body, []byte{1}) {
			t.Fatalf("ACK %+v", ack)
		}
		echo, rest := splitGameServerUpperPacket(t, rest)
		want := buildCurrentOfficialWhisperBody(42, "pouut", "来来来", []byte("hello <item:900001>"))
		if echo.Header.MsgID != 65 || echo.Header.Classification != 0 || !bytes.Equal(echo.Body, want) || len(rest) != 0 {
			t.Fatal("sender message route or extra trade-state notification")
		}
		notice, rest := splitGameServerUpperPacket(t, targetConn.write.Bytes())
		want = buildCurrentOfficialWhisperBody(42, "pouut", "pouut", []byte("hello <item:900001>"))
		if notice.Header.MsgID != 65 || notice.Header.Classification != 0 || !bytes.Equal(notice.Body, want) || len(rest) != 0 {
			t.Fatal("private notice altered bytes or sender channel")
		}
		if spectatorConn.write.Len() != 0 {
			t.Fatal("private text leaked to town")
		}
	}
}

func TestCurrentNativeWhisperRejectsSpoofedSourceMismatchedRecipientAndUnverifiedGroup(t *testing.T) {
	for _, mode := range []string{"source_owner", "target_name", "group", "offline"} {
		t.Run(mode, func(t *testing.T) {
			service, source, target, _, targetConn := newCurrentSocialPeerTest(t)
			body := currentNativeWhisperFixture(5)
			switch mode {
			case "source_owner":
				repositories, _ := service.repositoryGroup()
				if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "other", Name: "spoof"}); err != nil {
					t.Fatal(err)
				}
			case "target_name":
				body[len(body)-2] = 'x'
			case "group":
				body[0] = 0x2e
			case "offline":
				service.unbindGameSession(target)
			}
			if err := service.handleCurrentPeerChat(source, 17, body); err != nil {
				t.Fatal(err)
			}
			if targetConn.write.Len() != 0 {
				t.Fatal("rejected private text reached target")
			}
		})
	}
}
