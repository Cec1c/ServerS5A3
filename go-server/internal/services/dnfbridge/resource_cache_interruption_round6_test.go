package dnfbridge

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// round6InterruptAtObservation injects interruption at an exact synchronous
// loader observation. It does not replace a PVF read, sleep, or test scheduler
// timing or all possible cancellation points.
type round6InterruptAtObservation struct {
	context.Context
	remaining int
	err       error
}

func (c *round6InterruptAtObservation) Err() error {
	if c.remaining > 0 {
		c.remaining--
		return nil
	}
	return c.err
}

func TestRealPVFSpendTimeCatalogRetriesInterruptedLoadRound6(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		interrupt error
		remaining int
	}{
		// Outer and module entry checks pass; fail after attendance read.
		{"canceled_after_event_read", context.Canceled, 2},
		{"deadline_after_event_read", context.DeadlineExceeded, 2},
		// Also pass reward resolver entry; fail after stackable.lst read.
		{"canceled_after_reward_list", context.Canceled, 4},
		{"deadline_after_reward_list", context.DeadlineExceeded, 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &Service{initialEquipmentArchive: archive}
			ctx := &round6InterruptAtObservation{Context: context.Background(), remaining: test.remaining, err: test.interrupt}
			catalog, available, err := s.currentSpendTimeRuntimeCatalogIfAvailable(ctx)
			if !errors.Is(err, test.interrupt) || catalog != nil || !available {
				t.Fatalf("load interruption: catalog=%v available=%t err=%v", catalog, available, err)
			}
			if s.spendTimeCatalogState != nil || s.spendTimeCatalogLoadErr != nil {
				t.Fatalf("request interruption poisoned service cache: state=%v err=%v", s.spendTimeCatalogState, s.spendTimeCatalogLoadErr)
			}
			recovered, available, err := s.currentSpendTimeRuntimeCatalogIfAvailable(context.Background())
			if err != nil || !available || recovered == nil || len(recovered.rewardItemIDs) == 0 {
				t.Fatalf("fresh request did not recover: catalog=%v available=%t err=%v", recovered, available, err)
			}
			again, available, err := s.currentSpendTimeRuntimeCatalogIfAvailable(context.Background())
			if err != nil || !available || again != recovered {
				t.Fatalf("successful cache not reused: same=%t available=%t err=%v", again == recovered, available, err)
			}
		})
	}
}

func TestSpendTimeCatalogKeepsPermanentFailureAndEntryDeadlineRound6(t *testing.T) {
	// An archive without the required event really fails the resource read;
	// retain that permanent error even if the archive is later removed.
	s := &Service{initialEquipmentArchive: &platformpvf.Archive{}}
	catalog, available, firstErr := s.currentSpendTimeRuntimeCatalogIfAvailable(context.Background())
	if catalog != nil || !available || !errors.Is(firstErr, platformpvf.ErrFileNotFound) || s.spendTimeCatalogLoadErr != firstErr {
		t.Fatalf("permanent failure not retained: catalog=%v available=%t err=%v", catalog, available, firstErr)
	}
	s.initialEquipmentArchive = nil
	if catalog, available, err := s.currentSpendTimeRuntimeCatalogIfAvailable(context.Background()); catalog != nil || !available || err != firstErr {
		t.Fatalf("permanent failure unexpectedly retried: catalog=%v available=%t err=%v", catalog, available, err)
	}
	clean := &Service{initialEquipmentArchive: &platformpvf.Archive{}}
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	if _, _, err := clean.currentSpendTimeRuntimeCatalogIfAvailable(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("entry deadline err=%v", err)
	}
	if clean.spendTimeCatalogLoadErr != nil || clean.spendTimeCatalogState != nil {
		t.Fatal("entry deadline entered the cache")
	}
}
