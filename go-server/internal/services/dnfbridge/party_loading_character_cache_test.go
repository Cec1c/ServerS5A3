package dnfbridge

import (
	"sync"
	"sync/atomic"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestPartyLoadingCharacterSnapshotCacheDeduplicatesConcurrentLoads(t *testing.T) {
	cache := newPartyLoadingCharacterSnapshotCache(4)
	var calls atomic.Int32
	gate := make(chan struct{})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, name, rec, ok := cache.getOrLoad(1001, func() (uint16, string, dnfrepo.CharacterRecord, bool) {
				calls.Add(1)
				<-gate
				return 1001, "Hero", dnfrepo.CharacterRecord{
					CharacterID: "1001",
					Name:        "Hero",
					Stats:       map[string]int64{"level": 90},
				}, true
			})
			if id != 1001 || name != "Hero" || !ok || rec.Stats["level"] != 90 {
				t.Errorf("unexpected snapshot: id=%d name=%q ok=%v stats=%+v", id, name, ok, rec.Stats)
			}
			// Mutating caller's copy must not pollute cached snapshot.
			rec.Stats["level"] = 1
		}()
	}
	close(gate)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 underlying DB load, got %d", got)
	}
	_, _, fresh, ok := cache.getOrLoad(1001, func() (uint16, string, dnfrepo.CharacterRecord, bool) {
		t.Fatalf("unexpected second load on cached hit")
		return 0, "", dnfrepo.CharacterRecord{}, false
	})
	if !ok || fresh.Stats["level"] != 90 {
		t.Fatalf("cached snapshot mutated or missing: ok=%v stats=%+v", ok, fresh.Stats)
	}
}
