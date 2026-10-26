package dnfbridge

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
)

func partyChatRequest(id uint16, text string) []byte {
	b := guildChatRequest(id, text)
	b[0] = 2
	return b
}

func partyChatGroup(t *testing.T, s *Service, peers map[uint16]*gameSession, ids ...uint16) uint16 {
	t.Helper()
	manager := s.runtimePartyManagerForService()
	member := func(id uint16) party.RuntimePartyMember {
		i, ok := s.boundGameSessionCharacterSnapshot(peers[id])
		if !ok {
			t.Fatal("binding missing")
		}
		m, ok := s.runtimePartyMemberForIdentity(i)
		if !ok {
			t.Fatal("member missing")
		}
		return m
	}
	r := manager.Create(member(ids[0]), alignedcmd.PartyState{MaxMembers: 4})
	if !r.OK {
		t.Fatal(r)
	}
	for _, id := range ids[1:] {
		if result := manager.Join(r.Party.ID, member(id)); !result.OK {
			t.Fatal(result)
		}
	}
	return r.Party.ID
}

func TestPartyChatNativeRequestRoutesOnlyToCurrentMembers(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	// No scene actors are registered. The recipient can be in another room
	// and another guild, while the same-guild bystander must not receive.
	partyChatGroup(t, s, peers, 29, 31)
	request := partyChatRequest(0xffff, "hello")
	if dir := os.Getenv("DNF_PARTY_CHAT_NATIVE_DIR"); dir != "" {
		if native, err := os.ReadFile(filepath.Join(dir, "sender-2.bin")); err == nil {
			request = native
		}
	}
	if handled, err := s.handleOnlineSocialCommand(peers[29], 17, request); err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	wire := peers[29].conn.(*bufferConn).write.Bytes()
	ack, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
	if ack.Header.Classification != 1 || ack.Header.MsgID != 17 || !bytes.Equal(ack.Body, []byte{1}) {
		t.Fatalf("party ACK=%x", ack.Body)
	}
	echo, rest := splitCurrentGameServerUpperPacketAuto(t, rest)
	channel, _ := currentSessionOwnerChannelField(peers[29])
	want := buildCurrentPartyChatBody(channel, rosterNameBytes("角色29"), []byte("hello"))
	if echo.Header.Classification != 0 || echo.Header.MsgID != 65 || !bytes.Equal(echo.Body, want) || len(rest) != 0 {
		t.Fatalf("echo=%+v", echo)
	}
	notice, rest := splitCurrentGameServerUpperPacketAuto(t, peers[31].conn.(*bufferConn).write.Bytes())
	if !bytes.Equal(notice.Body, want) || len(rest) != 0 {
		t.Fatalf("recipient=%x", notice.Body)
	}
	for _, id := range []uint16{30, 32, 33} {
		requireGuildChatNotice(t, peers[id], 0)
	}
	if dir := os.Getenv("DNF_PARTY_CHAT_NATIVE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "party-65.bin"), echo.Body, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPartyChatLeaveStaleProjectionAndRegroup(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	id := partyChatGroup(t, s, peers, 29, 30)
	snapshot, _ := s.runtimePartyManagerForService().SnapshotByID(id)
	storeRuntimePartyState(peers[29], snapshot.StateFor(29))
	s.runtimePartyManagerForService().LeaveCurrent(29)
	if err := s.handleCurrentPeerChat(peers[29], 17, partyChatRequest(29, "left")); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitCurrentGameServerUpperPacketAuto(t, peers[29].conn.(*bufferConn).write.Bytes())
	if !bytes.Equal(ack.Body, []byte{0, 1}) || len(rest) != 0 {
		t.Fatalf("stale projection accepted=%x", ack.Body)
	}
	requireGuildChatNotice(t, peers[30], 0)
	peers[29].conn.(*bufferConn).write.Reset()
	partyChatGroup(t, s, peers, 29, 31)
	if err := s.handleCurrentPeerChat(peers[29], 17, partyChatRequest(29, "new party")); err != nil {
		t.Fatal(err)
	}
	requireGuildChatNotice(t, peers[29], 1)
	requireGuildChatNotice(t, peers[31], 1)
	requireGuildChatNotice(t, peers[30], 0)
	// Guild chat still targets the guild after party leave/regroup.
	if err := s.handleCurrentPeerChat(peers[29], 17, guildChatRequest(29, "guild")); err != nil {
		t.Fatal(err)
	}
	requireGuildChatNotice(t, peers[30], 1)
	requireGuildChatNotice(t, peers[33], 1)
	requireGuildChatNotice(t, peers[31], 1)
}

func TestPartyChatRejectsMalformedAndWrongOwner(t *testing.T) {
	for _, kind := range []string{"empty", "long", "nul", "unknown", "target", "trailing", "wrong_owner", "solo"} {
		t.Run(kind, func(t *testing.T) {
			s, peers, repos := guildHallFixture(t)
			if kind != "solo" {
				partyChatGroup(t, s, peers, 29, 30)
			}
			request, _ := decodeCurrentPeerChatRequest(partyChatRequest(29, "hello"))
			switch kind {
			case "empty":
				request.Message = nil
			case "long":
				request.Message = bytes.Repeat([]byte("a"), 256)
			case "nul":
				request.Message = []byte{'a', 0}
			case "unknown":
				request.Unknown = 1
			case "target":
				request.TargetName = []byte("target")
			case "trailing":
				request.HasTrailingValue = true
			case "wrong_owner":
				actor, _, _ := repos.Character.Load(context.Background(), "29")
				actor.AccountID = "other"
				if err := repos.Character.Save(context.Background(), actor); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.handleCurrentPartyChat(peers[29], 17, request); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitCurrentGameServerUpperPacketAuto(t, peers[29].conn.(*bufferConn).write.Bytes())
			if !bytes.Equal(ack.Body, []byte{0, 1}) || len(rest) != 0 {
				t.Fatal("invalid request accepted")
			}
			requireGuildChatNotice(t, peers[30], 0)
		})
	}
}

func TestPartyChatQueuedRecipientLeaveAndRebind(t *testing.T) {
	for _, kind := range []string{"leave", "regroup", "rebind"} {
		t.Run(kind, func(t *testing.T) {
			s, peers, _ := guildHallFixture(t)
			id := partyChatGroup(t, s, peers, 29, 30)
			recipient := peers[30]
			old, _ := s.boundGameSessionCharacterSnapshot(peers[30])
			switch kind {
			case "leave":
				s.runtimePartyManagerForService().LeaveCurrent(30)
			case "regroup":
				partyChatGroup(t, s, peers, 30, 31)
			case "rebind":
				s.bindGameSessionCharacter(peers[30], 32)
			}
			if err := s.queueCurrentPartyChat(old, id, buildCurrentPartyChatBody(11, []byte("alice"), []byte("late"))); err != nil {
				t.Fatal(err)
			}
			if recipient.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("late message reached departed/rebound member")
			}
		})
	}
}

func TestPartyChatSlowPeerDoesNotBlockOtherMembers(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	peers[33].channel = peers[29].channel
	partyChatGroup(t, s, peers, 29, 30, 33)
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	peers[30].conn = server
	startGuildChatTestEvents(t, s, peers)
	done := make(chan error, 1)
	go func() { done <- s.handleCurrentPeerChat(peers[29], 17, partyChatRequest(29, "slow")) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		client.Close()
		<-done
		t.Fatal("slow peer blocked sender")
	}
	flushGuildChatTestEvents(t, s, peers[33])
	requireGuildChatNotice(t, peers[33], 1)
	client.Close()
}
