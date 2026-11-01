package party

import (
	"reflect"
	"sync"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

func TestRuntimePartyJoinConfirmationPreservesFullParty(t *testing.T) {
	m := NewRuntimePartyManager()
	created := m.Create(testRuntimePartyMember(101, 1), alignedcmd.PartyState{MaxMembers: 4})
	for _, id := range []uint16{202, 303, 404} {
		if result := m.Join(created.Party.ID, testRuntimePartyMember(id, 1)); !result.OK {
			t.Fatal(result.Reason)
		}
	}
	before, _ := m.SnapshotByID(created.Party.ID)
	for _, member := range before.Members {
		result := m.Join(before.ID, member)
		if !result.OK || !reflect.DeepEqual(result.Party, before) || result.PriorLeave != nil {
			t.Fatalf("existing member confirmation = %+v, want unchanged success", result)
		}
	}
	if stale := m.Join(before.ID, testRuntimePartyMember(202, 2)); stale.OK || stale.Reason != "stale_member_session" {
		t.Fatalf("stale confirmation = %+v", stale)
	}
	if fifth := m.Join(before.ID, testRuntimePartyMember(505, 1)); fifth.OK || fifth.Reason != "party_full" {
		t.Fatalf("fifth member = %+v", fifth)
	}
	after, _ := m.SnapshotByID(before.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("confirmation changed leader, slots or members: before=%+v after=%+v", before, after)
	}
}

func TestRuntimePartyConcurrentJoinConfirmation(t *testing.T) {
	m := NewRuntimePartyManager()
	created := m.Create(testRuntimePartyMember(101, 1), alignedcmd.PartyState{MaxMembers: 4})
	var wg sync.WaitGroup
	results := make(chan RuntimePartyResult, 16)
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- m.Join(created.Party.ID, testRuntimePartyMember(202, 1))
		}()
	}
	wg.Wait()
	close(results)
	for result := range results {
		if !result.OK || len(result.Party.Members) != 2 {
			t.Fatalf("concurrent confirmation = %+v", result)
		}
	}
	final, _ := m.SnapshotByID(created.Party.ID)
	if final.Leader != 101 || len(final.Members) != 2 {
		t.Fatalf("concurrent joins changed party: %+v", final)
	}
}
