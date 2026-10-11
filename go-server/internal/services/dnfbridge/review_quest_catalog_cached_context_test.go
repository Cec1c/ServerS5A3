package dnfbridge

import (
	"context"
	"errors"
	"testing"
)

// Models the exact sticky cache state produced by a caller context ending during
// actual quest Build/Load. It does not induce external or database failures.
func TestReviewActualQuestCatalogCachedContextRetry(t *testing.T) {
	for _, cached := range []struct {
		name string
		err  error
	}{{"canceled", context.Canceled}, {"deadline", context.DeadlineExceeded}} {
		t.Run(cached.name, func(t *testing.T) {
			s, _, _, _ := prepareMonthlyBridge(t)
			s.questCatalogMu.Lock()
			s.questCatalog = nil
			s.questCatalogLoadErr = cached.err
			s.questCatalogMu.Unlock()
			catalog, err := s.loadQuestCatalog(context.Background())
			t.Logf("cached=%v retry=%v canceled=%t deadline=%t catalogPresent=%t", cached.err, err, errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), catalog != nil)
			if err != nil || catalog == nil {
				t.Fatalf("live background caller could not recover transient cached quest context: %v", err)
			}
		})
	}
}
func TestReviewQuestCatalogCachesPermanentResourceFailure(t *testing.T) {
	fault := errors.New("permanent-invalid-quest-catalog")
	s := &Service{questCatalogLoadErr: fault}
	catalog, err := s.loadQuestCatalog(context.Background())
	if !errors.Is(err, fault) || catalog != nil {
		t.Fatalf("permanent catalog failure unexpectedly cleared: catalog=%p err=%v", catalog, err)
	}
}

// The actual standard cancelable context remains live at loadQuestCatalog's
// first Err check; its second Err check, after locking and before Build, closes Done.
// No sleeps, wall-clock guesses, altered PVF, or external injections are used.
type reviewQuestSecondCheckCancel struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *reviewQuestSecondCheckCancel) Err() error {
	c.checks++
	if c.checks == 2 {
		c.cancel()
	}
	return c.Context.Err()
}
func TestReviewActualQuestCatalogPreBuildCancelRecovery(t *testing.T) {
	s, _, _, _ := prepareMonthlyBridge(t)
	s.questCatalogMu.Lock()
	warm := s.questCatalog != nil
	s.questCatalogMu.Unlock()
	if warm {
		t.Fatal("diagnostic actual quest catalog unexpectedly preloaded")
	}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &reviewQuestSecondCheckCancel{Context: parent, cancel: cancel}
	first, err := s.loadQuestCatalog(ctx)
	t.Logf("first checks=%d catalogPresent=%t err=%v", ctx.checks, first != nil, err)
	if !errors.Is(err, context.Canceled) || first != nil || ctx.checks < 2 {
		t.Fatalf("did not cancel after locking before Build: checks=%d catalog=%p err=%v", ctx.checks, first, err)
	}
	s.questCatalogMu.Lock()
	cached := s.questCatalogLoadErr
	s.questCatalogMu.Unlock()
	t.Logf("after pre-Build cancellation cache=%v", cached)
	catalog, retryErr := s.loadQuestCatalog(context.Background())
	t.Logf("background retry catalogPresent=%t err=%v", catalog != nil, retryErr)
	if retryErr != nil || catalog == nil {
		t.Fatalf("pre-Build cancellation poisoned later live caller: %v", retryErr)
	}
}
