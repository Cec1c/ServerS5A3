package dnfbridge

import (
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"testing"
	"time"
)

func r195PublicRewardAssetSnapshot(t *testing.T, s *Service, session *gameSession) []any {
	t.Helper()
	assets := rejoinAssetSnapshot(t, s, session)
	// Existing container refresh persists its normalization timestamp even
	// when contents are equal. Keep all item/quantity/receipt fields exact.
	inventory := assets[2].(repo.InventoryRecord)
	inventory.UpdatedAt = time.Time{}
	for key, item := range inventory.Slots {
		// RawEntry is the container projection cache regenerated for a fresh
		// connection. Semantic item identity/count/binding/expiry stay exact.
		item.RawEntry = nil
		inventory.Slots[key] = item
	}
	assets[2] = inventory
	return assets
}

func r195PublicFiendRewardFixture(t *testing.T, phase string) (*raidStartGuardFixture, []*gameSession) {
	t.Helper()
	if phase != "fiendwar" {
		t.Fatal("fixture is scoped to accepted FiendWar", phase)
	}
	var f *raidStartGuardFixture
	var session, peer *gameSession
	var peers []*gameSession
	f, peers = actualCurrentFiendWar(t, 2)
	session, peer = peers[0], peers[1]
	b := f.state.Battle
	f.state.Started = true
	b.EnterAfter, b.Deadline = time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
	b.PhaseCompleted, b.FiendWar.Complete, b.PhaseCompletedAt = true, true, time.Now()
	b.ClearedRuns = map[string]currentRaidClearedRun{}
	for i := uint32(0); i < b.AntonRewards.Phases[0].MinimumClears; i++ {
		b.ClearedRuns[strconv.Itoa(int(i))] = currentRaidClearedRun{DungeonID: 4978, Attempt: 1, Participants: []uint16{19, 20}}
	}
	if err := f.service.freezeCurrentFiendWarRewardFlowLocked(*f.state); err != nil {
		t.Fatal(err)
	}

	if _, errs := f.service.commitCurrentRaidWeeklyIntents(time.Now()); len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, owner := range []*gameSession{session, peer} {
		if err := f.service.sendCurrentAntonTerminalResult(owner, cloneRuntimeRaidState(*f.state)); err != nil {
			t.Fatal(err)
		}
	}
	f.conn = session.conn.(*bufferConn)
	return f, []*gameSession{session, peer}
}
