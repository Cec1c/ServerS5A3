package dnfbridge

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestPartyLoadingReadsEachCharacterOnce(t *testing.T) {
	s, a, b, ac, bc, _ := partyDungeonSyncFixture(t)
	runPartyDungeonSyncEntry(t, s, a)
	repos, _ := s.repositoryGroup()
	inner := repos.Character
	var counts [2]atomic.Int32
	repos.Character = crossSceneProfileCharacterReader{CharacterRepository: inner, load: func(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
		if id == "99" {
			counts[0].Add(1)
		}
		if id == "100" {
			counts[1].Add(1)
		}
		return inner.Load(ctx, id)
	}}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	partyID, members, _ := s.partyDungeonLoadingBarrierMembers(a)
	s.partyDungeonLoadingBarrierArrive(a, partyID, members)
	_, _, _, _, cycle := s.partyDungeonLoadingBarrierArrive(b, partyID, members)
	s.releasePartyDungeonLoadingCycle(partyID, cycle, members, members, "cache_regression")
	if !partyDungeonSyncConnHasPlacement(t, ac) || !partyDungeonSyncConnHasPlacement(t, bc) {
		t.Fatal("loading did not activate")
	}
	if counts[0].Load() != 1 || counts[1].Load() != 1 {
		t.Fatalf("character loads=%d/%d, want one each per loading cycle", counts[0].Load(), counts[1].Load())
	}
}

func TestPartyLoadingSnapshotCoalescesAndInvalidates(t *testing.T) {
	s, _, _, _, _, _ := partyDungeonSyncFixture(t)
	repos, _ := s.repositoryGroup()
	inner := repos.Character
	var calls atomic.Int32
	repos.Character = crossSceneProfileCharacterReader{CharacterRepository: inner, load: func(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
		calls.Add(1)
		return inner.Load(ctx, id)
	}}
	cache := newPartyLoadingSnapshots()
	wrapped := cache.wrap(repos)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, ok, err := wrapped.Character.Load(context.Background(), "99")
			if err != nil || !ok {
				t.Errorf("load: %t %v", ok, err)
				return
			}
			r.Stats["fatigue"] = 1
		}()
	}
	wg.Wait()
	r, _, _ := wrapped.Character.Load(context.Background(), "99")
	if calls.Load() != 1 || r.Stats["fatigue"] != 100 {
		t.Fatalf("not coalesced/detached: calls=%d fatigue=%d", calls.Load(), r.Stats["fatigue"])
	}
	r.Name = "saved"
	if err := wrapped.Character.Save(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	r, _, _ = wrapped.Character.Load(context.Background(), "99")
	if calls.Load() != 2 || r.Name != "saved" {
		t.Fatal("save did not invalidate")
	}
	_, _, _ = newPartyLoadingSnapshots().wrap(repos).Character.Load(context.Background(), "99")
	if calls.Load() != 3 {
		t.Fatal("next cycle reused old cache")
	}
}
