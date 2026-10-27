package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/eventloop"
)

func startGuildChatTestEvents(t *testing.T, s *Service, peers map[uint16]*gameSession) {
	t.Helper()
	for _, id := range []uint16{29, 30, 33} {
		p := peers[id]
		ctx, cancel := context.WithCancel(context.Background())
		p.ctx = ctx
		if err := s.startGameSessionEvents(ctx, p); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cancel()
			stop, c := context.WithTimeout(context.Background(), time.Second)
			defer c()
			if err := s.stopGameSessionEvents(stop, p); err != nil {
				t.Error(err)
			}
		})
	}
}

func flushGuildChatTestEvents(t *testing.T, s *Service, p *gameSession) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.callGameSession(ctx, p, "chat-test-barrier", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func requireGuildChatNotice(t *testing.T, p *gameSession, count int) {
	t.Helper()
	got := 0
	for wire := p.conn.(*bufferConn).write.Bytes(); len(wire) > 0; {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		if packet.Header.Classification == 0 && packet.Header.MsgID == 65 {
			got++
		}
	}
	if got != count {
		t.Fatalf("character=%d notices=%d want=%d", p.selectedCharacterID, got, count)
	}
}

func TestGuildChatBusyRecipientDoesNotRejectHealthyMembers(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	startGuildChatTestEvents(t, s, peers)
	identity, _ := s.boundGameSessionCharacterSnapshot(peers[30])
	busy, err := s.reserveCurrentCharacterAssets(context.Background(), identity, peers[30].accountID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.releaseCurrentCharacterAssets(busy)
	start := time.Now()
	if err = s.handleCurrentPeerChat(peers[29], 17, guildChatRequest(29, "busy peer")); err != nil {
		t.Fatal(err)
	}
	packet, _ := splitCurrentGameServerUpperPacketAuto(t, peers[29].conn.(*bufferConn).write.Bytes())
	if !bytes.Equal(packet.Body, []byte{1}) {
		t.Fatalf("another member's reservation rejected sender: %x", packet.Body)
	}
	if time.Since(start) > time.Second {
		t.Fatal("sender waited for recipient reservation")
	}
	flushGuildChatTestEvents(t, s, peers[33])
	requireGuildChatNotice(t, peers[33], 1)
	s.releaseCurrentCharacterAssets(busy)
	flushGuildChatTestEvents(t, s, peers[30])
	requireGuildChatNotice(t, peers[30], 1)
}

func TestGuildChatSlowRecipientDoesNotHoldOtherMembers(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	peers[30].conn = server
	startGuildChatTestEvents(t, s, peers)
	done := make(chan error, 1)
	go func() { done <- s.handleCurrentPeerChat(peers[29], 17, guildChatRequest(29, "slow peer")) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		client.Close()
		<-done
		t.Fatal("one unread socket blocked sender")
	}
	flushGuildChatTestEvents(t, s, peers[33])
	requireGuildChatNotice(t, peers[33], 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, id := range []uint16{29, 33} {
		identity, _ := s.boundGameSessionCharacterSnapshot(peers[id])
		reservation, err := s.reserveCurrentCharacterAssets(ctx, identity, peers[id].accountID)
		if err != nil {
			t.Fatalf("healthy member %d pinned by slow peer: %v", id, err)
		}
		s.releaseCurrentCharacterAssets(reservation)
	}
	flushGuildChatTestEvents(t, s, peers[30])
	if _, err := client.Write([]byte{1}); err == nil {
		t.Fatal("timed-out stream remained reusable")
	}
}

func pauseGuildChatRecipient(t *testing.T, s *Service, p *gameSession) func() {
	t.Helper()
	entered, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unpause := func() { once.Do(func() { close(resume) }) }
	t.Cleanup(unpause)
	if err := s.postGameSessionCharacterEvent(p, "chat-test-pause", p.selectedCharacterID, p.characterGeneration, func() error { close(entered); <-resume; return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("receiver loop did not pause")
	}
	return unpause
}

func TestGuildChatQueuedRecipientRevalidatesIdentityAndMembership(t *testing.T) {
	for _, mode := range []string{"replaced-session", "removed-member", "other-guild", "rejoined", "wrong-account"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos := guildHallFixture(t)
			startGuildChatTestEvents(t, s, peers)
			p := peers[30]
			resume := pauseGuildChatRecipient(t, s, p)
			if err := s.handleCurrentPeerChat(peers[29], 17, guildChatRequest(29, "queued")); err != nil {
				t.Fatal(err)
			}
			reg, err := dnfguild.Load(context.Background(), repos.Settings)
			if err != nil {
				t.Fatal(err)
			}
			g, m, _ := reg.GuildForCharacter("30")
			switch mode {
			case "replaced-session":
				replacement := &gameSession{conn: &bufferConn{}, accountID: p.accountID, selectedCharacterID: 30, characterGeneration: 999}
				s.promoteResidentGameSession(replacement, 30)
			case "removed-member":
				delete(g.Members, "30")
			case "other-guild":
				other, _, _ := reg.GuildForCharacter("31")
				delete(g.Members, "30")
				other.Members["30"] = m
			case "rejoined":
				m.JoinedAt = m.JoinedAt.Add(time.Second)
				g.Members["30"] = m
			case "wrong-account":
				m.AccountID = "different"
				g.Members["30"] = m
			}
			if mode != "replaced-session" {
				if err = dnfguild.Save(context.Background(), repos.Settings, reg, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
			}
			resume()
			flushGuildChatTestEvents(t, s, p)
			requireGuildChatNotice(t, p, 0)
			flushGuildChatTestEvents(t, s, peers[33])
			requireGuildChatNotice(t, peers[33], 1)
		})
	}
}

func TestGuildChatFullRecipientQueueDoesNotWaitOrCancelOthers(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	startGuildChatTestEvents(t, s, peers)
	resume := pauseGuildChatRecipient(t, s, peers[30])
	for i := 0; i < gameSessionEventQueueSize; i++ {
		if err := peers[30].events.loop.Submit(eventloop.Event{Type: "chat-test-full", Priority: eventloop.PriorityMedium, Data: gameSessionEventTask{run: func() error { return nil }}}); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	if err := s.handleCurrentPeerChat(peers[29], 17, guildChatRequest(29, "full queue")); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("full recipient queue blocked sender")
	}
	flushGuildChatTestEvents(t, s, peers[33])
	requireGuildChatNotice(t, peers[33], 1)
	resume()
	flushGuildChatTestEvents(t, s, peers[30])
	requireGuildChatNotice(t, peers[30], 0)
}

func TestGuildChatWireWaitCancellationPreservesUnusedStream(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	p := peers[29]
	p.wireMu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := s.sendCurrentGuildChatFrame(ctx, p, 65, []byte{6}, 0)
	p.wireMu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if p.upperSeq != 0 || p.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("expired wait touched wire or sequence")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err = s.sendCurrentGuildChatFrame(ctx2, p, 65, []byte{6}, 0); err != nil {
		t.Fatal(err)
	}
}

func TestGuildChatQueuedPacketUsesOwnedBytes(t *testing.T) {
	s, peers, repos := guildHallFixture(t)
	startGuildChatTestEvents(t, s, peers)
	p := peers[30]
	resume := pauseGuildChatRecipient(t, s, p)
	reg, _ := dnfguild.Load(context.Background(), repos.Settings)
	g, m, _ := reg.GuildForCharacter("30")
	identity, _ := s.boundGameSessionCharacterSnapshot(p)
	body := buildCurrentGuildChatBody(11, []byte("sender"), []byte("original"))
	expected := append([]byte(nil), body...)
	if err := s.queueCurrentGuildChat(currentGuildChatRecipient{identity: identity, account: m.AccountID, joinedAt: m.JoinedAt}, g.ID, body); err != nil {
		t.Fatal(err)
	}
	for i := range body {
		body[i] = 0
	}
	resume()
	flushGuildChatTestEvents(t, s, p)
	packet, _ := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
	if !bytes.Equal(packet.Body, expected) {
		t.Fatal("queued message retained mutable caller buffer")
	}
}

func TestGuildChatRechecksMembershipAfterWireWait(t *testing.T) {
	s, peers, repos := guildHallFixture(t)
	p := peers[30]
	reg, _ := dnfguild.Load(context.Background(), repos.Settings)
	g, m, _ := reg.GuildForCharacter("30")
	identity, _ := s.boundGameSessionCharacterSnapshot(p)
	p.wireMu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	peer := currentGuildChatRecipient{identity: identity, account: m.AccountID, joinedAt: m.JoinedAt}
	guildID := g.ID
	go func() {
		done <- s.deliverCurrentGuildChat(ctx, peer, guildID, []byte{6})
	}()
	// Observe the receiver's reservation before changing membership, so the
	// delivery is already pending behind the held wire lock.
	for {
		s.mu.Lock()
		waiting := s.characterAssetReservations[identity.character] != nil
		s.mu.Unlock()
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			p.wireMu.Unlock()
			<-done
			t.Fatal("delivery did not reach wire wait")
		case <-time.After(time.Millisecond):
		}
	}
	delete(g.Members, "30")
	if err := dnfguild.Save(context.Background(), repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	p.wireMu.Unlock()
	if err := <-done; !errors.Is(err, dnfguild.ErrNotMember) {
		t.Fatal(err)
	}
	if p.upperSeq != 0 || p.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("departed member received pending text")
	}
}

type guildChatFailedWriteConn struct {
	bufferConn
	closed bool
}

func (c *guildChatFailedWriteConn) Write(b []byte) (int, error) {
	return len(b) / 2, io.ErrUnexpectedEOF
}
func (c *guildChatFailedWriteConn) Close() error { c.closed = true; return nil }

func TestGuildChatFailedWriteClosesOnlyAffectedStream(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	failed := &guildChatFailedWriteConn{}
	peers[30].conn = failed
	if err := s.handleCurrentPeerChat(peers[29], 17, guildChatRequest(29, "write failure")); err != nil {
		t.Fatal(err)
	}
	if !failed.closed {
		t.Fatal("partially written stream retained")
	}
	requireGuildChatNotice(t, peers[29], 1)
	requireGuildChatNotice(t, peers[33], 1)
	s.mu.Lock()
	held := len(s.characterAssetReservations)
	s.mu.Unlock()
	if held != 0 {
		t.Fatalf("leaked reservations: %d", held)
	}
}

func TestGuildChatConcurrentProductionQueues(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	startGuildChatTestEvents(t, s, peers)
	done := make(chan error, 2)
	for _, id := range []uint16{29, 33} {
		p := peers[id]
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done <- s.callGameSession(ctx, p, "chat-test-send", func() error { return s.handleCurrentPeerChat(p, 17, guildChatRequest(0, "concurrent")) })
		}()
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []uint16{29, 30, 33} {
		flushGuildChatTestEvents(t, s, peers[id])
		requireGuildChatNotice(t, peers[id], 2)
	}
}

type guildChatDeadlineConn struct {
	bufferConn
	deadline time.Time
}

func (c *guildChatDeadlineConn) SetWriteDeadline(d time.Time) error { c.deadline = d; return nil }

func TestGuildChatUsesExistingDprotoAndRestoresDeadline(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	p := peers[29]
	conn := &guildChatDeadlineConn{}
	p.conn = conn
	provider := &testDprotoSession{}
	p.dproto = provider
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	body := buildCurrentGuildChatBody(11, []byte("sender"), []byte("text"))
	if err := s.sendCurrentGuildChatFrame(ctx, p, 65, body, 0); err != nil {
		t.Fatal(err)
	}
	if len(provider.encodeInputs) != 1 || bytes.Equal(provider.encodeInputs[0], conn.write.Bytes()) {
		t.Fatal("chat bypassed DPROTO")
	}
	if !conn.deadline.IsZero() {
		t.Fatal("deadline leaked into later game writes")
	}
	if p.upperSeq != 1 {
		t.Fatal("sequence not advanced once")
	}
}

func TestGuildChatContributionProjectionMatchesOrdinaryProjection(t *testing.T) {
	for _, mode := range []string{"inventory", "mail"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos := guildMileageFixture(t)
			contributionBridgeCatalog(t, s)
			contributionMatureBridgeMembers(t, s, repos)
			ctx := context.Background()
			if mode == "mail" {
				inv, _, _ := repos.Inventory.Load(ctx, "29")
				inv.CharacterID = "29"
				if inv.Slots == nil {
					inv.Slots = map[string]r.ItemStack{}
				}
				for slot := 177; slot <= 232; slot++ {
					inv.Slots[fmt.Sprintf("0:%d", slot)] = r.ItemStack{ItemID: int64(1000 + slot), Count: 1}
				}
				if err := repos.Inventory.Save(ctx, inv); err != nil {
					t.Fatal(err)
				}
			}
			identity, _ := s.boundGameSessionCharacterSnapshot(p)
			reservation, err := s.reserveCurrentCharacterAssets(ctx, identity, p.accountID)
			if err != nil {
				t.Fatal(err)
			}
			income, err := s.grantCurrentGuildContributionReserved(ctx, p, identity, dnfguild.ContributionChat)
			s.releaseCurrentCharacterAssets(reservation)
			if err != nil {
				t.Fatal(err)
			}
			if income.Points != 2 {
				t.Fatal(income)
			}
			if err = s.projectCurrentGuildContribution(p, income); err != nil {
				t.Fatal(err)
			}
			conn := p.conn.(*bufferConn)
			expected := append([]byte(nil), conn.write.Bytes()...)
			conn.write.Reset()
			p.upperSeq = 0
			if err = s.projectCurrentGuildChatContribution(identity, income); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(expected, conn.write.Bytes()) {
				t.Fatalf("projection drift: old=%x new=%x", expected, conn.write.Bytes())
			}
		})
	}
}
