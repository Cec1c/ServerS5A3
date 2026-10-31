package dnfbridge

import (
	"fmt"
	"longheng.io/server/internal/platform/metrics"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSlowStageSamplingHasFixedConcurrentBudget(t *testing.T) {
	m := newBridgePerformanceMetrics(metrics.New())
	now := m.slowEpoch
	if m.admitSlowSample(perfWireDirect, 299*time.Millisecond, now) {
		t.Fatal("sampled a fast operation")
	}
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 128; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if m.admitSlowSample(perfWireDirect, 300*time.Millisecond, now) {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 1 {
		t.Fatalf("concurrent slow samples=%d", admitted.Load())
	}
	if m.admitSlowSample(perfWireDirect, time.Second, now.Add(9*time.Second)) {
		t.Fatal("exceeded sample budget")
	}
	if !m.admitSlowSample(perfWireDirect, time.Second, now.Add(10*time.Second)) {
		t.Fatal("sample budget did not recover")
	}
	if !m.admitSlowSample(perfMonsterTasks, time.Second, now) {
		t.Fatal("one stage starved another")
	}
}

func TestAttendancePhasesStayDistinctAndWithinOriginalPersistenceInterval(t *testing.T) {
	now := time.Date(2026, 10, 31, 5, 59, 59, 999999999, time.FixedZone("test", 8*3600))
	spread := make(map[time.Duration]bool)
	for account := 0; account < 100; account++ {
		seen := make(map[time.Duration]bool)
		for kind := 0; kind < 4; kind++ {
			d := currentAttendanceTickDelay(now, fmt.Sprint(account), kind)
			if d <= 0 || d > 30*time.Second || seen[d] {
				t.Fatalf("phase=%s duplicate=%t", d, seen[d])
			}
			seen[d] = true
			spread[d] = true
			if next := currentAttendanceTickDelay(now.Add(d), fmt.Sprint(account), kind); next != 30*time.Second {
				t.Fatalf("changed steady period=%s", next)
			}
		}
	}
	if len(spread) < 100 {
		t.Fatalf("accounts still clustered: %d phases", len(spread))
	}
}
