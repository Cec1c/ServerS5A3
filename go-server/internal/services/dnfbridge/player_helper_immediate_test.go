package dnfbridge

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/platform/eventloop"
	"sync"
	"testing"
	"time"
)

func TestPlayerHelperImmediateBacklogDrainsWithoutPeriodicWait(t *testing.T) {
	t.Setenv("DNFBRIDGE_HELPER_AUTOPICKUP", "on")
	s, p, r, g, _ := helperFixture(t)
	key := seedCurrentDungeonPickupDrop(t, s, r, 3227, currentSceneActorObjectKey(99))
	base := *r.DropOwner.byObjectKey[key]
	const count = 130
	for i := uint32(1); i < count; i++ {
		d := base
		d.ObjectKey = key + i
		d.SceneSlot = uint16(i + 1)
		r.DropOwner.byObjectKey[d.ObjectKey] = &d
	}
	helperApply(t, s, p, helperLootPolicy{StackableMask: 127})
	q := s.gameplayTimers.(*fakeCurrentDungeonDeathTimerQueue)
	if r.DropOwner.byObjectKey[key].Status != runtimeDungeonDropAvailable {
		t.Fatal("activation picked up inline")
	}
	started := time.Now()
	for i := 0; i < 3; i++ {
		task := q.task(t, i)
		if task.delay != 0 {
			t.Fatal("artificial batch wait", task.delay)
		}
		q.fire(task, false)
	}
	for _, d := range r.DropOwner.byObjectKey {
		if d.Status != runtimeDungeonDropConsumed {
			t.Fatal("backlog left behind", d.ObjectKey)
		}
	}
	inv, _, err := g.Inventory.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, item := range inv.Slots {
		total += item.Count
	}
	if total != count {
		t.Fatal("transaction quantity", total)
	}
	scheduled, _, active := q.counts()
	if scheduled != 3 || active != 0 {
		t.Fatal("idle polling or extra turns", scheduled, active)
	}
	t.Logf("130 original pickup transactions in %s (in-memory repositories; not a production latency measurement)", time.Since(started))
}

func TestPlayerHelperFullOwnerInboxDoesNotBlockSharedClock(t *testing.T) {
	t.Setenv("DNFBRIDGE_HELPER_AUTOPICKUP", "on")
	s, p, r, _, _ := helperFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.ctx = ctx
	if err := s.startGameSessionEvents(ctx, p); err != nil {
		t.Fatal(err)
	}
	blocked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer func() { unblock(); stopTestGameSessionEvents(t, s, p) }()
	id, _ := s.boundGameSessionCharacterSnapshot(p)
	if err := s.tryPostGameSessionCharacterEvent(p, "helper-backpressure-fixture", id.character, id.generation, func() error { close(blocked); <-release; return nil }); err != nil {
		t.Fatal(err)
	}
	waitTestSignal(t, blocked, "fixture owner did not block")
	full := false
	for i := 0; i < 10000; i++ {
		err := s.tryPostGameSessionCharacterEvent(p, "helper-fill-fixture", id.character, id.generation, func() error { return nil })
		if errors.Is(err, eventloop.ErrQueueFull) {
			full = true
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !full {
		t.Fatal("fixture inbox did not fill")
	}
	key := seedCurrentDungeonPickupDrop(t, s, r, 3227, currentSceneActorObjectKey(99))
	helperApply(t, s, p, helperLootPolicy{StackableMask: 127})
	q := s.gameplayTimers.(*fakeCurrentDungeonDeathTimerQueue)
	done := make(chan struct{})
	go func() { q.fire(q.task(t, 0), false); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		unblock()
		t.Fatal("one full owner stalled the shared gameplay clock")
	}
	if retry := q.task(t, 1); retry.delay != 5*time.Millisecond {
		t.Fatal("missing bounded backpressure yield", retry.delay)
	}
	unblock()
	if err := s.callGameSession(ctx, p, "helper-fixture-drained", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	q.fire(q.task(t, 1), false)
	if err := s.callGameSession(ctx, p, "helper-fixture-picked", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if r.DropOwner.byObjectKey[key].Status != runtimeDungeonDropConsumed {
		t.Fatal("backpressure lost the pickup wake")
	}
}

func TestPlayerHelperDropWireWakesOnlyAfterSuccessfulAnnouncement(t *testing.T) {
	t.Setenv("DNFBRIDGE_HELPER_AUTOPICKUP", "on")
	s, p, r, _, _ := helperFixture(t)
	helperApply(t, s, p, helperLootPolicy{StackableMask: 127})
	q := s.gameplayTimers.(*fakeCurrentDungeonDeathTimerQueue)
	q.fire(q.task(t, 0), false)
	key := seedCurrentDungeonPickupDrop(t, s, r, 3227, currentSceneActorObjectKey(99))
	item, err := currentDungeonDeathDropItemState(1, r.DropOwner.byObjectKey[key].Item, 1)
	if err != nil {
		t.Fatal(err)
	}
	body, err := buildCurrentDungeonDeathNotificationBodyWithDrops(500, currentDungeonDeathResponseMonster, []currentDungeonDeathDropWire{{SceneObjectKey: key, Item: item, OwnerActorObjectKey: currentSceneActorObjectKey(99)}})
	if err != nil {
		t.Fatal(err)
	}
	p.conn = &pvpWriteFailureConn{remainingFailures: 1}
	if err := s.sendGameUpperRawClass(p, uint16(dnfenum.CmdPacketNotifyDieMonster), body, 0); err == nil {
		t.Fatal("expected failed write")
	}
	if scheduled, _, _ := q.counts(); scheduled != 1 {
		t.Fatal("unannounced drop activated helper")
	}
	p.conn = &bufferConn{}
	// The hook must only wake: the monster handler still owns dungeon.mu.
	p.dungeon.mu.Lock()
	for i := 0; i < 25; i++ {
		if err := s.sendGameUpperRawClass(p, uint16(dnfenum.CmdPacketNotifyDieMonster), body, 0); err != nil {
			t.Fatal(err)
		}
	}
	p.dungeon.mu.Unlock()
	if scheduled, _, active := q.counts(); scheduled != 2 || active != 1 {
		t.Fatal("drop burst was not coalesced", scheduled, active)
	}
	if r.DropOwner.byObjectKey[key].Status != runtimeDungeonDropAvailable {
		t.Fatal("pickup before owner turn")
	}
	q.fire(q.task(t, 1), false)
	if r.DropOwner.byObjectKey[key].Status != runtimeDungeonDropConsumed {
		t.Fatal("announced drop not picked up")
	}
}

func TestPlayerHelperQueuedWakeCannotSurviveReleaseOrCancelNewWake(t *testing.T) {
	t.Setenv("DNFBRIDGE_HELPER_AUTOPICKUP", "on")
	s, p, r, _, _ := helperFixture(t)
	key := seedCurrentDungeonPickupDrop(t, s, r, 3227, currentSceneActorObjectKey(99))
	a := helperApply(t, s, p, helperLootPolicy{StackableMask: 127})
	q := s.gameplayTimers.(*fakeCurrentDungeonDeathTimerQueue)
	old := q.task(t, 0)
	id, _ := s.boundGameSessionCharacterSnapshot(p)
	if _, err := s.applyHelperLoot(p, id, helperLootRequest{Action: "release", Binding: a.Binding, Owner: p.helper.owner, Lease: a.Lease}); err != nil {
		t.Fatal(err)
	}
	helperApply(t, s, p, helperLootPolicy{StackableMask: 127})
	q.fire(old, true)
	if !p.helper.pending || r.DropOwner.byObjectKey[key].Status != runtimeDungeonDropAvailable {
		t.Fatal("old callback changed new activation")
	}
	q.fire(q.task(t, 1), false)
	if r.DropOwner.byObjectKey[key].Status != runtimeDungeonDropConsumed {
		t.Fatal("new callback lost")
	}
}
