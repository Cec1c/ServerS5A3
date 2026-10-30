package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func TestCurrentCrossSceneProfileAutomaticBatchStaysMode8WhileInspectionPairsMode3(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	owner := currentConnectionTownActorOwnerContext(f.target)

	// A fresh automatic batch may refresh the same pair repeatedly, but every
	// automatic output remains exactly one mode8 prime.
	for round := 0; round < 2; round++ {
		f.reset()
		if err := newCurrentCrossSceneProfileBatch(f.service, context.Background()).send(f.target, f.actor, owner); err != nil {
			t.Fatalf("automatic refresh round %d: %v", round, err)
		}
		frames := splitUpperFrames(t, f.targetConn.write.Bytes())
		if len(frames) != 1 || frames[0].msgID != uint16(dnfenum.CmdPacketSetUDPIPPort) ||
			len(frames[0].body) < 3 || frames[0].body[0] != currentCrossSceneMode8Subtype ||
			binary.LittleEndian.Uint16(frames[0].body[1:3]) != currentCrossSceneMode8Count {
			t.Fatalf("automatic refresh round %d emitted non-prime frames: %v", round, townPartyActorHPFrameIDs(frames))
		}
	}

	f.reset()
	if err := f.service.sendCurrentCrossScenePeerProfile(context.Background(), f.target, f.actor, 29, 17); err != nil {
		t.Fatalf("explicit inspection: %v", err)
	}
	assertCurrentCrossSceneProfileFramePair(
		t,
		f.targetConn.write.Bytes(),
		f.service.gameUpperHeaderSize(),
		17,
		f.service.currentSceneActorRoute(),
		owner,
	)
}

// Transport tests need a complete native profile now that the delivery path
// refreshes its HP byte. Use the real builder/sender once to obtain valid
// bodies, then clear only the captured wire before testing transport behavior.
func currentCrossSceneProfileTransportBodies(t *testing.T, f *currentCrossSceneProfileBatchFixture) [][]byte {
	t.Helper()
	f.reset()
	if err := f.service.sendCurrentCrossScenePeerProfile(context.Background(), f.target, f.actor,
		f.target.selectedCharacterID, f.actor.selectedCharacterID); err != nil {
		t.Fatalf("prepare valid transport profile: %v", err)
	}
	frames := splitUpperFrames(t, f.targetConn.write.Bytes())
	if len(frames) != 2 || len(frames[0].body) == 0 || frames[0].body[0] != currentCrossSceneMode8Subtype ||
		len(frames[1].body) == 0 || frames[1].body[0] != currentCrossSceneMode3Subtype {
		t.Fatal("transport fixture needs the real mode8/mode3 pair")
	}
	bodies := [][]byte{append([]byte(nil), frames[0].body...), append([]byte(nil), frames[1].body...)}
	f.reset()
	return bodies
}

func TestCurrentCrossSceneProfileFramesRejectEmptyList(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	f.reset()
	delivered, err := f.service.sendCurrentCrossSceneProfileFrames(
		context.Background(), f.target, f.actor, nil, func() bool { return true },
	)
	if err == nil || delivered || f.targetConn.write.Len() != 0 {
		t.Fatalf("empty frame list delivered=%t err=%v bytes=%d", delivered, err, f.targetConn.write.Len())
	}
}

func TestCurrentCrossSceneProfileFramesStopBeforeNextInvalidatedFrame(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	bodies := currentCrossSceneProfileTransportBodies(t, f)
	f.reset()
	checks := 0
	delivered, err := f.service.sendCurrentCrossSceneProfileFrames(
		context.Background(),
		f.target,
		f.actor,
		bodies,
		func() bool {
			checks++
			// Simulate a binding/generation invalidation after the priming frame.
			return checks == 1
		},
	)
	if err != nil || delivered || checks != 2 {
		t.Fatalf("generation invalidation delivered=%t err=%v checks=%d", delivered, err, checks)
	}
	frames := splitUpperFrames(t, f.targetConn.write.Bytes())
	if len(frames) != 1 || len(frames[0].body) == 0 || frames[0].body[0] != currentCrossSceneMode8Subtype {
		t.Fatalf("invalidated pair did not leave only the already-written prime: %v", townPartyActorHPFrameIDs(frames))
	}
	assertCrossSceneProfileLocksReleased(t, f.target, f.actor)
}

type crossSceneProfileFailNthConn struct {
	*bufferConn
	failAt int
	writes int
	closed bool
}

func (c *crossSceneProfileFailNthConn) Write(data []byte) (int, error) {
	c.writes++
	if c.writes == c.failAt {
		return 0, errors.New("injected cross-scene second-frame write failure")
	}
	return c.bufferConn.Write(data)
}

func (c *crossSceneProfileFailNthConn) Close() error {
	c.closed = true
	return nil
}

func TestCurrentCrossSceneProfileFramesCloseStreamAfterLaterFrameFailure(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	bodies := currentCrossSceneProfileTransportBodies(t, f)
	f.reset()
	conn := &crossSceneProfileFailNthConn{bufferConn: f.targetConn, failAt: 2}
	f.target.conn = conn

	delivered, err := f.service.sendCurrentCrossSceneProfileFrames(
		context.Background(),
		f.target,
		f.actor,
		bodies,
		func() bool { return true },
	)
	if err == nil || delivered || !conn.closed || conn.writes != 2 {
		t.Fatalf("second-frame failure delivered=%t closed=%t writes=%d err=%v", delivered, conn.closed, conn.writes, err)
	}
	frames := splitUpperFrames(t, f.targetConn.write.Bytes())
	if len(frames) != 1 || len(frames[0].body) == 0 || frames[0].body[0] != currentCrossSceneMode8Subtype {
		t.Fatalf("failed pair did not retain only its complete first frame: %v", townPartyActorHPFrameIDs(frames))
	}
	assertCrossSceneProfileLocksReleased(t, f.target, f.actor)
}

func readCurrentCrossSceneUpperFrame(conn net.Conn) ([]byte, error) {
	header := make([]byte, dnfproto.GameServerUpperHeaderSize)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	total := int(binary.LittleEndian.Uint32(header[3:7]))
	if total < dnfproto.GameServerUpperHeaderSize {
		return nil, errors.New("cross-scene upper frame has invalid total length")
	}
	packet := make([]byte, total)
	copy(packet, header)
	if _, err := io.ReadFull(conn, packet[dnfproto.GameServerUpperHeaderSize:]); err != nil {
		return nil, err
	}
	return packet, nil
}

func TestCurrentCrossSceneProfileFramesShareDeadlineAcrossPair(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	bodies := currentCrossSceneProfileTransportBodies(t, f)
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	f.target.conn = left
	if err := right.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set frame read deadline: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	type result struct {
		delivered bool
		err       error
	}
	done := make(chan result, 1)
	go func() {
		delivered, err := f.service.sendCurrentCrossSceneProfileFrames(
			ctx,
			f.target,
			f.actor,
			bodies,
			func() bool { return true },
		)
		done <- result{delivered: delivered, err: err}
	}()

	first, err := readCurrentCrossSceneUpperFrame(right)
	if err != nil {
		t.Fatalf("read priming frame: %v", err)
	}
	if len(first) <= dnfproto.GameServerUpperHeaderSize || first[dnfproto.GameServerUpperHeaderSize] != currentCrossSceneMode8Subtype {
		t.Fatalf("first frame is not the mode8 prime: %x", first)
	}
	// Deliberately stop reading. The second frame must consume the same pair
	// deadline, fail, close the stream and release both profile locks.
	select {
	case got := <-done:
		if got.delivered || !errors.Is(got.err, context.DeadlineExceeded) {
			t.Fatalf("pair deadline delivered=%t err=%v", got.delivered, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cross-scene frame pair did not honor its shared deadline")
	}
	assertCrossSceneProfileLocksReleased(t, f.target, f.actor)
}

func assertCrossSceneProfileLocksReleased(t *testing.T, viewer, actor *gameSession) {
	t.Helper()
	if !actor.crossSceneAppearanceMu.TryLock() {
		t.Fatal("cross-scene appearance lock retained after frame failure")
	}
	actor.crossSceneAppearanceMu.Unlock()
	if !viewer.wireMu.TryLock() {
		t.Fatal("cross-scene wire lock retained after frame failure")
	}
	viewer.wireMu.Unlock()
}
