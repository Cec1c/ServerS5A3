package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
)

func guildItemRequest(t *testing.T, count int) []byte {
	t.Helper()
	raw := mustCurrentHyperlinkCapture(t, currentHyperlinkCaptureOneBody)
	req, err := decodeCurrentItemHyperlinkRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	items := make([][]byte, count)
	for i := range items {
		items[i] = req.Items[0]
	}
	body := buildCurrentHyperlinkBodyForTest(6, 0xffff, 0, []byte("hello"), items)
	if folder := os.Getenv("DNF_GUILD_ITEM_NATIVE_DIR"); folder != "" {
		captured, err := os.ReadFile(filepath.Join(folder, fmt.Sprintf("request-%d.bin", count)))
		if err != nil || !bytes.Equal(captured, body) {
			t.Fatalf("native request mismatch: %v", err)
		}
		body = captured
	}
	return body
}

func guildItemNotices(t *testing.T, peer *gameSession) [][]byte {
	t.Helper()
	var bodies [][]byte
	for wire := peer.conn.(*bufferConn).write.Bytes(); len(wire) > 0; {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		if packet.Header.MsgID == 371 && packet.Header.Classification == 0 {
			bodies = append(bodies, packet.Body)
		}
		if packet.Header.MsgID == 370 || packet.Header.MsgID == 65 {
			t.Fatalf("wrong guild item route: %d", packet.Header.MsgID)
		}
	}
	return bodies
}

func TestGuildItemHyperlinkNativeCrossChannelDelivery(t *testing.T) {
	for _, count := range []int{1, 2, 5} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, peers, _ := guildHallFixture(t)
			peers[30].townPositionSnapshot.TownID = 38
			request := guildItemRequest(t, count)
			if err := s.handleCurrentItemHyperlinkMessage(peers[29], 424, request); err != nil {
				t.Fatal(err)
			}
			ack, _ := splitCurrentGameServerUpperPacketAuto(t, peers[29].conn.(*bufferConn).write.Bytes())
			if ack.Header.MsgID != 424 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1}) {
				t.Fatalf("ack=%x", ack.Body)
			}
			var w packetWriter
			w.writeBytes([]byte{6, 0, 0, 0})
			w.writeRawDstr(rosterNameBytes("角色29"))
			w.writeByte(peers[29].townActorOwnerChannel)
			w.writeRawDstr([]byte("hello"))
			w.writeByte(byte(count))
			w.writeBytes(request[17:])
			w.writeRawDstr(nil)
			for id, p := range peers {
				notices := guildItemNotices(t, p)
				member := id == 29 || id == 30 || id == 33
				if member {
					if len(notices) != 1 || !bytes.Equal(notices[0], w.bytes()) {
						t.Fatalf("member%d notices=%x expected=%x", id, notices, w.bytes())
					}
				} else if p.conn.(*bufferConn).write.Len() != 0 {
					t.Fatalf("leaked to%d", id)
				}
			}
			if dir := os.Getenv("DNF_GUILD_ITEM_NATIVE_DIR"); dir != "" {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("notice-%d.bin", count)), w.bytes(), 0644); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestGuildItemHyperlinkRejectsInvalidIdentityAndBody(t *testing.T) {
	for _, mode := range []string{"nonmember", "wrong-account", "stale", "member-account", "reserved", "empty", "long", "nul", "count0", "count6", "truncated", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos := guildHallFixture(t)
			p := peers[29]
			body := guildItemRequest(t, 1)
			switch mode {
			case "nonmember":
				p = peers[32]
			case "wrong-account":
				p.accountID = "different"
			case "stale":
				s.unbindGameSession(p)
			case "member-account":
				r, _ := guild.Load(context.Background(), repos.Settings)
				g, m, _ := r.GuildForCharacter("29")
				m.AccountID = "different"
				g.Members["29"] = m
				if err := guild.Save(context.Background(), repos.Settings, r, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
			case "reserved":
				body[3] = 1
			case "empty":
				body = buildCurrentHyperlinkBodyForTest(6, 0, 0, nil, [][]byte{body[17:]})
			case "long":
				body = buildCurrentHyperlinkBodyForTest(6, 0, 0, bytes.Repeat([]byte{'a'}, 256), [][]byte{body[17:]})
			case "nul":
				body[11] = 0
			case "count0":
				body[16] = 0
			case "count6":
				body[16] = 6
			case "truncated":
				body = body[:len(body)-1]
			case "trailing":
				body = append(body, 0)
			}
			if err := s.handleCurrentItemHyperlinkMessage(p, 424, body); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
			if !bytes.Equal(ack.Body, []byte{0, 1}) || len(rest) != 0 {
				t.Fatalf("rejection=%x rest=%x", ack.Body, rest)
			}
			for _, peer := range peers {
				if peer != p && peer.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("rejected request forwarded")
				}
			}
		})
	}
}

func TestGuildItemHyperlinkQueuedRecipientRevalidated(t *testing.T) {
	for _, mode := range []string{"session", "removed", "rejoined", "account"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos := guildHallFixture(t)
			startGuildChatTestEvents(t, s, peers)
			p := peers[30]
			resume := pauseGuildChatRecipient(t, s, p)
			if err := s.handleCurrentItemHyperlinkMessage(peers[29], 424, guildItemRequest(t, 1)); err != nil {
				t.Fatal(err)
			}
			r, _ := guild.Load(context.Background(), repos.Settings)
			g, m, _ := r.GuildForCharacter("30")
			switch mode {
			case "session":
				s.promoteResidentGameSession(&gameSession{conn: &bufferConn{}, accountID: p.accountID, selectedCharacterID: 30, characterGeneration: 999}, 30)
			case "removed":
				delete(g.Members, "30")
			case "rejoined":
				m.JoinedAt = m.JoinedAt.Add(time.Second)
				g.Members["30"] = m
			case "account":
				m.AccountID = "different"
				g.Members["30"] = m
			}
			if mode != "session" {
				if err := guild.Save(context.Background(), repos.Settings, r, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
			}
			resume()
			flushGuildChatTestEvents(t, s, p)
			flushGuildChatTestEvents(t, s, peers[33])
			if len(guildItemNotices(t, p)) != 0 || len(guildItemNotices(t, peers[33])) != 1 {
				t.Fatal("stale or healthy delivery mismatch")
			}
		})
	}
}

func TestGuildItemHyperlinkSlowRecipientIsolated(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	peers[30].conn = server
	startGuildChatTestEvents(t, s, peers)
	done := make(chan error, 1)
	body := guildItemRequest(t, 1)
	go func() { done <- s.handleCurrentItemHyperlinkMessage(peers[29], 424, body) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		client.Close()
		<-done
		t.Fatal("slow recipient blocked sender")
	}
	flushGuildChatTestEvents(t, s, peers[33])
	if len(guildItemNotices(t, peers[33])) != 1 {
		t.Fatal("healthy recipient missing")
	}
	client.Close()
	flushGuildChatTestEvents(t, s, peers[30])
}
