package dnfbridge

import (
	"context"
	"sync"
	"testing"
	"time"
)

type partySelectReturnMemberSnapshot struct {
	stream                   []byte
	characterGeneration      uint64
	enterSelectSent          bool
	enterSelectAckSent       bool
	enterSelectContextSent   bool
	backToVillagePending     bool
	confirmedTownReturn      bool
	returnTownAckOnly        bool
	townSelectorOriginBound  bool
	townSelectorOriginCharID uint16
}

// The leader's selector return must queue behind the member's current event,
// not write op24 into a client that is still processing that event.
func TestPartySelectReturnFanoutWaitsForMemberEventLoop(t *testing.T) {
	t.Run("member return runs once after release", func(t *testing.T) {
		runPartySelectReturnOwnerQueueCase(t, false)
	})
	t.Run("stale generation drops queued return", func(t *testing.T) {
		runPartySelectReturnOwnerQueueCase(t, true)
	})
}

func runPartySelectReturnOwnerQueueCase(t *testing.T, staleGeneration bool) {
	t.Helper()
	service, leader, member := partySelectReturnFixture(t)
	t.Setenv(dungeonPartySelectReturnFanoutEnv, "")

	memberConn := member.conn.(*bufferConn)
	member.wireMu.Lock()
	memberConn.write.Reset()
	member.wireMu.Unlock()

	initial := snapshotPartySelectReturnMember(t, service, member, memberConn)
	if !initial.enterSelectSent || !initial.enterSelectAckSent || !initial.enterSelectContextSent {
		t.Fatalf("fixture must start on the dungeon selector page: %+v", initial)
	}

	release, gateDone := startPartySelectReturnMemberGate(t, service, member, staleGeneration)

	leaderDone := make(chan error, 1)
	go func() {
		leaderDone <- service.handleDungeonBackToVillage(leader, nil)
	}()
	select {
	case err := <-leaderDone:
		if err != nil {
			t.Fatalf("leader selector return: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("leader selector return waited for the member event loop")
	}

	beforeRelease := snapshotPartySelectReturnMember(t, service, member, memberConn)
	if got := partySelectReturnOp24Count(t, beforeRelease.stream); got != 0 {
		t.Fatalf("member received op24 while its event loop was gated: count=%d stream=%x", got, beforeRelease.stream)
	}
	assertPartySelectReturnSelectorStillOpen(t, beforeRelease)
	if queues := member.events.loop.QueueLengths(); queues.Medium != 1 {
		t.Fatalf("leader return was not queued behind the member event: queues=%+v", queues)
	}

	release()
	waitPartySelectReturnGate(t, gateDone)
	waitPartySelectReturnBarrier(t, service, member)

	afterRelease := snapshotPartySelectReturnMember(t, service, member, memberConn)
	if staleGeneration {
		if afterRelease.characterGeneration != initial.characterGeneration+1 {
			t.Fatalf("gate generation=%d want=%d", afterRelease.characterGeneration, initial.characterGeneration+1)
		}
		if got := partySelectReturnOp24Count(t, afterRelease.stream); got != 0 {
			t.Fatalf("stale queued return reached the member: count=%d stream=%x", got, afterRelease.stream)
		}
		assertPartySelectReturnSelectorStillOpen(t, afterRelease)
		return
	}

	if got := partySelectReturnOp24Count(t, afterRelease.stream); got != 1 {
		t.Fatalf("member op24 count=%d want=1 stream=%x", got, afterRelease.stream)
	}
	if afterRelease.enterSelectSent || afterRelease.enterSelectAckSent || afterRelease.enterSelectContextSent {
		t.Fatalf("member kept its dungeon selector context after return: %+v", afterRelease)
	}
	if !afterRelease.backToVillagePending || !afterRelease.confirmedTownReturn || !afterRelease.returnTownAckOnly {
		t.Fatalf("member did not commit the normal selector return: %+v", afterRelease)
	}
	if afterRelease.townSelectorOriginBound {
		t.Fatalf("member retained selector origin character=%d", afterRelease.townSelectorOriginCharID)
	}
}

func startPartySelectReturnMemberGate(
	t *testing.T,
	service *Service,
	member *gameSession,
	staleGeneration bool,
) (func(), <-chan error) {
	t.Helper()
	if member.events != nil {
		t.Fatal("fixture already owns member events; the queue boundary needs a real fresh loop")
	}

	loopCtx, cancelLoop := context.WithCancel(context.Background())
	member.ctx = loopCtx
	if err := service.startGameSessionEvents(loopCtx, member); err != nil {
		cancelLoop()
		t.Fatalf("start member session events: %v", err)
	}
	if member.events == nil || member.events.loop == nil {
		cancelLoop()
		t.Fatal("test requires the member's real session event loop")
	}

	gateCtx, cancelGate := context.WithTimeout(context.Background(), 5*time.Second)
	gateEntered := make(chan struct{})
	gateDone := make(chan error, 1)
	releaseCh := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseCh) })
	}
	t.Cleanup(func() {
		release()
		stopTestGameSessionEvents(t, service, member)
		cancelGate()
		cancelLoop()
	})

	go func() {
		gateDone <- service.callGameSession(gateCtx, member, "party-select-return-member-gate", func() error {
			close(gateEntered)
			select {
			case <-releaseCh:
			case <-gateCtx.Done():
				return gateCtx.Err()
			}
			if staleGeneration {
				service.mu.Lock()
				advanceGameSessionCharacterGeneration(member)
				service.mu.Unlock()
			}
			return nil
		})
	}()
	waitTestSignal(t, gateEntered, "member event loop gate did not start")
	return release, gateDone
}

func waitPartySelectReturnGate(t *testing.T, gateDone <-chan error) {
	t.Helper()
	select {
	case err := <-gateDone:
		if err != nil {
			t.Fatalf("member event loop gate: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("member event loop gate did not finish after release")
	}
}

// This medium-priority sentinel is ordered behind the queued selector return.
// Once its callback runs, both the stale check and any accepted return are done.
func waitPartySelectReturnBarrier(t *testing.T, service *Service, member *gameSession) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := service.callGameSession(ctx, member, "party-select-return-member-barrier", func() error {
		return nil
	}); err != nil {
		t.Fatalf("member event loop barrier: %v", err)
	}
}

func snapshotPartySelectReturnMember(
	t *testing.T,
	service *Service,
	member *gameSession,
	conn *bufferConn,
) partySelectReturnMemberSnapshot {
	t.Helper()
	identity, bound := service.boundGameSessionCharacterSnapshot(member)
	if !bound {
		t.Fatal("member character binding is missing")
	}

	member.wireMu.Lock()
	stream := append([]byte(nil), conn.write.Bytes()...)
	member.wireMu.Unlock()

	member.dungeon.mu.Lock()
	snapshot := partySelectReturnMemberSnapshot{
		stream:                 stream,
		characterGeneration:    identity.generation,
		enterSelectSent:        member.enterSelectDungeonSent,
		enterSelectAckSent:     member.enterSelectDungeonAckSent,
		enterSelectContextSent: member.enterSelectDungeonContextSent,
		backToVillagePending:   member.backToVillageEnterSelectPending,
		confirmedTownReturn:    member.confirmedDungeonReturnStatePending,
		returnTownAckOnly:      member.returnTownFinishLoadingAckOnly,
	}
	member.dungeon.mu.Unlock()

	member.townMu.Lock()
	snapshot.townSelectorOriginBound = member.townSelectorOriginBound
	snapshot.townSelectorOriginCharID = member.townSelectorOriginSnapshot.CharacterID
	member.townMu.Unlock()
	return snapshot
}

func assertPartySelectReturnSelectorStillOpen(t *testing.T, snapshot partySelectReturnMemberSnapshot) {
	t.Helper()
	if !snapshot.enterSelectSent || !snapshot.enterSelectAckSent || !snapshot.enterSelectContextSent {
		t.Fatalf("member selector state was cleared before the event loop was released: %+v", snapshot)
	}
	if snapshot.backToVillagePending || snapshot.confirmedTownReturn || snapshot.returnTownAckOnly {
		t.Fatalf("member town return was committed before the event loop was released: %+v", snapshot)
	}
	if !snapshot.townSelectorOriginBound || snapshot.townSelectorOriginCharID != 1002 {
		t.Fatalf("member selector origin changed before release: %+v", snapshot)
	}
}

func partySelectReturnOp24Count(t *testing.T, stream []byte) int {
	t.Helper()
	count := 0
	for _, frame := range splitUpperFrames(t, stream) {
		if frame.msgID == currentSceneTransitionMsgID {
			count++
		}
	}
	return count
}
