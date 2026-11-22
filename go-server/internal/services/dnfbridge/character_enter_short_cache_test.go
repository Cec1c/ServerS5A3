package dnfbridge

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestCharacterEnterShortCacheSingleflightAndTTL(t *testing.T) {
	s := &Service{}
	key := characterEnterShortCacheKey{charID: 1002, generation: 9}
	var calls atomic.Int32
	gate := make(chan struct{})
	now := time.Now()

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, name, rec, ok := s.characterEnterCache.getOrLoad(key, now, func() (uint16, string, dnfrepo.CharacterRecord, bool) {
				calls.Add(1)
				<-gate
				return 1002, "Mage", dnfrepo.CharacterRecord{CharacterID: "1002", Name: "Mage", Stats: map[string]int64{"hp": 100}}, true
			})
			if id != 1002 || name != "Mage" || !ok || rec.Stats["hp"] != 100 {
				t.Errorf("unexpected result: id=%d name=%q ok=%v stats=%+v", id, name, ok, rec.Stats)
			}
			rec.Stats["hp"] = 0
		}()
	}
	close(gate)
	wg.Wait()

	if calls.Load() != 1 {
		t.Fatalf("expected 1 DB call, got %d", calls.Load())
	}

	// TTL 内复用且不被上游修改污染
	_, _, cached, ok := s.characterEnterCache.getOrLoad(key, now.Add(200*time.Millisecond), func() (uint16, string, dnfrepo.CharacterRecord, bool) {
		t.Fatalf("expected cache hit within TTL")
		return 0, "", dnfrepo.CharacterRecord{}, false
	})
	if !ok || cached.Stats["hp"] != 100 {
		t.Fatalf("unexpected cached snapshot: ok=%v stats=%+v", ok, cached.Stats)
	}

	// 显式失效后重新加载
	s.invalidateCharacterEnterShortCache(1002)
	_, _, _, ok = s.characterEnterCache.getOrLoad(key, now.Add(300*time.Millisecond), func() (uint16, string, dnfrepo.CharacterRecord, bool) {
		calls.Add(1)
		return 1002, "Mage", dnfrepo.CharacterRecord{CharacterID: "1002", Name: "Mage"}, true
	})
	if !ok || calls.Load() != 2 {
		t.Fatalf("expected reload after invalidation, ok=%v calls=%d", ok, calls.Load())
	}
}
