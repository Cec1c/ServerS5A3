package dnfbridge

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// Exercise the same two-lock order as a projection publisher racing an aligned
// inventory callback. Channels establish both held locks before the clock read;
// a sleep or a stress loop alone would not reliably reproduce the inversion.
func TestGameplayClockPartyLockOrderAllowsBootstrap(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		name := "lazy"
		if initialized {
			name = "injected"
		}
		t.Run(name, func(t *testing.T) {
			s := &Service{}
			want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			if initialized {
				s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return want }}
			}
			s.packetLogDisabled.Store(true)
			session := &gameSession{}
			partyHeld := make(chan struct{})
			bindingHeld := make(chan struct{})
			clockDone := make(chan time.Time, 1)
			projectionDone := make(chan struct{})
			go func() {
				session.party.mu.Lock()
				close(partyHeld)
				<-bindingHeld
				now := s.gameplayNow()
				session.party.mu.Unlock()
				clockDone <- now
			}()
			go func() {
				<-partyHeld
				// storeCurrentPartyProjection holds the binding lock while waiting
				// for the target session's projection lock.
				s.mu.Lock()
				close(bindingHeld)
				session.party.mu.Lock()
				session.party.mu.Unlock()
				s.mu.Unlock()
				close(projectionDone)
			}()
			<-bindingHeld

			server, client := net.Pipe()
			defer server.Close()
			defer client.Close()
			handled := make(chan struct{})
			go func() {
				defer close(handled)
				s.trackConn(server)
				defer s.untrackConn(server)
				s.handleChannelConn(context.Background(), server)
			}()
			if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			request := make([]byte, 43)
			request[1], request[10] = 0x0b, 1
			binary.LittleEndian.PutUint32(request[2:6], uint32(len(request)))
			if _, err := client.Write(request); err != nil {
				t.Fatalf("bootstrap stalled behind party/clock lock inversion: %v", err)
			}
			ack := make([]byte, 47)
			if _, err := io.ReadFull(client, ack); err != nil {
				t.Fatalf("bootstrap acknowledgement: %v", err)
			}
			if ack[1] != 0x0c || ack[10] != 1 || binary.LittleEndian.Uint32(ack[2:6]) != 47 || binary.LittleEndian.Uint32(ack[11:15]) != 0 {
				t.Fatalf("invalid bootstrap acknowledgement: %x", ack[:15])
			}
			select {
			case now := <-clockDone:
				if now.IsZero() || (initialized && !now.Equal(want)) {
					t.Fatalf("gameplay clock changed: %s", now)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("party owner did not finish its clock read")
			}
			select {
			case <-projectionDone:
			case <-time.After(3 * time.Second):
				t.Fatal("projection owner did not release binding lock")
			}
			_ = client.Close()
			select {
			case <-handled:
			case <-time.After(3 * time.Second):
				t.Fatal("bootstrap connection cleanup did not finish")
			}
		})
	}
}

func TestGameplayClockConcurrentInitialization(t *testing.T) {
	s := &Service{}
	start := make(chan struct{})
	queues := make(chan gameplayTimeQueue, 32)
	for i := 0; i < cap(queues); i++ {
		go func() {
			<-start
			queues <- s.ensureGameplayTimeQueue()
		}()
	}
	close(start)
	var first gameplayTimeQueue
	for i := 0; i < cap(queues); i++ {
		select {
		case queue := <-queues:
			if queue == nil {
				t.Fatal("missing gameplay queue")
			}
			if first == nil {
				first = queue
			}
			if queue != first {
				t.Fatal("concurrent initialization replaced gameplay queue")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("gameplay queue initialization stalled")
		}
	}
	if first.Now().IsZero() {
		t.Fatal("initialized gameplay clock is zero")
	}
}
