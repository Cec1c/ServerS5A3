package dnfbridge

import (
	"testing"
	"time"
)

// Wait for actual asynchronous recipient projection, never manufacture progress.
func r160WaitRaidProjection(t *testing.T, f *raidStartGuardFixture, owners []*gameSession) {
	t.Helper()
	f.service.raidMu.Lock()
	version := f.state.Battle.ProgressVersion
	f.service.raidMu.Unlock()
	until := time.Now().Add(createWriteTimeout)
	for time.Now().Before(until) {
		done := true
		for _, owner := range owners {
			owner.raidProjection.mu.Lock()
			caughtUp := owner.raidProjection.version >= version && !owner.raidProjection.failed
			owner.raidProjection.mu.Unlock()
			done = done && caughtUp
		}
		if done {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("raid projection did not reach committed version %d for every recipient", version)
}
