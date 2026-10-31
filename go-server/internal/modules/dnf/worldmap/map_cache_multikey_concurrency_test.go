package worldmap

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// A capacity-one cache must not cross-populate distinct concurrent cold keys.
// Existing same-key flight test is intentionally not repeated.
func TestMapCacheConcurrentDistinctKeysUnderEvictionPreservesValues(t *testing.T) {
	const (
		mapCount    = 24
		workers     = 24
		iterations  = 40
		monsterBase = 700000
	)

	var list strings.Builder
	source := textSource{}
	mapPath := func(id int64) string {
		return fmt.Sprintf("map/concurrent-%02d.map", id)
	}
	for id := int64(1); id <= mapCount; id++ {
		list.WriteString(fmt.Sprintf("%d %s%s%s\n", id, pvfBacktick, mapPath(id), pvfBacktick))
		source[mapPath(id)] = fmt.Sprintf(
			"[map name]\n%sM%d%s\n[monster]\n%d 1 0 %d %d 0 0 0 %s[fixed]%s %s[normal]%s\n",
			pvfBacktick, id, pvfBacktick,
			monsterBase+id, id, id,
			pvfBacktick, pvfBacktick, pvfBacktick, pvfBacktick,
		)
	}
	source[DefaultMapList] = list.String()

	table, err := LoadSource(context.Background(), source, Options{SkipAreas: true, SkipDungeons: true})
	if err != nil {
		t.Fatal(err)
	}
	options := DefaultMapCacheOptions()
	options.Capacity = 1
	options.PinnedBytes = 0
	if err := table.EnableMapCache(source, options); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	failures := make(chan error, workers)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			<-start
			for iteration := 0; iteration < iterations; iteration++ {
				id := int64((worker+iteration)%mapCount) + 1
				value, found, err := table.FindMapWithError(id)
				if err != nil || !found {
					failures <- fmt.Errorf("worker=%d iteration=%d id=%d found=%t err=%v", worker, iteration, id, found, err)
					return
				}
				if value.ID != id || value.Path != mapPath(id) || value.Name != fmt.Sprintf("M%d", id) ||
					len(value.Monsters) != 1 || value.Monsters[0].MonsterID != monsterBase+id {
					failures <- fmt.Errorf("worker=%d iteration=%d id=%d got id=%d path=%q monsters=%+v", worker, iteration, id, value.ID, value.Path, value.Monsters)
					return
				}
				value.Path = "mutated"
				value.Name = "mutated"
				value.Monsters[0].MonsterID = -1

				again, found, err := table.FindMapWithError(id)
				if err != nil || !found || again.ID != id || again.Path != mapPath(id) || again.Name != fmt.Sprintf("M%d", id) ||
					len(again.Monsters) != 1 || again.Monsters[0].MonsterID != monsterBase+id {
					failures <- fmt.Errorf("worker=%d iteration=%d id=%d copy pollution: found=%t err=%v value=%+v", worker, iteration, id, found, err, again)
					return
				}
			}
		}(worker)
	}
	close(start)
	wait.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}

	snapshot := table.MapCacheSnapshot()
	if !snapshot.Enabled || snapshot.Entries > 1 || snapshot.Evictions == 0 {
		t.Fatalf("capacity-one cache did not evict under distinct-key concurrency: %+v", snapshot)
	}
}
