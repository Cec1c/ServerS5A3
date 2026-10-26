package dnfbridge

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
)

func guildChatRequest(source uint16, message string) []byte {
	var w packetWriter
	w.writeByte(6)
	w.writeUint16(source)
	w.writeUint32(0)
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(message))
	if err != nil {
		panic(err)
	}
	w.writeRawDstr(encoded)
	return w.bytes()
}

func TestGuildChatCrossChannelAndSceneUsesNamedNativeNotice(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	// No shared scene actors: the named notification must work across scenes.
	peers[30].townPositionSnapshot.TownID = 38
	if err := s.handleCurrentPeerChat(peers[29], 17, guildChatRequest(29, "公会消息")); err != nil {
		t.Fatal(err)
	}
	var expected packetWriter
	expected.writeBytes([]byte{6, 0, 0, 0})
	expected.writeRawDstr(rosterNameBytes("角色29"))
	expected.writeByte(peers[29].townActorOwnerChannel)
	expected.writeRawDstr(rosterNameBytes("公会消息"))
	expected.writeRawDstr(nil)
	for id, peer := range peers {
		wire := peer.conn.(*bufferConn).write.Bytes()
		member := id == 29 || id == 30 || id == 33
		if !member && len(wire) != 0 {
			t.Fatalf("guild text leaked to character %d", id)
		}
		ack, notices := 0, 0
		for len(wire) > 0 {
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
			wire = rest
			switch {
			case packet.Header.MsgID == 17 && packet.Header.Classification == 1:
				if !bytes.Equal(packet.Body, []byte{1}) {
					t.Fatalf("ack=%x", packet.Body)
				}
				ack++
			case packet.Header.MsgID == 65 && packet.Header.Classification == 0:
				if !bytes.Equal(packet.Body, expected.bytes()) {
					t.Fatalf("notice=%x", packet.Body)
				}
				notices++
			default:
				t.Fatalf("unexpected guild message route %d/%d", packet.Header.Classification, packet.Header.MsgID)
			}
		}
		if member && notices != 1 || id == 29 && ack != 1 || id != 29 && ack != 0 {
			t.Fatalf("character=%d ack=%d notices=%d", id, ack, notices)
		}
	}
}

func TestGuildChatRejectsInvalidSenderAndNativeBody(t *testing.T) {
	for _, mode := range []string{"nonmember", "wrong_account", "stale_session", "group_id", "named_target", "empty", "long", "nul", "member_account", "truncated", "trailing", "repository_owner"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos := guildHallFixture(t)
			source := peers[29]
			body := guildChatRequest(29, "消息")
			switch mode {
			case "nonmember":
				source = peers[32]
				body = guildChatRequest(32, "消息")
			case "wrong_account":
				source.accountID = "wrong"
			case "stale_session":
				s.gameSessions[29] = &gameSession{conn: &bufferConn{}, selectedCharacterID: 29, characterGeneration: 99}
			case "group_id":
				body[3] = 1
			case "truncated":
				body = body[:len(body)-1]
			case "trailing":
				body = append(body, 1)
			case "repository_owner":
				c, _, _ := repos.Character.Load(context.Background(), "29")
				c.AccountID = "wrong"
				if err := repos.Character.Save(context.Background(), c); err != nil {
					t.Fatal(err)
				}
			case "named_target":
				var extra packetWriter
				extra.writeRawDstr(rosterNameBytes("角色31"))
				body = append(body, extra.bytes()...)
			case "empty":
				body = guildChatRequest(29, "")
			case "long":
				body = guildChatRequest(29, strings.Repeat("a", 256))
			case "nul":
				body = guildChatRequest(29, "hello\x00tail")
			case "member_account":
				r, _ := dnfguild.Load(context.Background(), repos.Settings)
				g, m, _ := r.GuildForCharacter("29")
				m.AccountID = "wrong"
				g.Members["29"] = m
				if err := dnfguild.Save(context.Background(), repos.Settings, r, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.handleCurrentPeerChat(source, 17, body); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, source.conn.(*bufferConn).write.Bytes())
			if packet.Header.MsgID != 17 || !bytes.Equal(packet.Body, []byte{0, 1}) || len(rest) != 0 {
				t.Fatalf("rejection=%x", packet.Body)
			}
			for _, peer := range peers {
				if peer != source && peer.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("rejected text forwarded")
				}
			}
		})
	}
}

func TestGuildChatHintCannotChangeIdentityOrRecipients(t *testing.T) {
	for _, hint := range []uint16{0, 31, 65535} {
		s, peers, _ := guildHallFixture(t)
		message := strings.Repeat("a", 255)
		if err := s.handleCurrentPeerChat(peers[29], 17, guildChatRequest(hint, message)); err != nil {
			t.Fatal(err)
		}
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, peers[33].conn.(*bufferConn).write.Bytes())
		if packet.Header.MsgID != 65 || len(rest) != 0 ||
			!bytes.Contains(packet.Body, rosterNameBytes("角色29")) || !bytes.Contains(packet.Body, []byte(message)) {
			t.Fatalf("hint %d changed source or lost maximum message", hint)
		}
		if peers[31].conn.(*bufferConn).write.Len() != 0 || peers[32].conn.(*bufferConn).write.Len() != 0 {
			t.Fatal("hint redirected guild message")
		}
	}
}

func TestGuildChatMemberRemovalTakesEffectOnNextMessage(t *testing.T) {
	s, peers, repos := guildHallFixture(t)
	if err := s.handleCurrentPeerChat(peers[29], 17, guildChatRequest(29, "first")); err != nil {
		t.Fatal(err)
	}
	before := peers[30].conn.(*bufferConn).write.Len()
	if before == 0 {
		t.Fatal("current member received no first message")
	}
	r, _ := dnfguild.Load(context.Background(), repos.Settings)
	g, _, _ := r.GuildForCharacter("30")
	delete(g.Members, "30")
	if err := dnfguild.Save(context.Background(), repos.Settings, r, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	if err := s.handleCurrentPeerChat(peers[29], 17, guildChatRequest(29, "second")); err != nil {
		t.Fatal(err)
	}
	if peers[30].conn.(*bufferConn).write.Len() != before {
		t.Fatal("removed member received subsequent message")
	}
}

func TestGuildChatConcurrentSendersDoNotDeadlockOrDuplicate(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, id := range []uint16{29, 30} {
		peer := peers[id]
		go func() {
			<-start
			done <- s.handleCurrentPeerChat(peer, 17, guildChatRequest(0, "concurrent"))
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent guild senders deadlocked")
		}
	}
	for _, id := range []uint16{29, 30, 33} {
		wire := peers[id].conn.(*bufferConn).write.Bytes()
		count := 0
		for len(wire) > 0 {
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
			wire = rest
			if packet.Header.MsgID == 65 {
				count++
			}
		}
		if count != 2 {
			t.Fatalf("character %d got %d messages", id, count)
		}
	}
}

type guildChatPausedConn struct {
	bufferConn
	once            sync.Once
	entered, resume chan struct{}
}

func (c *guildChatPausedConn) Write(data []byte) (int, error) {
	c.once.Do(func() { close(c.entered); <-c.resume })
	return c.bufferConn.Write(data)
}

func TestGuildChatPinsOnlyDeliveringRecipientUntilWriteCompletes(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	paused := &guildChatPausedConn{entered: make(chan struct{}), resume: make(chan struct{})}
	peers[30].conn = paused
	var release sync.Once
	unpause := func() { release.Do(func() { close(paused.resume) }) }
	defer unpause()
	done := make(chan error, 1)
	go func() { done <- s.handleCurrentPeerChat(peers[29], 17, guildChatRequest(29, "before replacement")) }()
	select {
	case <-paused.entered:
	case <-time.After(time.Second):
		t.Fatal("chat did not reach delivery")
	}
	replacement := &gameSession{conn: &bufferConn{}, accountID: "different-account", selectedCharacterID: 30, characterGeneration: 999}
	replaced := make(chan struct{})
	go func() { s.promoteResidentGameSession(replacement, 30); close(replaced) }()
	select {
	case <-replaced:
		t.Fatal("recipient binding changed during delivery")
	case <-time.After(30 * time.Millisecond):
	}
	unpause()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("chat did not finish")
	}
	select {
	case <-replaced:
	case <-time.After(time.Second):
		t.Fatal("recipient binding reservation leaked")
	}
	if paused.bufferConn.write.Len() == 0 || replacement.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("snapshot message reached replacement session")
	}
	if err := s.handleCurrentPeerChat(peers[29], 17, guildChatRequest(29, "after replacement")); err != nil {
		t.Fatal(err)
	}
	if replacement.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("new account received guild message")
	}
}

func TestGuildChatReloadsMembershipAndRecipientOwnership(t *testing.T) {
	for _, mode := range []string{"removed", "other_guild", "wrong_account", "repository_owner"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos := guildHallFixture(t)
			ctx := context.Background()
			r, _ := dnfguild.Load(ctx, repos.Settings)
			g, m, _ := r.GuildForCharacter("30")
			switch mode {
			case "removed":
				delete(g.Members, "30")
			case "other_guild":
				other, _, _ := r.GuildForCharacter("31")
				delete(g.Members, "30")
				other.Members["30"] = m
			case "wrong_account":
				peers[30].accountID = "other"
			case "repository_owner":
				c, _, _ := repos.Character.Load(ctx, "30")
				c.AccountID = "other"
				if err := repos.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
			}
			if err := dnfguild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if err := s.handleCurrentPeerChat(peers[29], 17, guildChatRequest(29, "消息")); err != nil {
				t.Fatal(err)
			}
			if peers[30].conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("former member or wrong owner received guild text")
			}
			if peers[33].conn.(*bufferConn).write.Len() == 0 {
				t.Fatal("valid cross-channel member missed message")
			}
			after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if before.Values["state"] != after.Values["state"] {
				t.Fatal("chat transport changed guild points")
			}
		})
	}
}
