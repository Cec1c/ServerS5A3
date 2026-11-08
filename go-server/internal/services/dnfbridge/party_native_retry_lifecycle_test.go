package dnfbridge

import "testing"

// A real native readiness response is asynchronous. The completed-run return
// owner must not restore the old instance while that response is outstanding.
func TestNativePartyRetryKeepsRetiredRunsUntilAdmissionCompletes(t *testing.T) {
	f := newLeaderHandoffRoundtripFixture(t)
	service, leader, follower := f.service, f.leader, f.follower
	f.formPartyThroughClientAccept(t)
	f.selectDungeon(t, leader, 700)
	f.finishLeaderHandoffLoading(t, leader, follower)
	for _, peer := range []*gameSession{leader, follower} {
		peer.loginPacketSeen = true
		peer.accountFromToken = true
		peer.accountID = "account-1"
	}
	oldLeader, oldFollower := leader.dungeon.runtime, follower.dungeon.runtime
	completePartyRetryFixture(t, service, leader)
	completePartyRetryFixture(t, service, follower)
	t.Setenv("DNFBRIDGE_PARTY_NATIVE_ADMISSION", "")
	if err := service.retryCurrentCompletedDungeon(leader, oldLeader, "test_native_retry"); err != nil {
		t.Fatal(err)
	}
	service.partyNativeAdmission.mu.Lock()
	p := service.partyNativeAdmission.bySession[leader]
	service.partyNativeAdmission.mu.Unlock()
	if p == nil {
		t.Fatal("retry never requested the native readiness proof")
	}
	leader.dungeon.mu.Lock()
	leaderRestored := leader.dungeon.runtime == oldLeader
	leader.dungeon.mu.Unlock()
	follower.dungeon.mu.Lock()
	followerRestored := follower.dungeon.runtime == oldFollower
	follower.dungeon.mu.Unlock()
	for _, peer := range p.peers {
		if err := service.handlePartyNativeConnectionReply(peer.session, nativeAdmissionReply(p, peer.session, 0)); err != nil {
			t.Fatal(err)
		}
	}
	waitNativePartyAdmissionForTest(t, service, p)
	if !leaderRestored || !followerRestored {
		t.Fatalf("completed runs lost while asynchronous admission was pending: leader=%v follower=%v", leaderRestored, followerRestored)
	}
	for _, peer := range []*gameSession{leader, follower} {
		peer.dungeon.mu.Lock()
		runtime := peer.dungeon.runtime
		peer.dungeon.mu.Unlock()
		if runtime == nil || runtime == oldLeader || runtime == oldFollower {
			t.Fatalf("member %d did not get a fresh run", peer.selectedCharacterID)
		}
	}
}
