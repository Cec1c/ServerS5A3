package dnfbridge

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

// Audit-only connection: hold an already-started write beyond the caller deadline.
type doc104AuditHoldConn struct {
	net.Conn
	entered    chan struct{}
	release    chan struct{}
	once       sync.Once
	skipWrites int
}

func (c *doc104AuditHoldConn) Write(p []byte) (int, error) {
	if c.skipWrites > 0 {
		c.skipWrites--
		return c.Conn.Write(p)
	}
	c.once.Do(func() { close(c.entered); <-c.release })
	return c.Conn.Write(p)
}
func doc104AuditStartLoop(t *testing.T, f *doc104PartyFixture) {
	t.Helper()
	if err := f.service.startGameSessionEvents(context.Background(), f.follower); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := f.service.stopGameSessionEvents(ctx, f.follower); err != nil {
			t.Error(err)
		}
	})
}
func doc104AuditAnswer(f *doc104PartyFixture, ctx context.Context, token *partyDungeonEntryRoutedRequest) bool {
	request, _ := dungeoncmd.DecodeSelectDungeonRequest(doc104SelectBody())
	return f.service.answerRoutedPartyEntryOwnerOnRefusal(ctx, f.leader,
		runtimePartyStateSnapshot(f.leader), token, request, "final_queue_audit", "entry_unavailable", currentSelectDungeonUnavailableReason, nil)
}
func TestDoc104FinalQueueAudit(t *testing.T) {
	t.Run("started_write_outlives_deadline_and_duplicate_is_silent", func(t *testing.T) {
		f := newDoc104PartyFixture(t, 9, true)
		held := &doc104AuditHoldConn{Conn: f.follower.conn, entered: make(chan struct{}), release: make(chan struct{})}
		f.follower.conn = held
		doc104AuditStartLoop(t, f)
		var release sync.Once
		defer release.Do(func() { close(held.release) })
		token := f.service.bindPartyDungeonEntryRoutedPending(f.follower)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		result := make(chan bool, 1)
		go func() { result <- doc104AuditAnswer(f, ctx, token) }()
		select {
		case <-held.entered:
		case <-time.After(time.Second):
			t.Fatal("write never started")
		}
		select {
		case delivered := <-result:
			if delivered {
				t.Fatal("deadline unexpectedly reported delivered")
			}
		case <-time.After(time.Second):
			t.Fatal("deadline did not return")
		}
		if len(doc104StepsFor(t, f, 100)) != 0 {
			t.Fatal("write passed hold")
		}
		release.Do(func() { close(held.release) })
		if err := f.service.callGameSession(context.Background(), f.follower, "audit-fence", func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		if doc104AuditAnswer(f, context.Background(), token) {
			t.Fatal("duplicate was delivered")
		}
		frames := doc104Op16Steps(doc104StepsFor(t, f, 100))
		if len(frames) != 1 {
			t.Fatalf("late/duplicate frames=%d want 1", len(frames))
		}
		t.Log("caller returned false at deadline; started callback later emitted one refusal; duplicate emitted zero")
	})
	t.Run("queued_cancel_retains_token_for_fresh_fallback", func(t *testing.T) {
		f := newDoc104PartyFixture(t, 9, true)
		doc104AuditStartLoop(t, f)
		entered, releaseHold := make(chan struct{}), make(chan struct{})
		var release sync.Once
		defer release.Do(func() { close(releaseHold) })
		go func() {
			_ = f.service.callGameSession(context.Background(), f.follower, "audit-hold", func() error { close(entered); <-releaseHold; return nil })
		}()
		<-entered
		token := f.service.bindPartyDungeonEntryRoutedPending(f.follower)
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan bool, 1)
		go func() { result <- doc104AuditAnswer(f, ctx, token) }()
		waitDoc104(t, "queued refusal", func() bool { return f.follower.events.loop.QueueLengths().Medium > 0 })
		cancel()
		if <-result {
			t.Fatal("cancelled call reported delivered")
		}
		release.Do(func() { close(releaseHold) })
		_ = f.service.callGameSession(context.Background(), f.follower, "audit-fence", func() error { return nil })
		if len(doc104StepsFor(t, f, 100)) != 0 || doc104PendingCharacterForTest(f.follower) != 100 {
			t.Fatal("cancelled queued task ran or consumed token")
		}
		if !doc104AuditAnswer(f, context.Background(), token) {
			t.Fatal("fresh fallback did not answer")
		}
		if doc104AuditAnswer(f, context.Background(), token) {
			t.Fatal("duplicate answered")
		}
	})
	t.Run("successful_handler_commit_has_no_deferred_refusal", func(t *testing.T) {
		f := newDoc104PartyFixture(t, 20, true)
		doc104AuditStartLoop(t, f)
		token := f.service.bindPartyDungeonEntryRoutedPending(f.follower)
		if err := f.service.handleDungeonSelectUpperPlanned(f.leader, doc104SelectBody(), nil, true, token); err != nil {
			t.Fatal(err)
		}
		if !sessionHasActiveDungeonRuntime(f.leader) || !sessionHasActiveDungeonRuntime(f.follower) {
			t.Fatal("expected both runtimes committed")
		}
		if doc104PendingCharacterForTest(f.follower) != 0 {
			t.Fatal("success left token pending")
		}
		for _, frame := range doc104Op16Steps(doc104StepsFor(t, f, 100)) {
			if len(frame.Body) > 0 && frame.Body[0] == 0 {
				t.Fatalf("success produced refusal: %x", frame.Body)
			}
		}
		if doc104AuditAnswer(f, context.Background(), token) {
			t.Fatal("post-success duplicate answered")
		}
	})
}

// Records a frozen-code defect: a routed task that expires in the leader queue
// never enters the handler, so the handler's deferred answer cannot run.
func TestDoc104FinalQueueAuditLeaderQueueExpiryAnswersPending(t *testing.T) {
	f := newDoc104PartyFixture(t, 9, true)
	if err := f.service.startGameSessionEvents(context.Background(), f.leader); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = f.service.stopGameSessionEvents(ctx, f.leader)
	}()
	entered, releaseHold := make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(releaseHold) })
	go func() {
		_ = f.service.callGameSession(context.Background(), f.leader, "audit-leader-hold", func() error { close(entered); <-releaseHold; return nil })
	}()
	<-entered
	f.pressEnter(t, f.follower)
	waitDoc104(t, "leader route enqueued", func() bool { return f.leader.events.loop.QueueLengths().Medium > 0 })
	// Production dispatch creates its own createWriteTimeout context.
	time.Sleep(createWriteTimeout + 100*time.Millisecond)
	release.Do(func() { close(releaseHold) })
	if err := f.service.callGameSession(context.Background(), f.leader, "audit-leader-fence", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	waitDoc104(t, "dispatch fallback refusal", func() bool { return len(doc104Op16Steps(doc104StepsFor(t, f, 100))) == 2 })
	doc104AuditAssertACKAndRefusal(t, f)
}

func doc104AuditAssertACKAndRefusal(t *testing.T, f *doc104PartyFixture) {
	t.Helper()
	frames := doc104Op16Steps(doc104StepsFor(t, f, 100))
	if len(frames) != 2 || len(frames[0].Body) == 0 || frames[0].Body[0] != 1 {
		t.Fatalf("want ACK then one refusal, got %+v", frames)
	}
	doc104AssertRefusalFrames(t, 100, frames[1:], currentSelectDungeonUnavailableReason)
	if doc104PendingCharacterForTest(f.follower) != 0 {
		t.Fatal("dispatch failure left pending token")
	}
	doc104AssertNoEntrySequence(t, f)
}

func TestDoc104FinalQueueAuditStartedHandlerOutlivesDeadline(t *testing.T) {
	f := newDoc104PartyFixture(t, 20, true)
	if err := f.service.startGameSessionEvents(context.Background(), f.leader); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = f.service.stopGameSessionEvents(ctx, f.leader)
	}()
	entered, releaseHold := make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(releaseHold) })
	held := &doc104AuditHoldConn{Conn: f.follower.conn, entered: entered, release: releaseHold, skipWrites: 1}
	f.follower.conn = held
	f.pressEnter(t, f.follower)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler did not reach follower selector preflight write")
	}
	// The handler owns the claim, but has not committed or consumed the token.
	if doc104PendingCharacterForTest(f.follower) != 100 {
		t.Fatal("token not pending while handler paused")
	}
	if sessionHasActiveDungeonRuntime(f.leader) {
		t.Fatal("pause must precede commit")
	}
	time.Sleep(createWriteTimeout + 100*time.Millisecond)
	frames := doc104Op16Steps(doc104StepsFor(t, f, 100))
	if len(frames) != 1 || frames[0].Body[0] != 1 {
		t.Fatalf("started handler was prematurely refused: %+v", frames)
	}
	if doc104PendingCharacterForTest(f.follower) != 100 {
		t.Fatal("timeout consumed running handler token")
	}
	release.Do(func() { close(releaseHold) })
	if err := f.service.callGameSession(context.Background(), f.leader, "audit-started-fence", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !sessionHasActiveDungeonRuntime(f.leader) || !sessionHasActiveDungeonRuntime(f.follower) {
		t.Fatal("started handler did not subsequently commit successfully")
	}
	if doc104PendingCharacterForTest(f.follower) != 0 {
		t.Fatal("successful handler retained pending")
	}
	for _, frame := range doc104Op16Steps(doc104StepsFor(t, f, 100)) {
		if len(frame.Body) > 0 && frame.Body[0] == 0 {
			t.Fatalf("started successful handler received refusal: %x", frame.Body)
		}
	}
}

func TestDoc104FinalQueueAuditStoppedLeaderAnswersPending(t *testing.T) {
	for _, queued := range []bool{false, true} {
		name := "already_stopped"
		if queued {
			name = "stopped_while_queued"
		}
		t.Run(name, func(t *testing.T) {
			f := newDoc104PartyFixture(t, 9, true)
			doc104AuditStartLoop(t, f)
			if err := f.service.startGameSessionEvents(context.Background(), f.leader); err != nil {
				t.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = f.service.stopGameSessionEvents(ctx, f.leader)
			}()
			entered, releaseHold := make(chan struct{}), make(chan struct{})
			var release sync.Once
			defer release.Do(func() { close(releaseHold) })
			if queued {
				go func() {
					_ = f.service.callGameSession(context.Background(), f.leader, "audit-stop-hold", func() error { close(entered); <-releaseHold; return nil })
				}()
				<-entered
				f.pressEnter(t, f.follower)
				waitDoc104(t, "route queued before stop", func() bool { return f.leader.events.loop.QueueLengths().Medium > 0 })
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			err := f.service.stopGameSessionEvents(ctx, f.leader)
			cancel()
			if !queued && err != nil {
				t.Fatal(err)
			}
			if !queued {
				f.pressEnter(t, f.follower)
			}
			waitDoc104(t, "stopped leader fallback", func() bool { return len(doc104Op16Steps(doc104StepsFor(t, f, 100))) == 2 })
			release.Do(func() { close(releaseHold) })
			ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			if err := f.service.stopGameSessionEvents(ctx, f.leader); err != nil {
				t.Error(err)
			}
			cancel()
			doc104AuditAssertACKAndRefusal(t, f)
		})
	}
}
