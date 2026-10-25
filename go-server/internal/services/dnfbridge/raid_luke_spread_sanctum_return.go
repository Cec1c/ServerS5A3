package dnfbridge

import "longheng.io/server/internal/modules/dnf/worldmap"

// currentLukeSpreadSanctumDungeonID is the real Phase-1 SPREAD SANCTUM entry
// (5056). The live report is scoped to this already-completed result page.
const currentLukeSpreadSanctumDungeonID int64 = 5056

// currentLukeSpreadSanctumCompletedMapTownReturnReady accepts only the op72
// town action for a completed team Luke 5056 map whose frozen run is present in
// the accepted ClearedRuns journal. That journal entry is the reconnect receipt:
// it binds the restored runtime to the same instance, run, attempt, channel and
// participants. The ordinary op34/35 presentation chain may
// be absent after reconnect, so it cannot own this return. Retry, other-map
// selection, expired receipts, other Luke sanctums and Anton stay on the
// existing ordinary-settlement boundary. Caller holds session.dungeon.mu.
func (s *Service) currentLukeSpreadSanctumCompletedMapTownReturnReady(session *gameSession, runtime *runtimeDungeonState) bool {
	if session == nil || runtime == nil || runtime.raidEntry == nil ||
		runtime.Dungeon.ID != currentLukeSpreadSanctumDungeonID ||
		runtime.raidEntry.Phase != 0 || !runtime.raidEntry.teamMode ||
		runtime.Session == nil ||
		runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
		return false
	}
	return s.currentRaidCompletedMapExitReceiptReady(session, runtime, false, false, byte(currentRaidDailyLukeMode))
}
