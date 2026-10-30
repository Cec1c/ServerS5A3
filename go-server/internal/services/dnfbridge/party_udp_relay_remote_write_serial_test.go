package dnfbridge

import (
	"bytes"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"
)

// partyRelayOneByteWriteConn forces the relay frame writer through its
// short-write loop while keeping the recorded stream race-free.
type partyRelayOneByteWriteConn struct {
	bufferConn
	mu     sync.Mutex
	cancel <-chan struct{}
}

var _ net.Conn = (*partyRelayOneByteWriteConn)(nil)

func (c *partyRelayOneByteWriteConn) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	select {
	case <-c.cancel:
		return 0, net.ErrClosed
	default:
	}
	runtime.Gosched()
	c.mu.Lock()
	written, err := c.bufferConn.write.Write(data[:1])
	c.mu.Unlock()
	runtime.Gosched()
	return written, err
}

func (c *partyRelayOneByteWriteConn) writtenBytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.bufferConn.write.Bytes()...)
}

func TestCurrentPartyUDPRelayRemoteSendFrameSerializesWholeFrames(t *testing.T) {
	cancelWrites := make(chan struct{})
	conn := &partyRelayOneByteWriteConn{cancel: cancelWrites}
	remote := &currentPartyUDPRelayRemote{
		conn:      conn,
		connected: true,
	}

	sends := []struct {
		opcode  byte
		payload []byte
	}{
		{opcode: 0x31, payload: []byte{0x11, 0x12, 0x13, 0x14}},
		{opcode: 0x72, payload: []byte{0x21, 0x22, 0x23, 0x24, 0x25, 0x26}},
	}

	start := make(chan struct{})
	results := make(chan error, len(sends))
	var workers sync.WaitGroup
	workers.Add(len(sends))
	for _, send := range sends {
		send := send
		go func() {
			defer workers.Done()
			<-start
			results <- remote.sendFrame(send.opcode, send.payload)
		}()
	}
	close(start)

	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	var sendErr error
	for index := 0; index < len(sends); index++ {
		select {
		case err := <-results:
			if err != nil && sendErr == nil {
				sendErr = err
			}
		case <-timer.C:
			close(cancelWrites)
			workers.Wait()
			t.Fatal("timed out waiting for concurrent sendFrame calls to finish")
		}
	}
	workers.Wait()
	if sendErr != nil {
		t.Fatalf("concurrent sendFrame failed: %v", sendErr)
	}

	wire := conn.writtenBytes()
	reader := bytes.NewReader(wire)
	frames := make(map[byte][]byte, len(sends))
	for index := 0; index < len(sends); index++ {
		opcode, payload, err := readPartyRelayFrame(reader)
		if err != nil {
			t.Fatalf("read relay frame %d from wire %x: %v", index+1, wire, err)
		}
		if _, duplicate := frames[opcode]; duplicate {
			t.Fatalf("relay wire repeated opcode 0x%02x; wire=%x", opcode, wire)
		}
		frames[opcode] = payload
	}
	if remaining := reader.Len(); remaining != 0 {
		t.Fatalf("relay wire has %d trailing bytes after exactly two frames; wire=%x", remaining, wire)
	}
	if len(frames) != len(sends) {
		t.Fatalf("decoded frame count = %d, want %d; wire=%x", len(frames), len(sends), wire)
	}
	for _, send := range sends {
		payload, ok := frames[send.opcode]
		if !ok {
			t.Fatalf("relay opcode 0x%02x missing; wire=%x", send.opcode, wire)
		}
		if !bytes.Equal(payload, send.payload) {
			t.Fatalf("relay opcode 0x%02x payload = %x, want %x; wire=%x", send.opcode, payload, send.payload, wire)
		}
	}
}
