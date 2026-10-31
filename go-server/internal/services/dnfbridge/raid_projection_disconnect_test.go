package dnfbridge

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

type raidProjectionFailedConn struct {
	*bufferConn
	closed bool
}

func (c *raidProjectionFailedConn) Write([]byte) (int, error) { return 0, net.ErrClosed }
func (c *raidProjectionFailedConn) Close() error              { c.closed = true; return nil }

func TestRaidRefreshClosedRecipientDoesNotFailAnotherMember(t *testing.T) {
	s, leader, peer, leaderConn, _, _ := prepareCurrentRaidApplicationTest(t)
	if err := s.handleGameCommand(peer, 1, 820, currentRaidApplyFixture(leader.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameCommand(leader, 1, 11, currentRaidApprovalFixture(peer.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}
	bad := &raidProjectionFailedConn{bufferConn: &bufferConn{}}
	peer.conn = bad
	leaderConn.write.Reset()
	// A real committed leader command broadcasts to the stale peer. Returning
	// that peer's socket error would make handleGameStreamInput close the leader.
	if err := s.handleCurrentModifyRaid(leader, 670, currentRaidRequestFixture(0, "updated", 0, 0)); err != nil {
		t.Fatalf("recipient failure escaped into healthy command: %v", err)
	}
	if leaderConn.write.Len() == 0 || !bad.closed {
		t.Fatalf("leader bytes=%d failed recipient closed=%v", leaderConn.write.Len(), bad.closed)
	}
	state, ok := s.runtimeRaidForMember(peer.selectedCharacterID)
	if !ok || len(state.Members) != 2 {
		t.Fatal("delivery failure rewrote committed membership")
	}
	if err := s.sendGameUpperRaw(leader, 1276, nil); err != nil {
		t.Fatal("healthy session unusable", err)
	}
}

func TestRaidProfileAdmittedWriteSurvivesPreparationDeadline(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	f.reset()
	left, right := net.Pipe()
	t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
	conn := &crossSceneProfileObservedConn{Conn: left, writing: make(chan struct{})}
	f.target.conn = conn
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	batch := newCurrentRaidProfileBatch(f.service, ctx)
	// This is the batch used by the raid roster, whose preparation budget is
	// shared across recipients. It must not truncate a frame already admitted.
	done := make(chan error, 1)
	go func() { done <- batch.send(f.target, f.actor, currentConnectionTownActorOwnerContext(f.target)) }()
	select {
	case <-conn.writing:
	case <-time.After(2 * time.Second):
		t.Fatal("profile did not start writing")
	}
	<-ctx.Done()
	_ = right.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 8192)
	n, err := right.Read(buf)
	if err != nil || n == 0 {
		t.Fatalf("healthy frame cut off at preparation deadline: n=%d err=%v", n, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("profile writer stuck")
	}
	if err := batch.send(f.viewer2, f.actor, currentConnectionTownActorOwnerContext(f.viewer2)); !errors.Is(err, context.DeadlineExceeded) || f.viewer2Conn.write.Len() != 0 {
		t.Fatal("expired preparation admitted another frame", err)
	}
	// The main connection remains usable after completing the in-flight frame.
	go func() { _, e := left.Write([]byte{7}); done <- e }()
	if _, err := io.ReadFull(right, buf[:1]); err != nil || buf[0] != 7 {
		t.Fatal("main connection closed", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRaidProfileWriteStillHonorsCancelAndOwnDeadline(t *testing.T) {
	for _, explicitCancel := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancel", false: "slow_reader"}[explicitCancel], func(t *testing.T) {
			f := newCurrentCrossSceneProfileBatchFixture(t)
			left, right := net.Pipe()
			t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
			conn := &crossSceneProfileObservedConn{Conn: left, writing: make(chan struct{})}
			f.target.conn = conn
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- newCurrentRaidProfileBatch(f.service, ctx).send(f.target, f.actor, currentConnectionTownActorOwnerContext(f.target))
			}()
			select {
			case <-conn.writing:
			case <-time.After(2 * time.Second):
				t.Fatal("profile did not reach socket")
			}
			want := context.DeadlineExceeded
			if explicitCancel {
				cancel()
				want = context.Canceled
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("error=%v want=%v", err, want)
				}
			case <-time.After(createWriteTimeout + time.Second):
				t.Fatal("unbounded raid profile write")
			}
			_ = right.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := right.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
				t.Fatal("failed stream left reusable", err)
			}
			if !f.target.wireMu.TryLock() {
				t.Fatal("failed write retained wire lock")
			}
			f.target.wireMu.Unlock()
		})
	}
}

func TestRaidPartyProjectionClosedRecipientDoesNotStopOtherMembers(t *testing.T) {
	s, leader, peer, leaderConn, _, _ := prepareCurrentRaidApplicationTest(t)
	if err := s.handleGameCommand(peer, 1, 820, currentRaidApplyFixture(leader.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameCommand(leader, 1, 11, currentRaidApprovalFixture(peer.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}
	bad := &raidProjectionFailedConn{bufferConn: &bufferConn{}}
	peer.conn = bad
	state, ok := s.runtimeRaidForMember(leader.selectedCharacterID)
	if !ok {
		t.Fatal("raid absent")
	}
	// Reverse recipients so a failed first member cannot prevent delivery to
	// the healthy second member. Authoritative group membership is unchanged.
	state.Members[0], state.Members[1] = state.Members[1], state.Members[0]
	leaderConn.write.Reset()
	if err := s.broadcastRuntimeRaidRefresh(state); err != nil {
		t.Fatal("broadcast leaked recipient error", err)
	}
	if leaderConn.write.Len() == 0 || !bad.closed {
		t.Fatal("later healthy recipient missed roster")
	}
	if err := s.applyRuntimeRaidParties(state); err != nil {
		t.Fatal("party projection leaked recipient error", err)
	}
}
