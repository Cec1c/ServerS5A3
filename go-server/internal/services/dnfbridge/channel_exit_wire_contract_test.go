package dnfbridge

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestChannelExitSuppressesOldWriterAndPreservesNewConnection(t *testing.T) {
	s, p, _ := guildMileageFixture(t)
	c := p.conn.(*bufferConn)
	c.write.Reset()
	if err := s.handleCurrentChannelExit(p, []byte{1}, "wire-contract"); err != nil {
		t.Fatal(err)
	}
	ack := append([]byte(nil), c.write.Bytes()...)
	if err := s.sendGameUpperSecondaryRaw(p, 591, []byte{0}); err != nil {
		t.Fatal(err)
	}
	// Some batch and protected transport paths already hold the writer lock.
	p.wireMu.Lock()
	err := s.writeGameRawPacketLocked(p, []byte{1, 2, 3})
	if err == nil {
		err = s.writeGameUpperPacketLocked(p, ack)
	}
	n, directErr := s.writeGameSessionObserved(p, ack, perfWireDirect)
	p.wireMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || !errors.Is(directErr, io.ErrClosedPipe) {
		t.Fatalf("retired direct writer falsely reported delivery: n=%d err=%v", n, directErr)
	}
	if !bytes.Equal(c.write.Bytes(), ack) {
		t.Fatal("old wire accepted a packet after EXIT")
	}
	next := &gameSession{conn: &bufferConn{}, connID: "new-ordinary-connection"}
	if err := s.sendGameUpperSecondaryRaw(next, 1228, []byte{0}); err != nil {
		t.Fatal(err)
	}
	if next.conn.(*bufferConn).write.Len() == 0 {
		t.Fatal("new socket inherited old scene's closed writer")
	}
}

func TestChannelExitFailedAckDoesNotAcknowledgeTeardown(t *testing.T) {
	s, p, _ := guildMileageFixture(t)
	p.conn = &firstChannelFailedWriteConn{}
	if err := s.handleCurrentChannelExit(p, []byte{1}, "failed-wire-contract"); err == nil {
		t.Fatal("expected failed ACK")
	}
	if p.channelExitAcknowledged || p.channelExitWireClosed {
		t.Fatal("failed ACK acknowledged scene teardown")
	}
	p.raidRefresh.mu.Lock()
	closed := p.raidRefresh.closed
	p.raidRefresh.mu.Unlock()
	if closed {
		t.Fatal("failed ACK canceled pending raid state as though teardown succeeded")
	}
}
