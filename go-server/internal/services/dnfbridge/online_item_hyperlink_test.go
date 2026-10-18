package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	currentHyperlinkCaptureOneBody = "032f0000000000150000007f7f5b2b333120caa5d2abbec8caeab9e2bda35d7f01ffb400ff6dc105063900090a1f003000000000006dc1050604000700000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000008000000000000000000000000000000000000000000000000000000000000000000000000000000"
	currentHyperlinkCaptureTwoBody = "032e0000000000290000007f7f5b2b333120caa5d2abbec8caeab6dbc6f75d7f7f5b2b333120caa5d2abbec8caeab6dbc6f75d7f02ffb400ff02740506fec99a3b1f002d0000000000a2d19800030007000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000066a5f06a00000000000000000000000000000000000000000000000008000000000000000000000000000000000000000000000000000000000000000000000000000000ffb400ff02740506fec99a3b1f002d0000000000a2d19800030007000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000066a5f06a00000000000000000000000000000000000000000000000008000000000000000000000000000000000000000000000000000000000000000000000000000000"
)

func mustCurrentHyperlinkCapture(t *testing.T, value string) []byte {
	t.Helper()
	body, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func buildCurrentHyperlinkBodyForTest(messageType byte, hint uint16, reserved uint32, message []byte, items [][]byte) []byte {
	var writer packetWriter
	writer.writeByte(messageType)
	writer.writeUint16(hint)
	writer.writeUint32(reserved)
	writer.writeUint32(uint32(len(message)))
	writer.writeBytes(message)
	writer.writeByte(byte(len(items)))
	for _, item := range items {
		writer.writeBytes(item)
	}
	return writer.bytes()
}

func TestR73CurrentItemHyperlinkRealCaptureBodies(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		hint    uint16
		count   int
		itemHex string
	}{
		{
			name: "one_item_game_000009", body: currentHyperlinkCaptureOneBody, hint: 47, count: 1,
			itemHex: "ffb400ff6dc105063900090a1f003000000000006dc1050604000700000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000008000000000000000000000000000000000000000000000000000000000000000000000000000000",
		},
		{
			name: "two_items_game_000014", body: currentHyperlinkCaptureTwoBody, hint: 46, count: 2,
			itemHex: "ffb400ff02740506fec99a3b1f002d0000000000a2d19800030007000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000066a5f06a00000000000000000000000000000000000000000000000008000000000000000000000000000000000000000000000000000000000000000000000000000000",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := mustCurrentHyperlinkCapture(t, tc.body)
			request, err := decodeCurrentItemHyperlinkRequest(body)
			if err != nil {
				t.Fatal(err)
			}
			if request.MessageType != currentAreaChatMessageType || request.Hint != tc.hint || request.Reserved != 0 ||
				len(request.Message) == 0 || bytes.IndexByte(request.Message, 0) >= 0 || len(request.Items) != tc.count {
				t.Fatalf("decoded request = %+v", request)
			}
			item, err := hex.DecodeString(tc.itemHex)
			if err != nil {
				t.Fatal(err)
			}
			for index, got := range request.Items {
				if !bytes.Equal(got, item) {
					t.Fatalf("item %d changed: got %x want %x", index, got, item)
				}
			}
		})
	}
}

func TestR73CurrentItemHyperlinkAckThenTwoItemNotice(t *testing.T) {
	service, source, _, sourceConn, targetConn := newCurrentSocialPeerTest(t)
	body := mustCurrentHyperlinkCapture(t, currentHyperlinkCaptureTwoBody)
	request, err := decodeCurrentItemHyperlinkRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	handled, err := service.handleAlignedGameCommand(
		source, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketItemHyperlinkMessage), body,
	)
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	ack, rest := splitGameServerUpperPacket(t, sourceConn.write.Bytes())
	if ack.Header.Classification != dnfproto.DefaultChannelClassification ||
		ack.Header.MsgID != uint16(dnfenum.CmdPacketItemHyperlinkMessage) || !bytes.Equal(ack.Body, []byte{1}) {
		t.Fatalf("ack header=%+v body=%x", ack.Header, ack.Body)
	}
	wantNotice := buildCurrentItemHyperlinkNoticeBody(42, 1, request.Message, request.Items)
	self, rest := splitGameServerUpperPacket(t, rest)
	if self.Header.Classification != 0 || self.Header.MsgID != currentItemHyperlinkNotifyOpcode ||
		!bytes.Equal(self.Body, wantNotice) || len(rest) != 0 {
		t.Fatalf("self notice header=%+v body_match=%t trailing=%d", self.Header, bytes.Equal(self.Body, wantNotice), len(rest))
	}
	peer, rest := splitGameServerUpperPacket(t, targetConn.write.Bytes())
	if peer.Header.Classification != 0 || peer.Header.MsgID != currentItemHyperlinkNotifyOpcode ||
		!bytes.Equal(peer.Body, wantNotice) || len(rest) != 0 {
		t.Fatalf("peer notice header=%+v body_match=%t trailing=%d", peer.Header, bytes.Equal(peer.Body, wantNotice), len(rest))
	}
	if binary.LittleEndian.Uint16(wantNotice[1:3]) != 1 {
		t.Fatalf("sender came from hint or wrong role: %x", wantNotice[:3])
	}
	if len(wantNotice) != 8+len(request.Message)+1+2*currentItemHyperlinkItemSize+4 {
		t.Fatalf("notice length=%d", len(wantNotice))
	}
}

func TestR73CurrentItemHyperlinkRejectsMalformedAndForeignTypes(t *testing.T) {
	message := []byte("hello")
	item := bytes.Repeat([]byte{0x5a}, currentItemHyperlinkItemSize)
	valid := buildCurrentHyperlinkBodyForTest(currentAreaChatMessageType, 9, 0, message, [][]byte{item})
	validCountOffset := 11 + len(message)

	withReserved := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint32(withReserved[3:7], 7)
	badDeclared := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint32(badDeclared[7:11], uint32(len(message)+1))
	nulMessage := append([]byte(nil), valid...)
	nulMessage[11] = 0
	zeroCount := append([]byte(nil), valid[:validCountOffset]...)
	zeroCount = append(zeroCount, 0)
	tooManyItems := make([][]byte, 6)
	for index := range tooManyItems {
		tooManyItems[index] = bytes.Repeat([]byte{byte(index + 1)}, currentItemHyperlinkItemSize)
	}

	for _, tc := range []struct {
		name string
		body []byte
	}{
		{name: "short", body: valid[:10]},
		{name: "reserved_nonzero", body: withReserved},
		{name: "declared_length_mismatch", body: badDeclared},
		{name: "nul_message", body: nulMessage},
		{name: "zero_count", body: zeroCount},
		{name: "six_items", body: buildCurrentHyperlinkBodyForTest(currentAreaChatMessageType, 0, 0, message, tooManyItems)},
		{name: "truncated_item", body: valid[:len(valid)-1]},
		{name: "trailing_byte", body: append(append([]byte(nil), valid...), 0)},
		{name: "message_256", body: buildCurrentHyperlinkBodyForTest(currentAreaChatMessageType, 0, 0, bytes.Repeat([]byte{'a'}, 256), [][]byte{item})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, source, _, sourceConn, targetConn := newCurrentSocialPeerTest(t)
			handled, err := service.handleAlignedGameCommand(
				source, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketItemHyperlinkMessage), tc.body,
			)
			if err != nil || !handled {
				t.Fatalf("handled=%t err=%v", handled, err)
			}
			ack, rest := splitGameServerUpperPacket(t, sourceConn.write.Bytes())
			if ack.Header.Classification != dnfproto.DefaultChannelClassification ||
				ack.Header.MsgID != uint16(dnfenum.CmdPacketItemHyperlinkMessage) ||
				!bytes.Equal(ack.Body, []byte{0, 1}) || len(rest) != 0 || targetConn.write.Len() != 0 {
				t.Fatalf("rejection ack=%+v body=%x trailing=%d target=%d", ack.Header, ack.Body, len(rest), targetConn.write.Len())
			}
		})
	}

	for _, messageType := range []byte{4, 6, 0x24, 0x28, 55, 56} {
		t.Run("type_"+hex.EncodeToString([]byte{messageType}), func(t *testing.T) {
			service, source, _, sourceConn, targetConn := newCurrentSocialPeerTest(t)
			body := buildCurrentHyperlinkBodyForTest(messageType, 0, 0, message, [][]byte{item})
			handled, err := service.handleAlignedGameCommand(
				source, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketItemHyperlinkMessage), body,
			)
			if err != nil || !handled {
				t.Fatalf("handled=%t err=%v", handled, err)
			}
			ack, rest := splitGameServerUpperPacket(t, sourceConn.write.Bytes())
			if !bytes.Equal(ack.Body, []byte{0, 1}) || len(rest) != 0 || targetConn.write.Len() != 0 {
				t.Fatalf("type %d leaked or faked success: body=%x trailing=%d target=%d", messageType, ack.Body, len(rest), targetConn.write.Len())
			}
		})
	}
}

func TestR73CurrentItemHyperlinkMessageLimit(t *testing.T) {
	item := bytes.Repeat([]byte{1}, currentItemHyperlinkItemSize)
	for _, tc := range []struct {
		length int
		ok     bool
	}{{255, true}, {256, false}} {
		body := buildCurrentHyperlinkBodyForTest(currentAreaChatMessageType, 0, 0, bytes.Repeat([]byte{'a'}, tc.length), [][]byte{item})
		_, err := decodeCurrentItemHyperlinkRequest(body)
		if (err == nil) != tc.ok {
			t.Fatalf("message length %d accepted=%t want=%t err=%v", tc.length, err == nil, tc.ok, err)
		}
	}
}

func TestR73CurrentItemHyperlinkRequiresCurrentSameAreaRecipients(t *testing.T) {
	service, source, _, sourceConn, targetConn := newCurrentSocialPeerTest(t)
	privateConn := &bufferConn{}
	private := &gameSession{conn: privateConn, channel: source.channel, accountID: "dnf:3", selectedCharacterID: 9}
	service.bindGameSessionCharacter(private, 9)
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 9, TownID: 1, AreaID: 1, Instance: 1, Session: private})
	foreignChannelConn := &bufferConn{}
	foreignChannel := &gameSession{conn: foreignChannelConn, channel: channelcatalog.Channel{ID: 43}, accountID: "dnf:4", selectedCharacterID: 10}
	service.bindGameSessionCharacter(foreignChannel, 10)
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 10, TownID: 1, AreaID: 1, Session: foreignChannel})
	foreignAreaConn := &bufferConn{}
	foreignArea := &gameSession{conn: foreignAreaConn, channel: source.channel, accountID: "dnf:5", selectedCharacterID: 11}
	service.bindGameSessionCharacter(foreignArea, 11)
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 11, TownID: 1, AreaID: 2, Session: foreignArea})

	body := buildCurrentHyperlinkBodyForTest(currentAreaChatMessageType, 9999, 0, []byte("area link"), [][]byte{bytes.Repeat([]byte{7}, 148)})
	if err := service.handleCurrentItemHyperlinkMessage(source, uint16(dnfenum.CmdPacketItemHyperlinkMessage), body); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, sourceConn.write.Bytes())
	if !bytes.Equal(ack.Body, []byte{1}) {
		t.Fatalf("ack=%x", ack.Body)
	}
	self, rest := splitGameServerUpperPacket(t, rest)
	if self.Header.MsgID != currentItemHyperlinkNotifyOpcode || len(rest) != 0 {
		t.Fatalf("self notice=%+v trailing=%d", self.Header, len(rest))
	}
	peer, rest := splitGameServerUpperPacket(t, targetConn.write.Bytes())
	if peer.Header.MsgID != currentItemHyperlinkNotifyOpcode || len(rest) != 0 {
		t.Fatalf("peer notice=%+v trailing=%d", peer.Header, len(rest))
	}
	if privateConn.write.Len() != 0 || foreignChannelConn.write.Len() != 0 || foreignAreaConn.write.Len() != 0 {
		t.Fatalf("leaked private=%d channel=%d area=%d", privateConn.write.Len(), foreignChannelConn.write.Len(), foreignAreaConn.write.Len())
	}
}

func TestR73CurrentItemHyperlinkRejectsStaleSessionAndOwnerMismatch(t *testing.T) {
	t.Run("stale_session", func(t *testing.T) {
		service, stale, _, staleConn, targetConn := newCurrentSocialPeerTest(t)
		replacementConn := &bufferConn{}
		replacement := &gameSession{conn: replacementConn, channel: stale.channel, accountID: stale.accountID, selectedCharacterID: 1}
		service.bindGameSessionCharacter(replacement, 1)
		service.promoteResidentGameSession(replacement, 1)
		service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 1, TownID: 1, AreaID: 1, Session: replacement})
		staleConn.write.Reset()
		targetConn.write.Reset()
		replacementConn.write.Reset()
		body := buildCurrentHyperlinkBodyForTest(currentAreaChatMessageType, 0, 0, []byte("stale"), [][]byte{bytes.Repeat([]byte{1}, 148)})
		if err := service.handleCurrentItemHyperlinkMessage(stale, uint16(dnfenum.CmdPacketItemHyperlinkMessage), body); err != nil {
			t.Fatal(err)
		}
		ack, rest := splitGameServerUpperPacket(t, staleConn.write.Bytes())
		if !bytes.Equal(ack.Body, []byte{0, 1}) || len(rest) != 0 || targetConn.write.Len() != 0 {
			t.Fatalf("stale ack=%x trailing=%d target=%d", ack.Body, len(rest), targetConn.write.Len())
		}
		if err := service.handleCurrentItemHyperlinkMessage(replacement, uint16(dnfenum.CmdPacketItemHyperlinkMessage), body); err != nil {
			t.Fatal(err)
		}
		ack, rest = splitGameServerUpperPacket(t, replacementConn.write.Bytes())
		if !bytes.Equal(ack.Body, []byte{1}) {
			t.Fatalf("replacement ack=%x", ack.Body)
		}
		if _, rest = splitGameServerUpperPacket(t, rest); len(rest) != 0 {
			t.Fatalf("replacement self notice trailing=%d", len(rest))
		}
		if _, rest = splitGameServerUpperPacket(t, targetConn.write.Bytes()); len(rest) != 0 {
			t.Fatalf("replacement peer notice trailing=%d", len(rest))
		}
	})

	t.Run("owner_mismatch", func(t *testing.T) {
		service, source, _, sourceConn, targetConn := newCurrentSocialPeerTest(t)
		repositories, _ := service.repositoryGroup()
		if err := repositories.Character.Save(t.Context(), dnfrepo.CharacterRecord{
			CharacterID: "1", AccountID: "dnf:other", Name: "pouut", Job: "0", Level: 90,
		}); err != nil {
			t.Fatal(err)
		}
		body := buildCurrentHyperlinkBodyForTest(currentAreaChatMessageType, 0, 0, []byte("owner"), [][]byte{bytes.Repeat([]byte{2}, 148)})
		if err := service.handleCurrentItemHyperlinkMessage(source, uint16(dnfenum.CmdPacketItemHyperlinkMessage), body); err != nil {
			t.Fatal(err)
		}
		ack, rest := splitGameServerUpperPacket(t, sourceConn.write.Bytes())
		if !bytes.Equal(ack.Body, []byte{0, 1}) || len(rest) != 0 || targetConn.write.Len() != 0 {
			t.Fatalf("owner mismatch ack=%x trailing=%d target=%d", ack.Body, len(rest), targetConn.write.Len())
		}
	})
}
