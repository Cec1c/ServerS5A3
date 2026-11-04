package dnfbridge

import (
	"context"
	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	"sync"
	"testing"
	"time"
)

func TestRealPVFOrdinaryAttendanceCatalogReusesValidatedDefinitions(t *testing.T) {
	s, _, _, _ := prepareMonthlyBridge(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.currentOrdinaryCatalog(ctx); err != context.Canceled {
		t.Fatalf("canceled cold caller: %v", err)
	}
	first, err := s.currentOrdinaryCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	allocations := testing.AllocsPerRun(3, func() {
		if _, err := s.currentOrdinaryCatalog(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("four warm catalog calls elapsed=%s allocations/call=%.0f", time.Since(start), allocations)
	if allocations > 10 {
		t.Fatalf("periodic attendance reparses immutable PVF: %.0f allocations/call", allocations)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := s.currentOrdinaryCatalog(context.Background())
			if err != nil || got != first {
				t.Errorf("validated catalog not shared: %v", err)
			}
		}()
	}
	wg.Wait()
	// Concurrent first users must also share one validated result.
	cold := &Service{initialEquipmentArchive: s.initialEquipmentArchive}
	results := make([]*onlineevent.OrdinaryCatalog, 16)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			results[i], err = cold.currentOrdinaryCatalog(context.Background())
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	for _, got := range results {
		if got == nil || got != results[0] {
			t.Fatal("concurrent cold loads did not share validated catalog")
		}
	}
	if _, err := s.currentOrdinaryCatalog(ctx); err != context.Canceled {
		t.Fatalf("canceled caller: %v", err)
	}
}
