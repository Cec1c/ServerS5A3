package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestReviewQuestCatalogWrappedTransientCacheRecovery(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{{"wrapped-cancel", context.Canceled}, {"wrapped-deadline", context.DeadlineExceeded}} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _ := prepareMonthlyBridge(t)
			seeded := fmt.Errorf("actual-loader-context: %w", tc.err)
			s.questCatalogMu.Lock()
			s.questCatalog = nil
			s.questCatalogLoadErr = seeded
			s.questCatalogMu.Unlock()
			catalog, err := s.loadQuestCatalog(context.Background())
			t.Logf("seed=%v retry=%v catalogPresent=%t", seeded, err, catalog != nil)
			if err != nil || catalog == nil {
				t.Fatalf("wrapped transient cache did not recover: %v", err)
			}
			s.questCatalogMu.Lock()
			cached := s.questCatalogLoadErr
			s.questCatalogMu.Unlock()
			if cached != nil {
				t.Fatalf("successful load retained transient cache: %v", cached)
			}
		})
	}
}

// Capture the live Err result before signalling. The root can then cancel while
// holding questCatalogMu; the caller is guaranteed to have passed entry Err.
type reviewQuestWaiterContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *reviewQuestWaiterContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.entered) })
	return err
}
func TestReviewQuestCatalogCancelledLockWaiterDoesNotReadOrMutateCache(t *testing.T) {
	for _, kind := range []string{"warm-catalog", "old-transient-cache"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _, _ := prepareMonthlyBridge(t)
			warm, err := s.loadQuestCatalog(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			seeded := fmt.Errorf("old caller deadline: %w", context.DeadlineExceeded)
			s.questCatalogMu.Lock()
			if kind == "old-transient-cache" {
				s.questCatalog = nil
				s.questCatalogLoadErr = seeded
			}
			priorCatalog, priorErr := s.questCatalog, s.questCatalogLoadErr
			locked := true
			defer func() {
				if locked {
					s.questCatalogMu.Unlock()
				}
			}()
			child, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &reviewQuestWaiterContext{Context: child, entered: make(chan struct{})}
			type result struct {
				err     error
				present bool
			}
			done := make(chan result, 1)
			go func() { cat, e := s.loadQuestCatalog(ctx); done <- result{e, cat != nil} }()
			select {
			case <-ctx.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("caller never passed entry context check")
			}
			cancel()
			s.questCatalogMu.Unlock()
			locked = false
			var got result
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("cancelled lock waiter did not return")
			}
			t.Logf("kind=%s waiterError=%v catalogPresent=%t", kind, got.err, got.present)
			if !errors.Is(got.err, context.Canceled) || got.present {
				t.Fatalf("cancelled waiter obtained cache result: err=%v present=%t", got.err, got.present)
			}
			s.questCatalogMu.Lock()
			sameCatalog, sameErr := s.questCatalog == priorCatalog, s.questCatalogLoadErr == priorErr
			s.questCatalogMu.Unlock()
			if !sameCatalog || !sameErr {
				t.Fatal("cancelled waiter changed pre-existing cache")
			}
			// A separate live call still has the normal success/cache-recovery contract.
			cat, e := s.loadQuestCatalog(context.Background())
			if e != nil || cat == nil {
				t.Fatalf("live caller after cancelled waiter failed: %v", e)
			}
			if kind == "warm-catalog" && cat != warm {
				t.Fatal("healthy cached catalog was unnecessarily rebuilt")
			}
		})
	}
}

// Three Err checks supports both current loader (1 entry + >=2 Build checks)
// and proposed loader (1 entry + 1 after-lock + 1 Build check). Record the exact
// cancellation caller so this proof cannot silently turn into pre-Build cancel.
type reviewQuestThirdCheckCancel struct {
	context.Context
	cancel       context.CancelFunc
	checks       int
	cancelCaller string
}

func (c *reviewQuestThirdCheckCancel) Err() error {
	c.checks++
	if c.checks == 3 {
		if pc, _, _, ok := runtime.Caller(1); ok {
			if f := runtime.FuncForPC(pc); f != nil {
				c.cancelCaller = f.Name()
			}
		}
		c.cancel()
	}
	return c.Context.Err()
}
func TestReviewActualQuestMidBuildCancelWithPostLockGuardRecovery(t *testing.T) {
	s, _, _, _ := prepareMonthlyBridge(t)
	s.questCatalogMu.Lock()
	warm := s.questCatalog != nil
	s.questCatalogMu.Unlock()
	if warm {
		t.Fatal("unexpected warm actual quest fixture")
	}
	child, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &reviewQuestThirdCheckCancel{Context: child, cancel: cancel}
	first, err := s.loadQuestCatalog(ctx)
	t.Logf("checks=%d cancelCaller=%s first=%v catalogPresent=%t", ctx.checks, ctx.cancelCaller, err, first != nil)
	if !errors.Is(err, context.Canceled) || first != nil || ctx.cancelCaller != "longheng.io/server/internal/modules/dnf/pvf.Build" {
		t.Fatalf("cancellation was not inside actual Build: checks=%d caller=%s err=%v", ctx.checks, ctx.cancelCaller, err)
	}
	cat, retry := s.loadQuestCatalog(context.Background())
	if retry != nil || cat == nil {
		t.Fatalf("actual Build cancellation poisoned later live caller: %v", retry)
	}
}
