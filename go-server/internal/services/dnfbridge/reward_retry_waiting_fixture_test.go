package dnfbridge

import "testing"

// Minimal test-only helper extracted from the private waiting-area fixture.
// It establishes the raid start waiting presence needed by the accepted reward
// tests without importing the full P0 waiting-area test suite.

func enterRaidStartWaitingFixture(t *testing.T, f *raidStartGuardFixture, battleID string) {
	t.Helper()
	if f.service.onlinePlayers == nil {
		f.service.onlinePlayers = newOnlinePlayerManager()
	}
	for _, member := range f.state.Members {
		peer, found := f.service.onlineGameSession(member.CharID)
		if !found {
			t.Fatal("开团夹具缺少已认证成员", member.CharID)
		}
		rule, err := f.service.currentRaidAdmissionRule(peer)
		if err != nil {
			t.Fatal(err)
		}
		f.service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: member.CharID, AccountID: peer.accountID, Session: peer, TownID: rule.WaitingTown, AreaID: rule.WaitingArea, ChannelID: peer.channel.ID, Instance: f.state.RaidKey, RaidBattleInstance: battleID})
	}
}
