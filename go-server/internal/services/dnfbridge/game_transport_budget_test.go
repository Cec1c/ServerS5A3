package dnfbridge

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

type transportTestConn struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

func (c *transportTestConn) Write(b []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Write(b)
}

func TestGameTransportUnreadPeerExpiresAndReleasesRetirement(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	defer left.Close()
	conn := &transportTestConn{Conn: left, started: make(chan struct{})}
	slow := &gameSession{conn: conn, connID: "slow"}
	s := &Service{}
	first := make(chan error, 1)
	go func() { first <- s.sendGameUpperRawClass(slow, 1297, []byte{1}, 0) }()
	waitTestSignal(t, conn.started, "slow write not entered")
	owner := &gameSession{conn: &bufferConn{}, connID: "owner", ctx: context.Background()}
	if err := s.startGameSessionEvents(owner.ctx, owner); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, owner)
	s.accountLogins.sessions = map[*gameSession]string{owner: "test-account"}
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.callGameSession(owner.ctx, owner, "game-stream-packet", func() error {
			close(entered)
			return s.sendGameUpperRawClass(slow, 2, []byte{0}, 0)
		})
	}()
	waitTestSignal(t, entered, "owner did not enter")
	s.accountLogins.mu.Lock()
	s.accountLogins.replace("test-account")
	retired := s.accountLogins.retiring["test-account"].done
	s.accountLogins.mu.Unlock()
	select {
	case <-retired:
		t.Fatal("retirement released before cleanup")
	default:
	}
	select {
	case err := <-first:
		if err == nil {
			t.Fatal("unread peer succeeded")
		}
	case <-time.After(2 * createWriteTimeout):
		t.Fatal("ordinary write remained unbounded")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("peer wire wait did not end")
	}
	s.shutdownGameSessionEvents(owner, false)
	waitTestSignal(t, retired, "retirement did not release after actual shutdown")
	if err := s.sendGameUpperRawClass(&gameSession{conn: &bufferConn{}}, 1297, []byte{0}, 0); err != nil {
		t.Fatal(err)
	}
}

func TestGameTransportLockCancellationDoesNotAllocateSequence(t *testing.T) {
	p := &gameSession{conn: &bufferConn{}}
	p.wireMu.Lock()
	done := make(chan error, 1)
	go func() { done <- (&Service{}).sendGameUpperRawClass(p, 2, []byte{0}, 0) }()
	p.stopGameTransport()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled lock waited")
	}
	if p.sequence != 0 || p.upperSeq != 0 {
		t.Fatal("sequence allocated before lock")
	}
	p.wireMu.Unlock()
}

func TestGameTransportNestedBudgetRestoresEarlierParent(t *testing.T) {
	p := &gameSession{conn: &bufferConn{}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p.wireMu.Lock()
	defer p.wireMu.Unlock()
	finish, err := beginGameWriteBudgetLocked(p, ctx)
	if err != nil {
		t.Fatal(err)
	}
	parent := p.wireWriteContext
	want, _ := parent.Deadline()
	nested, err := beginGameWriteBudgetLocked(p, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := p.wireWriteContext.Deadline()
	if got.After(want) {
		t.Fatal("nested budget extended parent")
	}
	nested()
	if p.wireWriteContext != parent {
		t.Fatal("parent scope lost")
	}
	finish()
	if p.wireWriteContext != nil {
		t.Fatal("scope leaked")
	}
}

type shortGameWriteConn struct {
	*bufferConn
	closed bool
}

func (c *shortGameWriteConn) Write(b []byte) (int, error) { return len(b) - 1, nil }
func (c *shortGameWriteConn) Close() error                { c.closed = true; return nil }
func TestGameTransportShortWriteClosesOnlyRecipient(t *testing.T) {
	c := &shortGameWriteConn{bufferConn: &bufferConn{}}
	err := (&Service{}).sendGameUpperRawClass(&gameSession{conn: c}, 2, []byte{0}, 0)
	if !errors.Is(err, io.ErrShortWrite) || !c.closed {
		t.Fatalf("err=%v closed=%v", err, c.closed)
	}
	if err := (&Service{}).sendGameUpperRawClass(&gameSession{conn: &bufferConn{}}, 2, []byte{0}, 0); err != nil {
		t.Fatal(err)
	}
}

func TestGameTransportCallerDeadlineAndCancellationDoNotLeak(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	p := &gameSession{conn: left}
	s := &Service{}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	p.wireMu.Lock()
	finish, err := beginGameWriteBudgetLocked(p, ctx)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = s.writeGameSessionObserved(p, []byte{1}, perfWireDirect)
	finish()
	p.wireMu.Unlock()
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("earlier deadline ignored: %v", err)
	}
}
