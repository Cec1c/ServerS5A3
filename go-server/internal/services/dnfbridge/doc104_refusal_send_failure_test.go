package dnfbridge

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type doc104RefusalSendConn struct {
	net.Conn
	writeErr error
	closeErr error
	partial  int
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
	writes   atomic.Int32
	closes   atomic.Int32
}

func (c *doc104RefusalSendConn) Write(p []byte) (int, error) {
	c.writes.Add(1)
	if c.entered != nil {
		c.once.Do(func() { close(c.entered); <-c.release })
	}
	if c.writeErr != nil {
		return c.partial, c.writeErr
	}
	return c.Conn.Write(p)
}
func (c *doc104RefusalSendConn) Close() error {
	c.closes.Add(1)
	_ = c.Conn.Close()
	return c.closeErr
}

func TestDoc104RefusalSendFailureClosesOnlyFailedPeer(t *testing.T) {
	for _, partial := range []int{0, 7} {
		t.Run(map[int]string{0: "no_prefix", 7: "partial_frame"}[partial], func(t *testing.T) {
			f := newDoc104PartyFixture(t, 9, true)
			doc104AuditStartLoop(t, f)
			peer := &doc104RefusalSendConn{Conn: f.follower.conn, writeErr: errors.New("refusal write failed"), closeErr: errors.New("close failed"), partial: partial}
			leader := &doc104RefusalSendConn{Conn: f.leader.conn}
			f.follower.conn = peer
			f.leader.conn = leader
			token := f.service.bindPartyDungeonEntryRoutedPending(f.follower)
			if doc104AuditAnswer(f, context.Background(), token) {
				t.Fatal("failed send reported delivered")
			}
			if peer.writes.Load() != 1 || peer.closes.Load() != 1 || leader.closes.Load() != 0 {
				t.Fatalf("peer writes=%d closes=%d leader closes=%d", peer.writes.Load(), peer.closes.Load(), leader.closes.Load())
			}
			if doc104PendingCharacterForTest(f.follower) != 0 {
				t.Fatal("failed send retained consumed token")
			}
			if doc104AuditAnswer(f, context.Background(), token) || peer.writes.Load() != 1 || peer.closes.Load() != 1 {
				t.Fatal("duplicate token repeated send or close")
			}
			if sessionHasActiveDungeonRuntime(f.leader) || sessionHasActiveDungeonRuntime(f.follower) {
				t.Fatal("refusal committed dungeon state")
			}
		})
	}
}

func TestDoc104RefusalSendFailureKeepsLeaderReply(t *testing.T) {
	f := newDoc104PartyFixture(t, 9, true)
	doc104AuditStartLoop(t, f)
	peer := &doc104RefusalSendConn{Conn: f.follower.conn, writeErr: errors.New("refusal write failed")}
	leader := &doc104RefusalSendConn{Conn: f.leader.conn}
	f.follower.conn = peer
	f.leader.conn = leader
	token := f.service.bindPartyDungeonEntryRoutedPending(f.follower)
	if err := f.service.handleDungeonSelectUpperPlanned(f.leader, doc104SelectBody(), nil, true, token); err != nil {
		t.Fatal(err)
	}
	if peer.writes.Load() != 1 || peer.closes.Load() != 1 || leader.closes.Load() != 0 {
		t.Fatal("failed peer not closed independently of healthy leader")
	}
	frames := doc104Op16Steps(doc104StepsFor(t, f, 99))
	if len(frames) != 1 {
		t.Fatalf("leader frames=%d want1", len(frames))
	}
	doc104AssertRefusalFrames(t, 99, frames, currentSelectDungeonEntryLevelTooLowReason)
	doc104AssertNoEntrySequence(t, f)
}

func TestDoc104HealthyRefusalDoesNotCloseConnections(t *testing.T) {
	f := newDoc104PartyFixture(t, 9, true)
	doc104AuditStartLoop(t, f)
	peer := &doc104RefusalSendConn{Conn: f.follower.conn}
	leader := &doc104RefusalSendConn{Conn: f.leader.conn}
	f.follower.conn = peer
	f.leader.conn = leader
	token := f.service.bindPartyDungeonEntryRoutedPending(f.follower)
	if !doc104AuditAnswer(f, context.Background(), token) {
		t.Fatal("healthy refusal not delivered")
	}
	if peer.writes.Load() != 1 || peer.closes.Load() != 0 || leader.closes.Load() != 0 {
		t.Fatal("healthy refusal closed a connection")
	}
	if doc104PendingCharacterForTest(f.follower) != 0 {
		t.Fatal("healthy refusal left pending")
	}
}

func TestDoc104StaleRefusalDoesNotCloseOrConsumeNewRequest(t *testing.T) {
	for _, change := range []string{"sequence", "character"} {
		t.Run(change, func(t *testing.T) {
			f := newDoc104PartyFixture(t, 9, true)
			doc104AuditStartLoop(t, f)
			stale := f.service.bindPartyDungeonEntryRoutedPending(f.follower)
			want := uint16(100)
			if change == "character" {
				f.service.bindGameSessionCharacter(f.follower, 101)
				want = 101
			}
			fresh := f.service.bindPartyDungeonEntryRoutedPending(f.follower)
			if fresh == nil {
				t.Fatal("missing fresh token")
			}
			// Rebinding performs its own party cleanup writes. Observe only the
			// stale answer after that independent lifecycle work has completed.
			peer := &doc104RefusalSendConn{Conn: f.follower.conn, writeErr: errors.New("must not write")}
			f.follower.conn = peer
			if doc104AuditAnswer(f, context.Background(), stale) {
				t.Fatal("stale token delivered")
			}
			if peer.writes.Load() != 0 || peer.closes.Load() != 0 || doc104PendingCharacterForTest(f.follower) != want {
				t.Fatal("stale token affected current connection/request")
			}
		})
	}
}

func TestDoc104QueuedRefusalCancellationDoesNotClosePeer(t *testing.T) {
	f := newDoc104PartyFixture(t, 9, true)
	doc104AuditStartLoop(t, f)
	peer := &doc104RefusalSendConn{Conn: f.follower.conn, writeErr: errors.New("must not write")}
	f.follower.conn = peer
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	go func() {
		_ = f.service.callGameSession(context.Background(), f.follower, "hold-before-refusal", func() error { close(entered); <-release; return nil })
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("hold did not start")
	}
	token := f.service.bindPartyDungeonEntryRoutedPending(f.follower)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan bool, 1)
	go func() { result <- doc104AuditAnswer(f, ctx, token) }()
	waitDoc104(t, "queued refusal", func() bool { return f.follower.events.loop.QueueLengths().Medium > 0 })
	cancel()
	select {
	case delivered := <-result:
		if delivered {
			t.Fatal("canceled refusal delivered")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not return")
	}
	once.Do(func() { close(release) })
	if err := f.service.callGameSession(context.Background(), f.follower, "cancel-fence", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if peer.writes.Load() != 0 || peer.closes.Load() != 0 || doc104PendingCharacterForTest(f.follower) != 100 {
		t.Fatal("queue cancellation closed peer or consumed token")
	}
}

func TestDoc104StartedRefusalClosesOnlyAfterActualSendFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "late_success", true: "late_failure"}[failed], func(t *testing.T) {
			f := newDoc104PartyFixture(t, 9, true)
			doc104AuditStartLoop(t, f)
			peer := &doc104RefusalSendConn{Conn: f.follower.conn, entered: make(chan struct{}), release: make(chan struct{})}
			if failed {
				peer.writeErr = errors.New("late refusal write failure")
			}
			f.follower.conn = peer
			var release sync.Once
			defer release.Do(func() { close(peer.release) })
			token := f.service.bindPartyDungeonEntryRoutedPending(f.follower)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			result := make(chan bool, 1)
			go func() { result <- doc104AuditAnswer(f, ctx, token) }()
			select {
			case <-peer.entered:
			case <-time.After(time.Second):
				t.Fatal("write did not start")
			}
			select {
			case delivered := <-result:
				if delivered {
					t.Fatal("deadline reported held write delivered")
				}
			case <-time.After(time.Second):
				t.Fatal("deadline did not return")
			}
			if peer.closes.Load() != 0 {
				t.Fatal("caller deadline closed an active write")
			}
			release.Do(func() { close(peer.release) })
			if err := f.service.callGameSession(context.Background(), f.follower, "late-write-fence", func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			want := int32(0)
			if failed {
				want = 1
			}
			if peer.writes.Load() != 1 || peer.closes.Load() != want || doc104PendingCharacterForTest(f.follower) != 0 {
				t.Fatalf("writes=%d closes=%d want=%d", peer.writes.Load(), peer.closes.Load(), want)
			}
		})
	}
}
