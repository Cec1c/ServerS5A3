package pvf

import (
	"container/list"
	"sync"
	"testing"
)

// A one-entry byte budget must preserve key/value pairing under concurrent get, put, and clear.
// The existing concurrent test repeatedly reads one key; this one forces
// different-key insertion and eviction while clear runs concurrently.
func TestBoundedReadCacheConcurrentDistinctKeysAndClearPreservesBudget(t *testing.T) {
	const (
		workers    = 16
		iterations = 200
	)
	cache := byteReadCache{
		budget:  80,
		entries: make(map[int]*list.Element),
	}

	start := make(chan struct{})
	failures := make(chan string, workers)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			<-start
			for iteration := 0; iteration < iterations; iteration++ {
				key := (worker*7 + iteration) % 32
				cache.put(key, key, 16)
				value, found := cache.get(key)
				if found && value.(int) != key {
					failures <- "bounded cache returned a value for the wrong key"
					return
				}
				_, bytes := cache.stats()
				if bytes > cache.budget {
					failures <- "bounded cache exceeded its accounted byte budget"
					return
				}
			}
		}(worker)
	}
	wait.Add(1)
	go func() {
		defer wait.Done()
		<-start
		for iteration := 0; iteration < 100; iteration++ {
			cache.clear()
		}
	}()
	close(start)
	wait.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}

	cache.clear()
	if count, bytes := cache.stats(); count != 0 || bytes != 0 || cache.order.Len() != 0 {
		t.Fatalf("final clear left entries=%d bytes=%d order=%d", count, bytes, cache.order.Len())
	}
}
