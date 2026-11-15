package dnfbridge

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// The receiving client resumes after the former 3-second cutoff. Read a
// prefix first, then recover the rest of the *same* frame, followed by another
// frame on the original stream. No reconnect, re-encode or sequence replay.
func TestGameTransportCongestedPartialFrameRecoversOnSameConnection(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	p := &gameSession{conn: left}
	s := &Service{}
	first := bytes.Repeat([]byte{0x57}, 37000)
	second := []byte{1, 2, 3, 4}
	done := make(chan error, 1)
	go func() {
		p.wireMu.Lock()
		defer p.wireMu.Unlock()
		for _, payload := range [][]byte{first, second} {
			if _, err := s.writeGameSessionObserved(p, payload, perfWireDirect); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	got := make([]byte, len(first)+len(second))
	if _, err := io.ReadFull(right, got[:2048]); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(createWriteTimeout + 250*time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-done:
		t.Fatalf("stream terminated before client recovered: %v", err)
	case <-timer.C:
	}
	_ = right.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(right, got[2048:]); err != nil {
		t.Fatal("recoverable congestion closed stream", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, append(append([]byte(nil), first...), second...)) {
		t.Fatal("frame suffix or following frame corrupted")
	}
	if p.gameTransportContext().Err() != nil {
		t.Fatal("recovered connection retired")
	}
}

func TestGameTransportCongestedWireWaitDoesNotSpendNextWriteBudget(t *testing.T) {
	p := &gameSession{conn: &bufferConn{}}
	s := &Service{}
	p.wireMu.Lock()
	done := make(chan error, 1)
	go func() { done <- s.sendGameUpperRawClass(p, 2, []byte{0}, 0) }()
	timer := time.NewTimer(createWriteTimeout + 250*time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-done:
		p.wireMu.Unlock()
		t.Fatalf("ordinary congestion rejected frame: %v", err)
	case <-timer.C:
	}
	p.wireMu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("wire did not resume")
	}
	if p.gameTransportContext().Err() != nil {
		t.Fatal("recoverable wire wait closed transport")
	}
}

func TestGameTransportBudgetSeparatesStorageDeadline(t *testing.T) {
	if createWriteTimeout != 3*time.Second {
		t.Fatal("storage/operation deadline changed")
	}
	p := &gameSession{conn: &bufferConn{}}
	ctx, cancel := gameTransportBudget(p, nil)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) < 29*time.Second || time.Until(deadline) > 30*time.Second {
		t.Fatal("game recovery budget missing")
	}
	parent, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stop()
	child, finish := gameTransportBudget(p, parent)
	defer finish()
	want, _ := parent.Deadline()
	got, _ := child.Deadline()
	if !got.Equal(want) {
		t.Fatal("explicit caller deadline extended")
	}
	p.stopGameTransport()
	select {
	case <-child.Done():
	case <-time.After(time.Second):
		t.Fatal("retired connection did not cancel pending transport")
	}
}
