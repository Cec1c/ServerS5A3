package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

type crossSceneProfileObservedConn struct {
	net.Conn
	writing chan struct{}
	once    sync.Once
}

func (c *crossSceneProfileObservedConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.writing) })
	return c.Conn.Write(p)
}

func TestCrossSceneProfileSlowReaderReleasesAppearanceLock(t *testing.T) {
	for _, cancelDuringWrite := range []bool{false, true} {
		name := "deadline"
		if cancelDuringWrite {
			name = "cancel"
		}
		t.Run(name, func(t *testing.T) {
			f := newCurrentCrossSceneProfileBatchFixture(t)
			left, right := net.Pipe()
			t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
			conn := &crossSceneProfileObservedConn{Conn: left, writing: make(chan struct{})}
			f.target.conn = conn
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			if cancelDuringWrite {
				cancel()
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- newCurrentCrossSceneProfileBatch(f.service, ctx).send(f.target, f.actor, currentConnectionTownActorOwnerContext(f.target))
			}()
			select {
			case <-conn.writing:
			case <-time.After(3 * time.Second):
				t.Fatal("profile never reached socket")
			}
			want := context.DeadlineExceeded
			if cancelDuringWrite {
				cancel()
				want = context.Canceled
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("write error %v, want %v", err, want)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("slow peer retained actor appearance lock")
			}
			if !f.actor.crossSceneAppearanceMu.TryLock() {
				t.Fatal("appearance lock retained after failure")
			}
			f.actor.crossSceneAppearanceMu.Unlock()
			if !f.target.wireMu.TryLock() {
				t.Fatal("wire lock retained after failure")
			}
			f.target.wireMu.Unlock()
			if _, err := right.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
				t.Fatalf("failed stream remained reusable: %v", err)
			}
		})
	}
}

func TestCrossSceneProfileLockWaitHonorsCancellation(t *testing.T) {
	for _, which := range []string{"appearance", "wire"} {
		t.Run(which, func(t *testing.T) {
			f := newCurrentCrossSceneProfileBatchFixture(t)
			f.reset()
			mutex := &f.actor.crossSceneAppearanceMu
			if which == "wire" {
				mutex = &f.target.wireMu
			}
			mutex.Lock()
			defer mutex.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			seq := f.target.upperSeq
			done := make(chan error, 1)
			go func() {
				_, err := f.service.sendCurrentCrossSceneProfileFrame(ctx, f.target, f.actor, []byte{8, 0, 0}, func() bool { return true })
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("lock error %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cancelled lock waiter did not return")
			}
			if f.target.upperSeq != seq || f.targetConn.write.Len() != 0 {
				t.Fatal("cancelled lock waiter changed wire")
			}
		})
	}
}

func TestCrossSceneProfileRechecksMembershipAfterWireWait(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	batch := newCurrentCrossSceneProfileBatch(f.service, ctx)
	owner := currentConnectionTownActorOwnerContext(f.target)
	if err := batch.send(f.target, f.actor, owner); err != nil {
		t.Fatal(err)
	}
	f.reset()
	f.viewer2.wireMu.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			f.viewer2.wireMu.Unlock()
		}
	}()
	done := make(chan error, 1)
	go func() { done <- batch.send(f.viewer2, f.actor, owner) }()
	// Observe the actual sender holding the actor lock while it waits for the
	// blocked recipient wire. Then remove that recipient through the owner.
	until := time.Now().Add(time.Second)
	for f.actor.crossSceneAppearanceMu.TryLock() {
		f.actor.crossSceneAppearanceMu.Unlock()
		if time.Now().After(until) {
			t.Fatal("sender did not reach wire wait")
		}
		time.Sleep(time.Millisecond)
	}
	if result := f.service.runtimePartyManagerForService().LeaveCurrent(1001); !result.OK {
		t.Fatalf("leave failed: %s", result.Reason)
	}
	f.viewer2.wireMu.Unlock()
	unlocked = true
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("wire waiter did not finish")
	}
	if f.viewer2Conn.write.Len() != 0 {
		t.Fatal("departed member received queued profile")
	}
}

func TestCrossSceneProfileWireMatchesExistingCodecAndRestoresDeadline(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	body := []byte{8, 0, 0}
	f.reset()
	seq := f.target.upperSeq
	if err := f.service.sendGameUpperRawClass(f.target, 2, body, 0); err != nil {
		t.Fatal(err)
	}
	want := append([]byte(nil), f.targetConn.write.Bytes()...)
	f.target.upperSeq = seq
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	f.target.conn = left
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	read := make(chan []byte, 1)
	go func() {
		b := make([]byte, len(want))
		_, err := io.ReadFull(right, b)
		if err != nil {
			read <- nil
			return
		}
		read <- b
	}()
	delivered, err := f.service.sendCurrentCrossSceneProfileFrame(ctx, f.target, f.actor, body, func() bool { return true })
	if err != nil || !delivered {
		t.Fatalf("profile delivered=%v err=%v", delivered, err)
	}
	if got := <-read; !bytes.Equal(got, want) {
		t.Fatal("profile framing/sequence differs from original codec")
	}
	// Cancel the completed profile and wait past its deadline. The following
	// ordinary write must still work on this same connection.
	cancel()
	time.Sleep(110 * time.Millisecond)
	go func() {
		b := make([]byte, len(want))
		_, err := io.ReadFull(right, b)
		if err != nil {
			read <- nil
			return
		}
		read <- b
	}()
	done := make(chan error, 1)
	go func() { done <- f.service.sendGameUpperRawClass(f.target, 2, body, 0) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("profile deadline leaked to ordinary frame: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ordinary write did not finish")
	}
	if got := <-read; len(got) != len(want) {
		t.Fatal("ordinary frame missing")
	}
}
