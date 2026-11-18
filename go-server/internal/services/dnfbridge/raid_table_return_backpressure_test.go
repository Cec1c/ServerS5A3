package dnfbridge

import (
	"context"
	"sync"
	"testing"
	"time"
)

type raidTableReturnBlockedFixture struct {
	completionReturnFixture
	ctx     context.Context
	cancel  context.CancelFunc
	release func()
	done    <-chan error
}

func blockRaidTableReturnRecipient(t *testing.T) raidTableReturnBlockedFixture {
	t.Helper()
	f := syntheticRaidTablePartyFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.member.ctx = ctx
	if err := f.service.startGameSessionEvents(ctx, f.member); err != nil {
		cancel()
		t.Fatal(err)
	}
	started, gate, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(func() {
		release()
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("blocked owner did not finish")
		}
		cancel()
		stopTestGameSessionEvents(t, f.service, f.member)
	})
	go func() {
		done <- f.service.callGameSession(ctx, f.member, "table-return-owner-gate", func() error {
			close(started)
			<-gate
			return nil
		})
		close(finished)
	}()
	waitTestSignal(t, started, "recipient owner did not block")
	for i := 0; i < gameSessionEventQueueSize; i++ {
		if err := f.service.tryPostGameSessionCharacterEvent(f.member, "table-return-filler", 0, 0, func() error { return nil }); err != nil {
			t.Fatalf("fill owner queue at %d: %v", i, err)
		}
	}
	return raidTableReturnBlockedFixture{f, ctx, cancel, release, done}
}

func TestRaidTableReturnSurvivesFullRecipientQueue(t *testing.T) {
	f := blockRaidTableReturnRecipient(t)
	s := f.service
	started := time.Now()
	if err := raidTableDispatch(s, f.leader, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("leader waited for recipient queue capacity")
	}
	owner := f.leaderRuntime.raidTablePartyReturn
	if owner == nil || !f.leaderRuntime.townReturnOp24Sent {
		t.Fatal("leader's actual table action did not establish return ownership")
	}
	q := &f.member.raidTableReturn
	q.mu.Lock()
	first := q.pending
	retained := first != nil && first.timer != nil && !first.queued
	q.mu.Unlock()
	if !retained {
		t.Fatal("full recipient queue did not retain a bounded retry")
	}
	for i := 0; i < 128; i++ {
		s.queueCurrentRaidTablePartyReturn(owner)
	}
	q.mu.Lock()
	same := q.pending == first
	q.mu.Unlock()
	if !same {
		t.Fatal("duplicate accepted return replaced its single pending delivery")
	}
	if f.member.events.loop.QueueLengths().Medium != gameSessionEventQueueSize {
		t.Fatal("full owner queue changed capacity")
	}
	f.release()
	if err := <-f.done; err != nil {
		t.Fatal(err)
	}
	awaitRaidTableReturnTransition(t, f, f.memberRuntime)
}

func awaitRaidTableReturnTransition(t *testing.T, f raidTableReturnBlockedFixture, expected *runtimeDungeonState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		pending, transitions := false, 0
		if err := f.service.callGameSession(f.ctx, f.member, "inspect-table-return", func() error {
			pending = f.member.dungeon.runtime == expected && expected.townReturnPending && expected.townReturnOp24Sent
			for _, frame := range splitUpperFrames(t, f.memberConn.write.Bytes()) {
				if frame.msgID == 24 {
					transitions++
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if pending {
			if transitions != 1 {
				t.Fatalf("recipient town transitions=%d want1 despite duplicate requests", transitions)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recipient queue drained but the accepted party return was lost")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitRaidTableReturnQueueEmpty(t *testing.T, session *gameSession) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		q := &session.raidTableReturn
		q.mu.Lock()
		empty := q.pending == nil
		q.mu.Unlock()
		if empty {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("invalidated return retained its retry")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRaidTableReturnRetryRejectsInvalidatedTargets(t *testing.T) {
	for _, mode := range []string{"member_generation", "leader_generation", "run", "phase", "runtime", "context", "owner_closed", "owner_stopped"} {
		t.Run(mode, func(t *testing.T) {
			f := blockRaidTableReturnRecipient(t)
			s := f.service
			if err := raidTableDispatch(s, f.leader, []byte{1, 2}); err != nil {
				t.Fatal(err)
			}
			q := &f.member.raidTableReturn
			q.mu.Lock()
			retry := q.pending != nil && q.pending.timer != nil
			q.mu.Unlock()
			if !retry {
				t.Fatal("positive return control did not reach retry state")
			}
			var stopped <-chan error
			switch mode {
			case "member_generation", "leader_generation":
				p := f.member
				if mode == "leader_generation" {
					p = f.leader
				}
				s.mu.Lock()
				advanceGameSessionCharacterGeneration(p)
				s.mu.Unlock()
			case "run", "phase", "runtime":
				s.raidMu.Lock()
				b := s.raids[f.memberRuntime.raidEntry.RaidKey].Battle
				p := b.Participation[f.member.selectedCharacterID]
				switch mode {
				case "run":
					p.RunID = "replacement-run"
				case "phase":
					b.Phase++
				case "runtime":
					replacement := *f.memberRuntime
					p.Runtime = &replacement
				}
				b.Participation[f.member.selectedCharacterID] = p
				s.raidMu.Unlock()
			case "context":
				f.cancel()
			case "owner_closed":
				s.beginGameSessionClose(f.member)
			case "owner_stopped":
				done := make(chan error, 1)
				stopped = done
				go func() { done <- s.stopGameSessionEvents(context.Background(), f.member) }()
			}
			waitRaidTableReturnQueueEmpty(t, f.member)
			f.release()
			<-f.done
			if stopped != nil {
				if err := <-stopped; err != nil {
					t.Fatal(err)
				}
			}
			stopTestGameSessionEvents(t, s, f.member)
			if f.memberRuntime.townReturnPending || f.memberConn.write.Len() != 0 {
				t.Fatal("retired target received an old town transition")
			}
		})
	}
}

func TestRaidTableReturnValidSquadLeaderDoesNotWaitForRecipientDungeonLock(t *testing.T) {
	f := syntheticRaidTablePartyFixture(t)
	s := f.service
	// The ordinary party leader need not lead the entire attack raid.
	s.raidMu.Lock()
	s.raids[f.leaderRuntime.raidEntry.RaidKey].LeaderID = f.member.selectedCharacterID
	s.raidMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	f.member.ctx = ctx
	if err := s.startGameSessionEvents(ctx, f.member); err != nil {
		cancel()
		t.Fatal(err)
	}
	f.member.dungeon.mu.Lock()
	var once sync.Once
	release := func() { once.Do(func() { f.member.dungeon.mu.Unlock() }) }
	t.Cleanup(func() {
		release()
		cancel()
		stopTestGameSessionEvents(t, s, f.member)
	})
	done := make(chan error, 1)
	go func() { done <- raidTableDispatch(s, f.leader, []byte{1, 2}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		release()
		<-done
		t.Fatal("valid squad leader waited for the recipient's dungeon lock")
	}
	release()
	if err := s.callGameSession(ctx, f.member, "drain-squad-table-return", func() error {
		if !f.memberRuntime.townReturnPending || !f.memberRuntime.townReturnOp24Sent {
			t.Error("valid squad leader did not return its own party")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRaidTableReturnOldRetryDoesNotDiscardReplacementRequest(t *testing.T) {
	f := blockRaidTableReturnRecipient(t)
	s := f.service
	if err := raidTableDispatch(s, f.leader, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	q := &f.member.raidTableReturn
	q.mu.Lock()
	old := q.pending
	q.mu.Unlock()
	if old == nil {
		t.Fatal("original request did not reach the pending queue")
	}
	replacement := *f.memberRuntime
	f.member.dungeon.mu.Lock()
	f.member.dungeon.runtime = &replacement
	f.member.dungeon.mu.Unlock()
	s.raidMu.Lock()
	b := s.raids[replacement.raidEntry.RaidKey].Battle
	p := b.Participation[f.member.selectedCharacterID]
	p.Runtime = &replacement
	b.Participation[f.member.selectedCharacterID] = p
	s.raidMu.Unlock()
	s.queueCurrentRaidTablePartyReturn(f.leaderRuntime.raidTablePartyReturn)
	q.mu.Lock()
	fresh := q.pending
	valid := fresh != nil && fresh != old && fresh.runtime == &replacement
	q.mu.Unlock()
	if !valid {
		t.Fatal("replacement admitted runtime lost its own pending return")
	}
	s.submitCurrentRaidTableReturn(old)
	q.mu.Lock()
	stillFresh := q.pending == fresh
	q.mu.Unlock()
	if !stillFresh {
		t.Fatal("stale retry cleared the replacement request")
	}
	f.release()
	if err := <-f.done; err != nil {
		t.Fatal(err)
	}
	awaitRaidTableReturnTransition(t, f, &replacement)
	if f.memberRuntime.townReturnPending {
		t.Fatal("old runtime was returned alongside its replacement")
	}
}
