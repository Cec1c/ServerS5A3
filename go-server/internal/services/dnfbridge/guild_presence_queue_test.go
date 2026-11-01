package dnfbridge

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/platform/eventloop"
)

type guildPresenceQueueConn struct {
	*bufferConn
	closed atomic.Bool
}

func (c *guildPresenceQueueConn) Close() error {
	c.closed.Store(true)
	return c.bufferConn.Close()
}

func TestGuildPresenceReturnSelectDoesNotWaitForPeerOwner(t *testing.T) {
	s, peers, repos, now := guildPresenceFixture(t)
	peer := peers[0]
	peer.guild.memberListRequested = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peer.ctx = ctx
	if err := s.startGameSessionEvents(ctx, peer); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, peer)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	if err := s.tryPostGameSessionCharacterEvent(peer, "test-held-guild-owner", peer.selectedCharacterID, peer.characterGeneration, func() error {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	<-entered
	// A live asset reservation makes the old synchronous fanout wait until its
	// three-second context expires. Keep it held until after the logout ACK.
	identity, _ := s.boundGameSessionCharacterSnapshot(peer)
	reservation, err := s.reserveCurrentCharacterAssets(ctx, identity, peer.accountID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.releaseCurrentCharacterAssets(reservation)
	go func() { _ = s.handleUpperReturnSelectCharacter(peers[1], nil); close(finished) }()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("return-select waits on another guild member")
	}
	if got := guildPresenceLastSeen(t, repos, "2"); !got.Equal(now) {
		t.Fatal("ACK preceded durable last-seen")
	}
	s.releaseCurrentCharacterAssets(reservation)
	close(release)
	drained := make(chan struct{})
	if err := s.tryPostGameSessionCharacterEvent(peer, "test-guild-refresh-drained", peer.selectedCharacterID, peer.characterGeneration, func() error { close(drained); return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("refresh did not drain")
	}
	if channel, _ := guildPresenceWireRow(t, guildReply(t, peer, 140), 2); channel != 255 {
		t.Fatal("queued roster did not reflect logout")
	}
}

func TestGuildPresenceQueuedRefreshDropsRebindGeneration(t *testing.T) {
	s, peers, repos, _ := guildPresenceFixture(t)
	peer := peers[1]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peer.ctx = ctx
	if err := s.startGameSessionEvents(ctx, peer); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, peer)

	registry, err := dnfguild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	guild, _, found := registry.GuildForCharacter("2")
	if !found {
		t.Fatal("guild fixture missing character 2")
	}

	entered, release := make(chan struct{}), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	if err := s.postGameSessionCharacterEvent(peer, "test-guild-generation-gate", peer.selectedCharacterID, peer.characterGeneration, func() error {
		close(entered)
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	waitTestSignal(t, entered, "guild generation gate did not start")

	before := peer.events.loop.MetricsSnapshot().Submitted[eventloop.PriorityMedium]
	s.queueCurrentGuildPresenceRefresh(ctx, guild.ID)
	after := peer.events.loop.MetricsSnapshot().Submitted[eventloop.PriorityMedium]
	if after != before+1 {
		t.Fatalf("queued guild refresh submissions=%d want=%d", after-before, 1)
	}

	// The queued refresh still carries character 2's generation. Rebind the same
	// socket to an unrelated character while it is waiting; releasing the queue
	// must skip the stale refresh instead of projecting guild state to the new
	// selection (or back to the old character).
	s.resetGameSessionForCharacterSelect(peer)
	s.bindGameSessionCharacter(peer, 99)
	close(release)
	released = true
	if err := s.callGameSession(ctx, peer, "test-guild-generation-drain", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := peer.conn.(*bufferConn).write.Len(); got != 0 {
		t.Fatalf("stale guild refresh wrote %d bytes after rebind", got)
	}
}

func TestGuildPresenceFullQueueDoesNotCloseSessionOrBlockReturnSelect(t *testing.T) {
	s, peers, _, _ := guildPresenceFixture(t)
	peer := peers[1]
	base := peer.conn.(*bufferConn)
	conn := &guildPresenceQueueConn{bufferConn: base}
	peer.conn = conn
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peer.ctx = ctx
	if err := s.startGameSessionEvents(ctx, peer); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, peer)

	entered, release := make(chan struct{}), make(chan struct{})
	released := false
	gateDone := make(chan error, 1)
	defer func() {
		if !released {
			close(release)
		}
		select {
		case err := <-gateDone:
			if err != nil {
				t.Errorf("release guild full-queue gate: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("guild full-queue gate did not exit")
		}
	}()
	go func() {
		gateDone <- s.callGameSession(ctx, peer, "test-guild-full-queue-gate", func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	waitTestSignal(t, entered, "guild full-queue gate did not start")
	for i := 0; i < gameSessionEventQueueSize; i++ {
		if err := peer.events.loop.Submit(eventloop.Event{
			Type:     "test-guild-full-queue",
			Priority: eventloop.PriorityMedium,
			Context:  ctx,
			Data: gameSessionEventTask{
				source: "test-guild-full-queue",
				run:    func() error { return nil },
			},
		}); err != nil {
			t.Fatalf("fill guild queue %d/%d: %v", i+1, gameSessionEventQueueSize, err)
		}
	}
	if got := peer.events.loop.QueueLengths().Medium; got != gameSessionEventQueueSize {
		t.Fatalf("guild queue length=%d want=%d", got, gameSessionEventQueueSize)
	}

	started := time.Now()
	returned := make(chan error, 1)
	go func() { returned <- s.handleUpperReturnSelectCharacter(peer, nil) }()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("full guild audience queue blocked return-select")
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("return-select took %s with a full guild audience queue", elapsed)
	}
	if conn.closed.Load() {
		t.Fatal("full guild audience queue closed the selecting session")
	}
	if !peer.events.active.Load() {
		t.Fatal("full guild audience queue deactivated the selecting session")
	}
	if peer.selectedCharacterID != 0 {
		t.Fatalf("return-select kept character %d selected", peer.selectedCharacterID)
	}
	if base.write.Len() == 0 {
		t.Fatal("return-select acknowledgement was not written")
	}

	close(release)
	released = true
}
