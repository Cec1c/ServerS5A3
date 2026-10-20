package auction

import (
	"context"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	"sync"
	"testing"
)

func TestListingSequenceSurvivesOwnerRestartAndConcurrentAllocation(t *testing.T) {
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	first, err := NewOwner(r)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := first.AllocateListingID(ctx)
	if err != nil || initial == 0 {
		t.Fatal(initial, err)
	}
	const count = 48
	ids := make(chan uint64, count)
	failures := make(chan error, count)
	var wg sync.WaitGroup
	for n := 0; n < count; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, e := NewOwner(r)
			if e != nil {
				failures <- e
				return
			}
			id, e := o.AllocateListingID(ctx)
			if e != nil {
				failures <- e
				return
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)
	close(failures)
	for e := range failures {
		t.Error(e)
	}
	seen := map[uint64]bool{initial: true}
	for id := range ids {
		if seen[id] || id <= initial || id > initial+count {
			t.Errorf("编号重复或越界: %d", id)
		}
		seen[id] = true
	}
	if len(seen) != count+1 {
		t.Fatalf("编号数=%d", len(seen))
	}
	restarted, err := NewOwner(r)
	if err != nil {
		t.Fatal(err)
	}
	next, err := restarted.AllocateListingID(ctx)
	if err != nil || next != initial+count+1 {
		t.Fatal(next, err)
	}
}
