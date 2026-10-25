package dnfbridge

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// r195SignalConn records the first write on a member socket so the regression
// can observe "this member was already released" without timing thresholds.
type r195SignalConn struct {
	*bufferConn
	once  sync.Once
	wrote chan struct{}
}

func newR195SignalConn(conn *bufferConn) *r195SignalConn {
	return &r195SignalConn{bufferConn: conn, wrote: make(chan struct{})}
}

func (c *r195SignalConn) Write(data []byte) (int, error) {
	c.once.Do(func() { close(c.wrote) })
	return c.bufferConn.Write(data)
}

// r195PrepareLoadingTail makes the next finish-loading call run the op37/op30
// tail for a member that has not sent them yet. The PVF-dependent post-finish
// segment stays marked as already sent, matching the existing party tail tests.
func r195PrepareLoadingTail(sessions ...*gameSession) {
	for _, session := range sessions {
		session.connID = fmt.Sprintf("game-%04d", session.selectedCharacterID)
		session.currentFinishLoadingStateSent = false
		session.currentFinishLoadingCompletionSent = false
		session.postFinishLoadingPlayerStateSent = true
	}
}

func r195RegisterLoadingCycle(t *testing.T, service *Service, partyID uint16, sessions ...*gameSession) *partyDungeonLoadingCycle {
	t.Helper()
	owners := make(map[uint16]partyDungeonLoadingOwner, len(sessions))
	ready := make(map[uint16]bool, len(sessions))
	members := make([]uint16, 0, len(sessions))
	for _, session := range sessions {
		id := session.selectedCharacterID
		members = append(members, id)
		owners[id] = snapshotPartyDungeonLoadingOwner(session)
		ready[id] = true
	}
	cycle := &partyDungeonLoadingCycle{PartyID: partyID, Cycle: 1, Ready: ready, owners: owners, membersAt: members}
	service.partyDungeonLoadingMu.Lock()
	service.partyDungeonLoadingBarrierCyclesLocked()[partyID] = cycle
	service.partyDungeonLoadingMu.Unlock()
	return cycle
}

func r195LoadingReleasedLine(logText string, charID uint16) bool {
	for _, line := range strings.Split(logText, "\n") {
		if !strings.Contains(line, "kind=game-party-dungeon-loading-released") {
			continue
		}
		if strings.Contains(line, fmt.Sprintf("char_id=%d ", charID)) {
			return true
		}
	}
	return false
}

// A blocked member loop must not delay its peer. The leader's real event loop is
// held by a gate event; the follower has to finish its own release while that
// gate is still closed. The observable is the follower's first socket write plus
// the absence of the leader's "-released" line, so no sleep or timing threshold
// decides the result.
func TestR195LoadingReleaseRunsMembersOnTheirOwnLoops(t *testing.T) {
	service, leader, follower, _, _ := partyDungeonDeathFixture(t)
	logger, _ := newPeerEndpointTestLogger(t)
	service.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	r195PrepareLoadingTail(leader, follower)

	leaderConn := leader.conn.(*bufferConn)
	followerConn := follower.conn.(*bufferConn)
	leaderConn.write.Reset()
	followerConn.write.Reset()
	signal := newR195SignalConn(followerConn)
	follower.conn = signal

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	for _, session := range []*gameSession{leader, follower} {
		if err := service.startGameSessionEvents(ctx, session); err != nil {
			t.Fatal(err)
		}
	}

	partyID := uint16(4211)
	cycle := r195RegisterLoadingCycle(t, service, partyID, leader, follower)
	members := []uint16{leader.selectedCharacterID, follower.selectedCharacterID}

	gateStarted := make(chan struct{})
	gateRelease := make(chan struct{})
	var gateOnce sync.Once
	openGate := func() { gateOnce.Do(func() { close(gateRelease) }) }
	defer openGate()
	go func() {
		_ = service.callGameSession(context.Background(), leader, "r195-loading-gate", func() error {
			close(gateStarted)
			<-gateRelease
			return nil
		})
	}()
	<-gateStarted

	done := make(chan struct{})
	go func() {
		service.releasePartyDungeonLoadingCycle(partyID, cycle, members, members, "r195_parallel_release")
		close(done)
	}()

	select {
	case <-signal.wrote:
	case <-time.After(10 * time.Second):
		t.Fatalf("follower was never released while the leader's loop was blocked:\n%s", testPacketLogText(t, service))
	}
	if text := testPacketLogText(t, service); r195LoadingReleasedLine(text, leader.selectedCharacterID) {
		t.Fatalf("leader release finished before the follower could start; members are still serialized:\n%s", text)
	}

	openGate()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("release did not return after the gate opened")
	}
	text := testPacketLogText(t, service)
	for _, id := range members {
		if !r195LoadingReleasedLine(text, id) {
			t.Fatalf("member %d was not released:\n%s", id, text)
		}
	}
}

// A cycle that is no longer the live one must never place or release anybody.
func TestR195LoadingReleaseStaleCycleDoesNotRelease(t *testing.T) {
	service, leader, follower, _, _ := partyDungeonDeathFixture(t)
	logger, _ := newPeerEndpointTestLogger(t)
	service.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	r195PrepareLoadingTail(leader, follower)
	leaderConn := leader.conn.(*bufferConn)
	followerConn := follower.conn.(*bufferConn)
	leaderConn.write.Reset()
	followerConn.write.Reset()

	partyID := uint16(4212)
	cycle := r195RegisterLoadingCycle(t, service, partyID, leader, follower)
	stale := &partyDungeonLoadingCycle{PartyID: partyID, Cycle: 0, owners: cycle.owners}
	members := []uint16{leader.selectedCharacterID, follower.selectedCharacterID}
	service.releasePartyDungeonLoadingCycle(partyID, stale, members, members, "r195_stale_cycle")

	if leaderConn.write.Len() != 0 || followerConn.write.Len() != 0 {
		t.Fatal("stale cycle released a member")
	}
	if text := testPacketLogText(t, service); r195LoadingReleasedLine(text, leader.selectedCharacterID) || r195LoadingReleasedLine(text, follower.selectedCharacterID) {
		t.Fatalf("stale cycle logged a release:\n%s", text)
	}
}

// The owner guard is evaluated inside the session event: a member whose scene
// owner changed after the cycle froze its owners is skipped, while the other
// member still releases normally.
func TestR195LoadingReleaseStaleOwnerIsNotReleased(t *testing.T) {
	service, leader, follower, _, _ := partyDungeonDeathFixture(t)
	logger, _ := newPeerEndpointTestLogger(t)
	service.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	r195PrepareLoadingTail(leader, follower)
	leaderConn := leader.conn.(*bufferConn)
	followerConn := follower.conn.(*bufferConn)
	leaderConn.write.Reset()
	followerConn.write.Reset()

	partyID := uint16(4213)
	cycle := r195RegisterLoadingCycle(t, service, partyID, leader, follower)
	leader.dungeon.mu.Lock()
	leader.dungeon.runToken++
	leader.dungeon.mu.Unlock()

	members := []uint16{leader.selectedCharacterID, follower.selectedCharacterID}
	service.releasePartyDungeonLoadingCycle(partyID, cycle, members, members, "r195_stale_owner")

	text := testPacketLogText(t, service)
	// The follower's own peer-actor tail writes to the leader's socket, so the
	// release signal here is the leader's "-released" line, not its socket length.
	if r195LoadingReleasedLine(text, leader.selectedCharacterID) {
		t.Fatalf("stale owner was released:\n%s", text)
	}
	if !r195LoadingReleasedLine(text, follower.selectedCharacterID) || followerConn.write.Len() == 0 {
		t.Fatalf("valid member was not released:\n%s", text)
	}
}
