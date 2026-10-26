package dnfbridge

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func startGuildChatReconnectEvents(t *testing.T, s *Service, p *gameSession) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	p.ctx = ctx
	if err := s.startGameSessionEvents(ctx, p); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		stop, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		if err := s.stopGameSessionEvents(stop, p); err != nil {
			t.Error(err)
		}
	})
}

func guildChatNoticeMessages(t *testing.T, p *gameSession) [][]byte {
	t.Helper()
	var messages [][]byte
	for wire := p.conn.(*bufferConn).write.Bytes(); len(wire) > 0; {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		if packet.Header.Classification != 0 || packet.Header.MsgID != currentOfficialWhisperNotifyOpcode {
			continue
		}
		switch {
		case bytes.Contains(packet.Body, []byte("from-29")):
			messages = append(messages, []byte("from-29"))
		case bytes.Contains(packet.Body, []byte("from-30")):
			messages = append(messages, []byte("from-30"))
		case bytes.Contains(packet.Body, []byte("repeat")):
			messages = append(messages, []byte("repeat"))
		case bytes.Contains(packet.Body, []byte("old-offline")):
			messages = append(messages, []byte("old-offline"))
		case bytes.Contains(packet.Body, []byte("new-online")):
			messages = append(messages, []byte("new-online"))
		}
	}
	return messages
}

func TestGuildChatOfflineReconnectDropsOldAndDeliversNew(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	startGuildChatTestEvents(t, s, peers)
	old := peers[30]
	s.unbindGameSession(old)

	if err := s.handleCurrentPeerChat(peers[29], 17, guildChatRequest(29, "old-offline")); err != nil {
		t.Fatal(err)
	}
	flushGuildChatTestEvents(t, s, peers[29])
	flushGuildChatTestEvents(t, s, peers[33])
	requireGuildChatNotice(t, peers[29], 1)
	requireGuildChatNotice(t, peers[33], 1)
	requireGuildChatNotice(t, old, 0)

	replacement := &gameSession{
		conn:                            &bufferConn{},
		connID:                          "guild-chat-reconnect-30",
		accountID:                       old.accountID,
		selectedCharacterID:             old.selectedCharacterID,
		characterGeneration:             old.characterGeneration + 1,
		channel:                         old.channel,
		residentChannel:                 old.residentChannel,
		connectionTownActorOwnerChannel: old.connectionTownActorOwnerChannel,
		townActorOwnerChannel:           old.townActorOwnerChannel,
		townPositionSnapshot:            old.townPositionSnapshot,
	}
	startGuildChatReconnectEvents(t, s, replacement)
	s.promoteResidentGameSession(replacement, old.selectedCharacterID)
	flushGuildChatTestEvents(t, s, replacement)
	requireGuildChatNotice(t, replacement, 0)

	if err := s.handleCurrentPeerChat(peers[29], 17, guildChatRequest(29, "new-online")); err != nil {
		t.Fatal(err)
	}
	flushGuildChatTestEvents(t, s, peers[29])
	flushGuildChatTestEvents(t, s, peers[33])
	flushGuildChatTestEvents(t, s, replacement)
	requireGuildChatNotice(t, peers[29], 2)
	requireGuildChatNotice(t, peers[33], 2)
	requireGuildChatNotice(t, replacement, 1)
	requireGuildChatNotice(t, old, 0)
	if got := guildChatNoticeMessages(t, replacement); len(got) != 1 || !bytes.Equal(got[0], []byte("new-online")) {
		t.Fatalf("reconnected character messages=%q", got)
	}
}

func TestGuildChatSameDisplayNameSeparatesCharacterIdentity(t *testing.T) {
	s, peers, repos := guildHallFixture(t)
	ctx := context.Background()
	for _, id := range []string{"29", "30", "33"} {
		character, found, err := repos.Character.Load(ctx, id)
		if err != nil || !found {
			t.Fatalf("load character %s found=%t err=%v", id, found, err)
		}
		character.Name = "同名角色"
		if err := repos.Character.Save(ctx, character); err != nil {
			t.Fatal(err)
		}
	}
	startGuildChatTestEvents(t, s, peers)

	if err := s.handleCurrentPeerChat(peers[29], 17, guildChatRequest(0, "from-29")); err != nil {
		t.Fatal(err)
	}
	if err := s.handleCurrentPeerChat(peers[30], 17, guildChatRequest(0, "from-30")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint16{29, 30, 33} {
		flushGuildChatTestEvents(t, s, peers[id])
		requireGuildChatNotice(t, peers[id], 2)
	}
	for _, id := range []uint16{31, 32} {
		requireGuildChatNotice(t, peers[id], 0)
	}

	got := guildChatNoticeMessages(t, peers[33])
	if len(got) != 2 || !bytes.Equal(got[0], []byte("from-29")) || !bytes.Equal(got[1], []byte("from-30")) {
		t.Fatalf("same-name recipient messages=%q", got)
	}
}

func TestGuildChatRepeatedIdenticalSendDeliversOncePerRequest(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	startGuildChatTestEvents(t, s, peers)

	if err := s.handleCurrentPeerChat(peers[29], 17, guildChatRequest(29, "repeat")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint16{29, 30, 33} {
		flushGuildChatTestEvents(t, s, peers[id])
		requireGuildChatNotice(t, peers[id], 1)
	}

	if err := s.handleCurrentPeerChat(peers[29], 17, guildChatRequest(29, "repeat")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint16{29, 30, 33} {
		flushGuildChatTestEvents(t, s, peers[id])
		requireGuildChatNotice(t, peers[id], 2)
		messages := guildChatNoticeMessages(t, peers[id])
		if len(messages) != 2 || !bytes.Equal(messages[0], []byte("repeat")) || !bytes.Equal(messages[1], []byte("repeat")) {
			t.Fatalf("character=%d repeated messages=%q", id, messages)
		}
	}
}
