package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// Direct unit boundary for lockCurrentCrossSceneProfileForDelivery. Initially
// only a dungeon mutex is occupied, so its first Done query is the retry select
// AFTER releasing appearance and wire. No production hook or lock loop is copied.
// Full sendFrames wraps the context; the retry tests deliberately call the new
// lock helper directly and use the production projector + locked wire codec.
// The mode3 test below uses the complete sendCurrentCrossSceneProfileFrame API.
type p05HPContentionContext struct {
	context.Context
	once     sync.Once
	retrying chan struct{}
	resume   chan struct{}
}

func (c *p05HPContentionContext) Done() <-chan struct{} {
	c.once.Do(func() {
		close(c.retrying)
		select {
		case <-c.resume:
		case <-c.Context.Done():
		}
	})
	return c.Context.Done()
}

type p05HPDeliveryResult struct {
	delivered bool
	err       error
}

// Exercise the new lock/sample primitive and the exact production HP/wire
// writers. This small adapter omits sendFrames' socket deadline machinery;
// it must not be presented as testing that machinery or public game dispatch.
func p05HPDeliverUnderNewLock(ctx context.Context, f *townPartyActorHPFixture, body []byte) p05HPDeliveryResult {
	realtime, err := f.service.lockCurrentCrossSceneProfileForDelivery(ctx, f.target, f.actor, true)
	if err != nil {
		return p05HPDeliveryResult{err: err}
	}
	defer f.actor.crossSceneAppearanceMu.Unlock()
	defer f.target.wireMu.Unlock()
	if err := ctx.Err(); err != nil {
		return p05HPDeliveryResult{err: err}
	}
	if !f.service.currentCrossSceneProfileMembership(f.target, f.actor, f.target.selectedCharacterID, f.actor.selectedCharacterID) {
		return p05HPDeliveryResult{err: errors.New("fixture membership changed")}
	}
	projected, err := projectCurrentTownPartyHPIntoProfile(body, realtime, f.actor.selectedCharacterID)
	if err != nil {
		return p05HPDeliveryResult{err: err}
	}
	err = f.service.sendGameUpperRawClassCodecLocked(f.target, 2, projected, 0, true)
	return p05HPDeliveryResult{delivered: err == nil, err: err}
}

func p05HPContentionFixture(t *testing.T, mode3 bool) (*townPartyActorHPFixture, []byte) {
	t.Helper()
	f := newTownPartyActorHPFixture(t, true) // existing two-member fixture
	f.target.accountID, f.actor.accountID = "dnf:1", "dnf:2"
	var body []byte
	if mode3 {
		if err := f.service.sendCurrentCrossScenePeerProfile(context.Background(), f.target, f.actor, f.target.selectedCharacterID, f.actor.selectedCharacterID); err != nil {
			t.Fatal(err)
		}
		frames := splitUpperFrames(t, f.targetConn.write.Bytes())
		if len(frames) != 2 || len(frames[1].body) == 0 || frames[1].body[0] != currentCrossSceneMode3Subtype {
			t.Fatal("existing inspection sender did not provide a real mode3 body")
		}
		body = append([]byte(nil), frames[1].body...)
	} else {
		batch := newCurrentCrossSceneProfileBatch(f.service, context.Background())
		owner := currentConnectionTownActorOwnerContext(f.target)
		if err := batch.send(f.target, f.actor, owner); err != nil {
			t.Fatal(err)
		}
		body = append([]byte(nil), batch.bodies[currentCrossSceneProfileKey{actor: f.actor.selectedCharacterID, owner: owner}].data...)
		if len(body) == 0 || body[0] != currentCrossSceneMode8Subtype || body[townMode0FixtureHPOffset(t, body)] != 0 {
			t.Fatal("existing batch must provide an unprojected mode8 template")
		}
	}
	f.reset() // sequence is NOT reset; each test records the actual next value
	return f, body
}

func p05HPReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * createWriteTimeout):
		t.Fatal("contention handshake did not progress")
		var zero T
		return zero
	}
}

func p05HPJoinCleanup(t *testing.T, exited <-chan struct{}) {
	t.Helper()
	select {
	case <-exited:
	case <-time.After(2 * createWriteTimeout):
		t.Error("contention fixture goroutine did not terminate")
	}
}

func p05HPRequireFree(t *testing.T, name string, mu *sync.Mutex) {
	t.Helper()
	if !mu.TryLock() {
		t.Fatalf("%s retained at retry/exit boundary", name)
	}
	mu.Unlock()
}

func TestP05WireHPContentionHandlerProgress(t *testing.T) {
	for _, held := range []string{"viewer", "actor"} {
		t.Run(held, func(t *testing.T) {
			f, body := p05HPContentionFixture(t, false)
			hpBody := f.service.currentTownPartyRealtimeBody(f.target, f.actor)
			if len(hpBody) == 0 {
				t.Fatal("missing current party HP before handler takes its dungeon lock")
			}
			busy, other := &f.target.dungeon.mu, &f.actor.dungeon.mu
			if held == "actor" {
				busy, other = other, busy
			}
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			gate := &p05HPContentionContext{Context: ctx, retrying: make(chan struct{}), resume: make(chan struct{})}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(gate.resume) }) }
			handlerHeld, sendNow := make(chan struct{}), make(chan struct{})
			wireBusy := make(chan bool, 1)
			handlerDone, handlerExited := make(chan error, 1), make(chan struct{})
			profileDone, profileExited := make(chan p05HPDeliveryResult, 1), make(chan struct{})
			wireOwned := false
			t.Cleanup(func() {
				cancel()
				if wireOwned {
					f.target.wireMu.Unlock()
				}
				release()
				p05HPJoinCleanup(t, handlerExited)
				p05HPJoinCleanup(t, profileExited)
			})
			go func() {
				defer close(handlerExited)
				busy.Lock()
				defer busy.Unlock()
				close(handlerHeld)
				select {
				case <-sendNow:
				case <-ctx.Done():
					handlerDone <- ctx.Err()
					return
				}
				// A failed probe establishes the handler owns dungeon while its
				// destination wire is unavailable; no scheduler delay is assumed.
				available := f.target.wireMu.TryLock()
				if available {
					f.target.wireMu.Unlock()
				}
				wireBusy <- !available
				handlerDone <- f.service.sendGameUpperRawClassCodec(f.target, 0x0099, hpBody, 0, true)
			}()
			seq := f.target.upperSeq
			go func() {
				defer close(profileExited)
				// Handler ownership is established before attempting delivery.
				select {
				case <-handlerHeld:
				case <-ctx.Done():
					profileDone <- p05HPDeliveryResult{err: ctx.Err()}
					return
				}
				profileDone <- p05HPDeliverUnderNewLock(gate, f, body)
			}()
			p05HPReceive(t, gate.retrying)
			p05HPRequireFree(t, "appearance", &f.actor.crossSceneAppearanceMu)
			p05HPRequireFree(t, "wire", &f.target.wireMu)
			p05HPRequireFree(t, "other dungeon", other)
			if f.target.upperSeq != seq || f.targetConn.write.Len() != 0 {
				t.Fatal("busy dungeon attempt allocated a sequence or wrote a frame")
			}
			// Keep the profile paused after its proven release. Hold wire only
			// for the handler's blocked-wire handshake, then let the real send
			// finish and release dungeon before resuming the profile retry.
			f.target.wireMu.Lock()
			wireOwned = true
			close(sendNow)
			if !p05HPReceive(t, wireBusy) {
				t.Fatal("handler did not observe the same destination wire occupied")
			}
			f.target.wireMu.Unlock()
			wireOwned = false
			if err := p05HPReceive(t, handlerDone); err != nil {
				t.Fatalf("handler send while holding %s dungeon: %v", held, err)
			}
			p05HPReceive(t, handlerExited) // includes its deferred dungeon unlock
			release()
			got := p05HPReceive(t, profileDone)
			p05HPReceive(t, profileExited)
			if got.err != nil || !got.delivered {
				t.Fatalf("profile after handler: delivered=%t err=%v", got.delivered, got.err)
			}
			p05HPRequireFree(t, "appearance", &f.actor.crossSceneAppearanceMu)
			p05HPRequireFree(t, "wire", &f.target.wireMu)
			p05HPRequireFree(t, "handler dungeon", busy)
			frames := splitUpperFrames(t, f.targetConn.write.Bytes())
			if len(frames) != 2 || frames[0].msgID != 153 || !bytes.Equal(frames[0].body, hpBody) || f.target.upperSeq != seq+2 {
				t.Fatalf("want handler op153 then profile, exactly two sequences; frames=%v seq=%d->%d", townPartyActorHPFrameIDs(frames), seq, f.target.upperSeq)
			}
			profile := requireCurrentCrossSceneProfileBatchMode8(t, frames[1:], f.actor.selectedCharacterID, currentConnectionTownActorOwnerContext(f.target))
			if hp := profile[townMode0FixtureHPOffset(t, profile)]; hp != 100 {
				t.Fatalf("wire-locked profile HP=%d want established fixture HP100", hp)
			}
			t.Logf("order=%s dungeon held -> busy retry releases both -> handler wire busy -> op153 -> dungeon unlock -> profile HP100; seq=%d->%d", held, seq, f.target.upperSeq)
		})
	}
}

func TestP05WireHPContentionCancellationReleasesLocks(t *testing.T) {
	for _, held := range []string{"viewer", "actor"} {
		for _, end := range []string{"cancel", "deadline"} {
			t.Run(held+"/"+end, func(t *testing.T) {
				f, body := p05HPContentionFixture(t, false)
				busy, other := &f.target.dungeon.mu, &f.actor.dungeon.mu
				if held == "actor" {
					busy, other = other, busy
				}
				busy.Lock()
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				gate := &p05HPContentionContext{Context: ctx, retrying: make(chan struct{}), resume: make(chan struct{})}
				done, exited := make(chan p05HPDeliveryResult, 1), make(chan struct{})
				t.Cleanup(func() {
					cancel()
					close(gate.resume)
					busy.Unlock()
					p05HPJoinCleanup(t, exited)
				})
				seq := f.target.upperSeq
				go func() {
					defer close(exited)
					done <- p05HPDeliverUnderNewLock(gate, f, body)
				}()
				p05HPReceive(t, gate.retrying)
				p05HPRequireFree(t, "appearance at retry", &f.actor.crossSceneAppearanceMu)
				p05HPRequireFree(t, "wire at retry", &f.target.wireMu)
				p05HPRequireFree(t, "other dungeon at retry", other)
				want := context.DeadlineExceeded
				if end == "cancel" {
					want = context.Canceled
					cancel()
				}
				// In deadline case the actual context timer ends the wait.
				// No sleep drives scheduling or pretends a mutex was released.
				got := p05HPReceive(t, done)
				p05HPReceive(t, exited)
				if got.delivered || !errors.Is(got.err, want) {
					t.Fatalf("delivered=%t err=%v want=%v", got.delivered, got.err, want)
				}
				p05HPRequireFree(t, "appearance after exit", &f.actor.crossSceneAppearanceMu)
				p05HPRequireFree(t, "wire after exit", &f.target.wireMu)
				p05HPRequireFree(t, "other dungeon after exit", other)
				if f.target.upperSeq != seq || f.targetConn.write.Len() != 0 {
					t.Fatal("cancelled/timed-out contention advanced sequence or sent bytes")
				}
				t.Logf("%s dungeon busy, %s after observed retry: no wire bytes/sequence change and both delivery locks free", held, end)
			})
		}
	}
}

func TestP05WireHPContentionMode3DoesNotTakeDungeonLocks(t *testing.T) {
	f, body := p05HPContentionFixture(t, true)
	f.target.dungeon.mu.Lock()
	f.actor.dungeon.mu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	done, exited := make(chan p05HPDeliveryResult, 1), make(chan struct{})
	t.Cleanup(func() {
		cancel()
		f.actor.dungeon.mu.Unlock()
		f.target.dungeon.mu.Unlock()
		p05HPJoinCleanup(t, exited)
	})
	seq := f.target.upperSeq
	go func() {
		defer close(exited)
		delivered, err := f.service.sendCurrentCrossSceneProfileFrame(ctx, f.target, f.actor, body, func() bool {
			return f.service.currentCrossSceneProfileMembership(f.target, f.actor, f.target.selectedCharacterID, f.actor.selectedCharacterID)
		})
		done <- p05HPDeliveryResult{delivered: delivered, err: err}
	}()
	got := p05HPReceive(t, done)
	p05HPReceive(t, exited)
	if got.err != nil || !got.delivered {
		t.Fatalf("mode3 blocked by held dungeon locks: delivered=%t err=%v", got.delivered, got.err)
	}
	frames := splitUpperFrames(t, f.targetConn.write.Bytes())
	if len(frames) != 1 || frames[0].msgID != 2 || !bytes.Equal(frames[0].body, body) || f.target.upperSeq != seq+1 {
		t.Fatal(fmt.Sprintf("mode3 changed body/count/sequence: frames=%v seq=%d->%d", townPartyActorHPFrameIDs(frames), seq, f.target.upperSeq))
	}
	p05HPRequireFree(t, "mode3 appearance", &f.actor.crossSceneAppearanceMu)
	p05HPRequireFree(t, "mode3 wire", &f.target.wireMu)
	t.Log("full mode3 sender completed while both dungeon locks remained held")
}
