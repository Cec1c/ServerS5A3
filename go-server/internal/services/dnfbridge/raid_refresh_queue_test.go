package dnfbridge

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

func waitRaidRefreshIdle(t *testing.T, p *gameSession) {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		p.raidRefresh.mu.Lock()
		idle := p.raidRefresh.pending == nil
		p.raidRefresh.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("dirty raid refresh did not finish or retire")
}

func TestRaidPresenceBroadcastDoesNotWaitForSlowRecipient(t *testing.T) {
	s, slow, fast, _, fc, state := prepareApprovedCurrentRaid(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, p := range []*gameSession{slow, fast} {
		p.ctx = ctx
		if err := s.startGameSessionEvents(ctx, p); err != nil {
			t.Fatal(err)
		}
		defer stopTestGameSessionEvents(t, s, p)
	}
	left, right := net.Pipe()
	blocked := &raidRewardGatedConn{Conn: left, writing: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(blocked.release); _ = right.Close(); _ = left.Close() }) }
	defer release()
	slow.conn = blocked
	fc.write.Reset()
	done := make(chan struct{})
	go func() { s.publishCurrentRaidPresence(state, 0); close(done) }()
	select {
	case <-blocked.writing:
	case <-time.After(5 * time.Second):
		t.Fatal("slow recipient never entered write")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("presence broadcast waits for slow recipient")
	}
	if err := s.callGameSession(ctx, fast, "drain-presence-test", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, f := range splitUpperFrames(t, fc.write.Bytes()) {
		if f.msgID == 591 {
			seen = true
		}
	}
	if !seen {
		t.Error("healthy recipient received no roster while slow write was still blocked")
	}
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("broadcast did not retire")
	}
}

func TestRaidProgressBurstUsesSingleQueuedOwnerEvent(t *testing.T) {
	s, p, _, battle := raidLifeRuntimeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.ctx = ctx
	if err := s.startGameSessionEvents(ctx, p); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, p)
	started, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer release()
	done := make(chan error, 1)
	go func() {
		done <- s.callGameSession(ctx, p, "busy-owner-test", func() error { close(started); <-gate; return nil })
	}()
	waitTestSignal(t, started, "owner not blocked")
	for i := 0; i < 128; i++ {
		s.raidMu.Lock()
		battle.ProgressVersion++
		state := cloneRuntimeRaidState(*s.runtimeRaidByMemberLocked(p.selectedCharacterID))
		s.raidMu.Unlock()
		s.queueCurrentRaidDungeonProgress(state)
	}
	if !af10WaitRaidProgressQueued(p) {
		t.Fatal("progress was never queued")
	}
	// Let the old per-broadcast goroutines reach SubmitBlocking before counting.
	time.Sleep(150 * time.Millisecond)
	if n := p.events.loop.QueueLengths().Medium; n != 1 {
		t.Errorf("128 updates retained %d queued owner events; want one cumulative update", n)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.callGameSession(ctx, p, "drain-progress-test", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestRaidPresenceQueueFullRetriesLatestOrRetiresGeneration(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "recover_latest", true: "changed_generation"}[stale], func(t *testing.T) {
			s, p, member, conn, _, state := prepareApprovedCurrentRaid(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p.ctx = ctx
			if err := s.startGameSessionEvents(ctx, p); err != nil {
				t.Fatal(err)
			}
			defer stopTestGameSessionEvents(t, s, p)
			started, gate := make(chan struct{}), make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(gate) }) }
			defer release()
			done := make(chan error, 1)
			go func() {
				done <- s.callGameSession(ctx, p, "full-queue-owner", func() error {
					close(started)
					<-gate
					if stale {
						advanceGameSessionCharacterGeneration(p)
					}
					return nil
				})
			}()
			waitTestSignal(t, started, "owner not blocked")
			for i := 0; i < gameSessionEventQueueSize; i++ {
				if err := s.tryPostGameSessionCharacterEvent(p, "filler", p.selectedCharacterID, p.characterGeneration, func() error { return nil }); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 128; i++ {
				s.publishCurrentRaidPresence(state, member.selectedCharacterID)
			}
			s.raidMu.Lock()
			live := s.raids[state.RaidKey]
			live.Members[runtimeRaidMemberIndex(*live, member.selectedCharacterID)].UserState = 0
			state = cloneRuntimeRaidState(*live)
			s.raidMu.Unlock()
			want, err := s.buildRuntimeRaidRosterBody(state)
			if err != nil {
				t.Fatal(err)
			}
			s.publishCurrentRaidPresence(state, member.selectedCharacterID)
			p.raidRefresh.mu.Lock()
			pending := p.raidRefresh.pending
			retry := pending != nil && pending.timer != nil && !pending.queued && !pending.running
			p.raidRefresh.mu.Unlock()
			if !retry || p.events.loop.QueueLengths().Medium != gameSessionEventQueueSize {
				t.Fatal("full queue lost dirty update or grew beyond bound")
			}
			release()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			waitRaidRefreshIdle(t, p)
			if err := s.callGameSession(ctx, p, "drain-queue-full-test", func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			if stale {
				if conn.write.Len() != 0 {
					t.Fatal("retired generation received old presence")
				}
				return
			}
			frames := splitUpperFrames(t, conn.write.Bytes())
			if len(frames) != 1 || frames[0].msgID != 591 || !bytes.Equal(frames[0].body, want) {
				t.Fatalf("expected exactly latest presence after queue recovered: frames=%d", len(frames))
			}
		})
	}
}

func TestRaidSeatExpiryClockDoesNotWaitForRecipientWrite(t *testing.T) {
	s, p, member, _, _, state := prepareApprovedCurrentRaid(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.ctx = ctx
	if err := s.startGameSessionEvents(ctx, p); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, p)
	identity, ok := s.boundGameSessionCharacterSnapshot(member)
	if !ok {
		t.Fatal("member not bound")
	}
	s.unbindGameSession(member)
	q := newFakeCurrentDungeonDeathTimerQueue()
	s.gameplayTimers = q
	s.raidMu.Lock()
	live := s.raids[state.RaidKey]
	s.currentRaidDisconnects = map[uint16]currentRaidDisconnectSeat{identity.character: {Raid: live, RaidKey: state.RaidKey, CharacterID: identity.character, Session: member, Generation: identity.generation, AccountID: member.accountID, DisconnectedAt: q.Now().Add(-time.Minute), ExpiresAt: q.Now()}}
	s.raidMu.Unlock()
	left, right := net.Pipe()
	blocked := &raidRewardGatedConn{Conn: left, writing: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(blocked.release); _ = left.Close(); _ = right.Close() }) }
	defer release()
	p.conn = blocked
	if err := s.scheduleCurrentRaidClock(ctx); err != nil {
		t.Fatal(err)
	}
	task := q.task(t, 0)
	done := make(chan struct{})
	go func() { q.fire(task, false); close(done) }()
	select {
	case <-blocked.writing:
	case <-time.After(5 * time.Second):
		t.Fatal("expiry did not project roster")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shared raid clock blocked on socket")
	}
	if _, found := s.runtimeRaidForMember(member.selectedCharacterID); found {
		t.Fatal("expired seat retained")
	}
	if scheduled, _, _ := q.counts(); scheduled < 2 {
		t.Fatal("clock was not rescheduled while peer remained blocked")
	}
	release()
	waitRaidRefreshIdle(t, p)
}
