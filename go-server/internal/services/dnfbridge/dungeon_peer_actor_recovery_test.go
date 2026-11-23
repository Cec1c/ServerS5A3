package dnfbridge

import "testing"

func peerRecoveryFixture(t *testing.T) (leaderExitFixtureState, *fakeCurrentDungeonDeathTimerQueue) {
	t.Helper()
	f := newLeaderExitFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	q := newFakeCurrentDungeonDeathTimerQueue()
	f.service.gameplayTimers = q
	for _, p := range []*gameSession{f.leader, f.member} {
		p.setFinishLoadingGateState(true)
		p.setFinishLoadingGateCompletion(true)
		p.setFinishLoadingGatePostFinish(true)
	}
	return f, q
}
