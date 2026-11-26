package dnfbridge

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type p5SlowDeadlineConn struct {
	*bufferConn
	mu         sync.Mutex
	deadline   time.Time
	lastBudget time.Duration
	writeDelay time.Duration
	closed     atomic.Bool
}

func (c *p5SlowDeadlineConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	if !t.IsZero() {
		c.lastBudget = time.Until(t)
	}
	c.mu.Unlock()
	return nil
}

func (c *p5SlowDeadlineConn) Write(p []byte) (int, error) {
	if c.writeDelay > 0 {
		time.Sleep(c.writeDelay)
	}
	c.mu.Lock()
	d := c.deadline
	c.mu.Unlock()
	if !d.IsZero() && time.Now().After(d) {
		return 0, context.DeadlineExceeded
	}
	return c.bufferConn.Write(p)
}

func (c *p5SlowDeadlineConn) Close() error {
	c.closed.Store(true)
	return c.bufferConn.Close()
}

func (c *p5SlowDeadlineConn) observedBudget() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastBudget
}

func TestP5GuildChatAdmittedWriteUsesIndependentTransportBudget(t *testing.T) {
	s, peers, _ := guildHallFixture(t)
	p := peers[30]
	slowConn := &p5SlowDeadlineConn{
		bufferConn: p.conn.(*bufferConn),
		writeDelay: 60 * time.Millisecond,
	}
	p.conn = slowConn

	// Prep ctx has 50ms total; validate consumes 25ms (leaving ~25ms < 60ms writeDelay).
	// Because the frame passes validation before ctx expires, the admitted socket
	// write must use the independent 30s transport budget and must NOT close p.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	body := buildCurrentGuildChatBody(11, []byte("sender"), []byte("budget-ok"))
	err := s.sendCurrentGuildChatFrameChecked(ctx, p, 65, body, 0, func() error {
		time.Sleep(25 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatalf("admitted guild chat write failed despite independent transport budget: %v", err)
	}
	if budget := slowConn.observedBudget(); budget < 20*time.Second {
		t.Fatalf("guild chat write budget=%v want >= 20s independent transport budget", budget)
	}
	if slowConn.closed.Load() {
		t.Fatal("admitted guild chat write closed healthy recipient connection")
	}
	if p.upperSeq != 1 {
		t.Fatalf("upperSeq=%d want=1", p.upperSeq)
	}

	// If ctx already expires BEFORE wire admission (during validate), the call must
	// return context.DeadlineExceeded cleanly without advancing upperSeq, writing
	// bytes, or closing the connection.
	writtenBefore := slowConn.bufferConn.write.Len()
	expiredCtx, expiredCancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer expiredCancel()
	err = s.sendCurrentGuildChatFrameChecked(expiredCtx, p, 65, body, 0, func() error {
		time.Sleep(30 * time.Millisecond)
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pre-admission expired ctx err=%v want context.DeadlineExceeded", err)
	}
	if p.upperSeq != 1 || slowConn.bufferConn.write.Len() != writtenBefore {
		t.Fatalf("pre-admission expired ctx touched wire or seq: seq=%d bytes=%d->%d",
			p.upperSeq, writtenBefore, slowConn.bufferConn.write.Len())
	}
	if slowConn.closed.Load() {
		t.Fatal("pre-admission expired ctx closed healthy recipient connection")
	}
}

func TestP5GuildRosterPrimeAdmittedWriteUsesIndependentTransportBudget(t *testing.T) {
	f, repos, viewer := setupP1WeddingGuildRosterFixture(t)
	s := f.service
	ctx := context.Background()

	f.target.channel.ID, f.target.connectionTownActorOwnerChannel = 11, 11
	slowConn := &p5SlowDeadlineConn{
		bufferConn: f.targetConn,
		writeDelay: 60 * time.Millisecond,
	}
	f.target.conn = slowConn

	origGuildTimeout := testGuildContextTimeout
	origHeadway := guildSameChannelPrimeMinCtxHeadway
	origMaxDur := guildSameChannelPrimeMaxDuration
	testGuildContextTimeout = 45 * time.Millisecond
	guildSameChannelPrimeMinCtxHeadway = 0
	guildSameChannelPrimeMaxDuration = 100 * time.Millisecond
	t.Cleanup(func() {
		testGuildContextTimeout = origGuildTimeout
		guildSameChannelPrimeMinCtxHeadway = origHeadway
		guildSameChannelPrimeMaxDuration = origMaxDur
	})

	// 1. Full guild member list request triggers wedding roster priming with 45ms
	// guild query ctx while TCP Write takes 60ms. Must succeed on 30s transport
	// budget without closing the viewer's socket.
	if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
		t.Fatalf("guild roster with wedding prime failed despite independent transport budget: %v", err)
	}
	if budget := slowConn.observedBudget(); budget < 20*time.Second {
		t.Fatalf("wedding roster prime write budget=%v want >= 20s", budget)
	}
	if slowConn.closed.Load() {
		t.Fatal("wedding roster prime closed healthy viewer connection")
	}
	if got := countSilentMode8Frames(t, slowConn.bufferConn.write.Bytes()); got < 1 {
		t.Fatalf("expected >= 1 wedding mode8 prime frame, got %d", got)
	}

	// 2. Same-channel peer priming path (wedding inactive) with 35ms caller ctx
	// and 60ms TCP Write must also complete cleanly on the 30s transport budget.
	viewer.Stats["wedding_v1_phase"] = 0
	if err := dnfrepo.SaveCharacterFields(ctx, repos.Character, viewer, dnfrepo.CharacterFieldWedding); err != nil {
		t.Fatal(err)
	}
	f.target.guild.mu.Lock()
	f.target.guild.weddingPrimedPeers = nil
	f.target.guild.mu.Unlock()
	slowConn.bufferConn.write.Reset()
	slowConn.mu.Lock()
	slowConn.lastBudget = 0
	slowConn.mu.Unlock()

	bound, ok := s.boundGameSessionCharacterSnapshot(f.target)
	if !ok {
		t.Fatal("missing bound snapshot")
	}
	display, err := s.readCurrentGuildDisplay(ctx, bound, false, true)
	if err != nil {
		t.Fatal(err)
	}
	shortCtx, shortCancel := context.WithTimeout(ctx, 35*time.Millisecond)
	defer shortCancel()
	s.primeCurrentSameChannelGuildPartyPeers(shortCtx, bound, display)
	if budget := slowConn.observedBudget(); budget < 20*time.Second {
		t.Fatalf("same-channel peer prime write budget=%v want >= 20s", budget)
	}
	if slowConn.closed.Load() {
		t.Fatal("same-channel peer prime closed healthy viewer connection")
	}
	if got := countSilentMode8Frames(t, slowConn.bufferConn.write.Bytes()); got != 1 {
		t.Fatalf("expected 1 same-channel mode8 prime frame, got %d", got)
	}
}
