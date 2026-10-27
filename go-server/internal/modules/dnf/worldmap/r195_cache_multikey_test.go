package worldmap

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

// Each root's first read is held separately, so eviction is tested while another
// cold parse is still in flight. Imports remain real reads of the shared fixture.
type r195CacheGatedMapSource struct {
	source  textSource
	ctx     context.Context
	started chan string
	release map[string]chan struct{}
	mu      sync.Mutex
	reads   map[string]int
}

func (s *r195CacheGatedMapSource) ReadText(p string) (string, error) {
	key := pathKey(p)
	s.mu.Lock()
	s.reads[key]++
	first := s.reads[key] == 1
	s.mu.Unlock()
	if gate := s.release[key]; gate != nil && first {
		s.started <- key
		select {
		case <-gate:
		case <-s.ctx.Done():
			return "", s.ctx.Err()
		}
	}
	return s.source.ReadText(p)
}

func TestR195CacheMultiKeyColdReadsEvictWhileOtherLoadsRemainInFlight(t *testing.T) {
	for _, capacity := range []int{1, 2} {
		t.Run(fmt.Sprintf("capacity_%d", capacity), func(t *testing.T) {
			source := textSource{
				DefaultMapList:   "1 `a.map`\n2 `b.map`\n3 `c.map`\n4 `d.map`\n",
				"map/common.map": "[npc]\n22 `[right]` 187 200 0\n",
			}
			paths := []string{"map/a.map", "map/b.map", "map/c.map", "map/d.map"}
			for i, p := range paths {
				source[p] = fmt.Sprintf("[map name]\n`map-%d`\n[import script]\n`common.map`\n[monster]\n%d 1 0 10 20 0 0 0 `[fixed]` `[normal]`\n", i+1, i+1)
			}
			table, err := LoadSource(context.Background(), source, Options{SkipAreas: true, SkipDungeons: true})
			if err != nil {
				t.Fatal(err)
			}
			// The oracle is the ordinary eager loader, before enabling the cache.
			want := table.Maps()
			if len(want) != len(paths) {
				t.Fatalf("eager map count=%d want=%d", len(want), len(paths))
			}
			for _, m := range want {
				if len(m.NPCs) != 1 || len(m.Monsters) != 1 || len(m.SourceSections) == 0 {
					t.Fatalf("incomplete eager fixture: %+v", m)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			gated := &r195CacheGatedMapSource{source: source, ctx: ctx, started: make(chan string, len(paths)), release: make(map[string]chan struct{}), reads: make(map[string]int)}
			for _, p := range paths {
				gated.release[pathKey(p)] = make(chan struct{})
			}
			options := DefaultMapCacheOptions()
			options.Capacity, options.PinnedBytes = capacity, 0
			if err := table.EnableMapCache(gated, options); err != nil {
				t.Fatal(err)
			}
			type result struct {
				value Map
				err   error
			}
			results := make([]chan result, len(paths))
			for i := range paths {
				results[i] = make(chan result, 1)
				go func(position int) {
					value, err := table.readMap(position)
					results[position] <- result{value, err}
				}(i)
			}
			started := make(map[string]bool)
			for range paths {
				select {
				case p := <-gated.started:
					if started[p] {
						t.Fatalf("duplicate cold-load start: %s", p)
					}
					started[p] = true
				case <-ctx.Done():
					t.Fatalf("not all distinct cold loads started: %v", started)
				}
			}
			checkBudget := func() MapCacheSnapshot {
				t.Helper()
				snap := table.MapCacheSnapshot()
				if !snap.Enabled || snap.Capacity != capacity || snap.Entries < 0 || snap.Entries > capacity || snap.EstimatedBytes < 0 || snap.EstimatedBytes > options.MaxBytes || snap.Pinned != 0 || snap.PinnedEstimatedBytes != 0 || snap.Errors != 0 {
					t.Fatalf("cache budget/invariants: %+v", snap)
				}
				return snap
			}
			receive := func(i int) Map {
				t.Helper()
				select {
				case got := <-results[i]:
					if got.err != nil || !reflect.DeepEqual(got.value, want[i]) {
						t.Fatalf("map %d differs from eager load: err=%v got=%+v want=%+v", i+1, got.err, got.value, want[i])
					}
					checkBudget()
					return got.value
				case <-ctx.Done():
					t.Fatalf("map %d did not finish", i+1)
					return Map{}
				}
			}
			for i := 0; i < 3; i++ {
				close(gated.release[pathKey(paths[i])])
				receive(i)
			}
			if snap := checkBudget(); snap.Loads != 4 || snap.Entries != capacity || snap.Evictions != uint64(3-capacity) {
				t.Fatalf("eviction not reached with fourth root held: %+v", snap)
			}
			// Root 1 was evicted for both capacities; reread it before releasing 4.
			value, err := table.readMap(0)
			if err != nil || !reflect.DeepEqual(value, want[0]) {
				t.Fatalf("evicted root reread differs: %v", err)
			}
			gated.mu.Lock()
			firstReads := gated.reads[pathKey(paths[0])]
			gated.mu.Unlock()
			if firstReads != 2 {
				t.Fatalf("evicted root source reads=%d want=2", firstReads)
			}
			close(gated.release[pathKey(paths[3])])
			receive(3)
			value.Monsters[0].MonsterID = 999
			value.NPCs[0].NPCID = 999
			value.SourceSections[0].Name = "caller mutation"
			restored, err := table.readMap(0)
			if err != nil || !reflect.DeepEqual(restored, want[0]) {
				t.Fatalf("returned value mutation leaked into cache: %v", err)
			}
			checkBudget()
		})
	}
}
