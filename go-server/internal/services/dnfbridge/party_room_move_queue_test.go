package dnfbridge

import (
	"context"
	"sync"
	"testing"
	"time"
)

type roomFollowQueueFixture struct {
	s         *Service
	peers     []*gameSession
	ctx       context.Context
	cancel    context.CancelFunc
	release   func()
	gateDone  <-chan struct{}
	request   []byte
	initial29 int
}

func countRoomFollowPackets(t *testing.T, p *gameSession, msg uint16) int {
	t.Helper()
	n := 0
	stream := p.conn.(*partyDungeonSyncConn).frames()
	for len(stream) > 0 {
		frame, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
		if frame.Header.MsgID == msg {
			n++
		}
		stream = rest
	}
	return n
}

func newRoomFollowQueueFixture(t *testing.T, beforeFollow func(*Service, *gameSession) error) roomFollowQueueFixture {
	t.Helper()
	s, peers := fourMemberReturnLoadingFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	for _, p := range peers {
		p.ctx = ctx
		if err := s.startGameSessionEvents(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range peers {
		if err := s.callGameSession(ctx, p, "queue-guard-initial-op37", func() error {
			return s.sendFinishLoadingStatus(p, make([]byte, currentFinishLoadingLegacyRequestBodySize))
		}); err != nil {
			t.Fatal(err)
		}
	}
	awaitRoomFanoutLoading(t, s, ctx, peers)
	blocked := peers[1]
	initial29 := countRoomFollowPackets(t, blocked, 29)
	gate, started, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(func() {
		release()
		for _, p := range peers {
			stopTestGameSessionEvents(t, s, p)
		}
		cancel()
	})
	go func() {
		_ = s.callGameSession(ctx, blocked, "queue-guard-owner-gate", func() error { close(started); <-gate; return nil })
		close(done)
	}()
	waitTestSignal(t, started, "queue-guard owner did not block")
	filled := 0
	if beforeFollow != nil {
		if err := s.postGameSessionCharacterEvent(blocked, "owner-intent-before-follow", blocked.selectedCharacterID, blocked.characterGeneration, func() error { return beforeFollow(s, blocked) }); err != nil {
			t.Fatal(err)
		}
		filled++
	}
	for i := filled; i < gameSessionEventQueueSize; i++ {
		if err := s.tryPostGameSessionCharacterEvent(blocked, "queue-guard-filler", 0, 0, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	request := make([]byte, 100)
	request[0] = 1
	if err := s.callGameSession(ctx, peers[0], "queue-guard-leader-move", func() error { return s.handleDungeonMoveMap(peers[0], request) }); err != nil {
		t.Fatal(err)
	}
	q := &blocked.partyRoomMove
	q.mu.Lock()
	retained := q.pending != nil && q.pending.timer != nil
	q.mu.Unlock()
	if !retained {
		t.Fatal("positive control did not retain the full recipient queue follow")
	}
	return roomFollowQueueFixture{s, peers, ctx, cancel, release, done, request, initial29}
}

func waitRoomFollowQueueEmpty(t *testing.T, p *gameSession) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		q := &p.partyRoomMove
		q.mu.Lock()
		empty := q.pending == nil
		q.mu.Unlock()
		if empty {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("room follow pending delivery did not retire")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRoomFollowMailboxCoalescesAndKeepsOwnedRequest(t *testing.T) {
	f := newRoomFollowQueueFixture(t, nil)
	p := f.peers[1]
	q := &p.partyRoomMove
	q.mu.Lock()
	delivery := q.pending
	q.mu.Unlock()
	f.request[0] = 255
	if delivery.body[0] != 1 {
		t.Fatal("queued follow retained caller-owned mutable body")
	}
	for i := 0; i < 128; i++ {
		if err := f.s.postPartyRoomMove(delivery.target, delivery.body, delivery.plan, delivery.retired); err != nil {
			t.Fatal(err)
		}
	}
	q.mu.Lock()
	same := q.pending == delivery && delivery.timer != nil
	q.mu.Unlock()
	if !same || p.events.loop.QueueLengths().Medium != gameSessionEventQueueSize {
		t.Fatal("duplicate fanout expanded the retained request or full owner queue")
	}
	f.release()
	<-f.gateDone
	waitRoomFollowQueueEmpty(t, p)
	if countRoomFollowPackets(t, p, 29) != f.initial29+1 {
		t.Fatal("recovered follow did not send exactly one start-map")
	}
	if err := f.s.callGameSession(f.ctx, p, "repeat-exact-committed-follow", func() error { return f.s.deliverPartyRoomMove(delivery) }); err != nil {
		t.Fatal(err)
	}
	if countRoomFollowPackets(t, p, 29) != f.initial29+1 {
		t.Fatal("already committed follow replay sent another start-map")
	}
}

func TestRoomFollowMailboxRejectsStaleTargets(t *testing.T) {
	for _, mode := range []string{"generation", "epoch", "runtime", "visit", "token", "instance", "context", "closed", "stopped", "revoked", "membership"} {
		t.Run(mode, func(t *testing.T) {
			f := newRoomFollowQueueFixture(t, nil)
			p := f.peers[1]
			var stopped <-chan error
			clearBeforeRecovery := false
			switch mode {
			case "generation":
				f.s.mu.Lock()
				advanceGameSessionCharacterGeneration(p)
				f.s.mu.Unlock()
				clearBeforeRecovery = true
			case "epoch":
				p.dungeonPlacementEpoch.Add(1)
				clearBeforeRecovery = true
			case "runtime", "visit", "token", "instance":
				p.dungeon.mu.Lock()
				switch mode {
				case "runtime":
					replacement := *p.dungeon.runtime
					p.dungeon.runtime = &replacement
				case "visit":
					p.dungeon.runtime.loadingRoomVisit++
				case "token":
					p.dungeon.runToken++
				case "instance":
					p.dungeon.runtime.partyInstance = &dungeonPartyInstance{}
				}
				p.dungeon.mu.Unlock()
			case "context":
				f.cancel()
				clearBeforeRecovery = true
			case "closed":
				f.s.beginGameSessionClose(p)
				clearBeforeRecovery = true
			case "stopped":
				done := make(chan error, 1)
				stopped = done
				go func() { done <- f.s.stopGameSessionEvents(context.Background(), p) }()
				clearBeforeRecovery = true
			case "revoked":
				p.loginRevoked.Store(true)
				clearBeforeRecovery = true
			case "membership":
				manager := f.s.runtimePartyManagerForService()
				before, _ := manager.SnapshotByUser(p.selectedCharacterID, 0)
				if r := manager.LeaveDungeonExpected(p.selectedCharacterID, p.characterGeneration, before.ID); !r.OK {
					t.Fatal(r.Reason)
				}
				clearBeforeRecovery = true
			}
			if clearBeforeRecovery {
				waitRoomFollowQueueEmpty(t, p)
			}
			f.release()
			<-f.gateDone
			if stopped != nil {
				if err := <-stopped; err != nil {
					t.Fatal(err)
				}
			}
			waitRoomFollowQueueEmpty(t, p)
			stopTestGameSessionEvents(t, f.s, p)
			if countRoomFollowPackets(t, p, 29) != f.initial29 {
				t.Fatal("old follow wrote into an invalidated target")
			}
			p.dungeon.mu.Lock()
			scene, _ := p.dungeon.runtime.Session.Scene()
			p.dungeon.mu.Unlock()
			if scene.Coordinate.X != 0 {
				t.Fatal("old follow moved invalidated runtime")
			}
		})
	}
}

func TestRoomFollowOldRequestCannotReplaceNewVisit(t *testing.T) {
	f := newRoomFollowQueueFixture(t, nil)
	p := f.peers[1]
	q := &p.partyRoomMove
	q.mu.Lock()
	old := q.pending
	q.mu.Unlock()
	p.dungeon.mu.Lock()
	p.dungeonPlacementEpoch.Add(1)
	p.dungeon.runtime.loadingRoomVisit++
	freshTarget := snapshotPartyRoomMoveTargetLocked(old.target.identity)
	p.dungeon.mu.Unlock()
	if err := f.s.postPartyRoomMove(freshTarget, old.body, old.plan, old.retired); err != nil {
		t.Fatal(err)
	}
	q.mu.Lock()
	fresh := q.pending
	q.mu.Unlock()
	if fresh == nil || fresh == old {
		t.Fatal("new source visit did not retain its own request")
	}
	f.s.submitPartyRoomMove(old)
	if err := f.s.postPartyRoomMove(old.target, old.body, old.plan, old.retired); err != nil {
		t.Fatal(err)
	}
	q.mu.Lock()
	kept := q.pending == fresh
	q.mu.Unlock()
	if !kept {
		t.Fatal("old timer or producer discarded the new visit request")
	}
	f.release()
	<-f.gateDone
	waitRoomFollowQueueEmpty(t, p)
	if countRoomFollowPackets(t, p, 29) != f.initial29+1 {
		t.Fatal("new visit did not receive exactly one retained follow")
	}
}

func TestRoomFollowDoesNotCancelQueuedPersonalReturn(t *testing.T) {
	f := newRoomFollowQueueFixture(t, func(s *Service, p *gameSession) error { return s.handleDungeonGiveupGame(p, nil) })
	p := f.peers[1]
	f.release()
	<-f.gateDone
	waitRoomFollowQueueEmpty(t, p)
	pending := false
	if err := f.s.callGameSession(f.ctx, p, "observe-personal-return-not-cancelled", func() error {
		pending = p.dungeon.runtime != nil && p.dungeon.runtime.townReturnPending && p.dungeon.runtime.townReturnOp24Sent
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !pending {
		t.Fatal("internal copied leader move cancelled the recipient's accepted personal return")
	}
	if countRoomFollowPackets(t, p, 29) != f.initial29 || countRoomFollowPackets(t, p, 24) != 1 {
		t.Fatal("pending personal return received an unsolicited room transition")
	}
}
