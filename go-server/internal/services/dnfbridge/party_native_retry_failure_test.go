package dnfbridge

import (
	"context"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// newNativeRetryFailureFixture uses the complete official fixture chain and
// leaves both peers on their committed completed result pages. selectDungeon
// answers the first-entry native query through the existing synthetic PCR1
// helper; this is server-control-flow coverage, not a real native exchange.
func newNativeRetryFailureFixture(t *testing.T) (
	*inviteAcceptDungeonFixture,
	*runtimeDungeonState,
	*runtimeDungeonState,
) {
	t.Helper()
	t.Setenv("DNFBRIDGE_PARTY_NATIVE_ADMISSION", "on")
	f := newLeaderHandoffRoundtripFixture(t)
	service, leader, follower := f.service, f.leader, f.follower
	f.formPartyThroughClientAccept(t)
	f.selectDungeon(t, leader, 700)
	f.finishLeaderHandoffLoading(t, leader, follower)
	for _, session := range []*gameSession{leader, follower} {
		session.loginPacketSeen = true
		session.accountFromToken = true
		session.accountID = "account-1"
	}

	leader.dungeon.mu.Lock()
	oldLeader := leader.dungeon.runtime
	leader.dungeon.mu.Unlock()
	follower.dungeon.mu.Lock()
	oldFollower := follower.dungeon.runtime
	follower.dungeon.mu.Unlock()
	if oldLeader == nil || oldFollower == nil || oldLeader == oldFollower || oldLeader.partyInstance != oldFollower.partyInstance {
		t.Fatal("fixture did not finish one shared dungeon instance")
	}

	completePartyRetryFixture(t, service, leader)
	completePartyRetryFixture(t, service, follower)
	return f, oldLeader, oldFollower
}

func pendingNativeRetryProof(t *testing.T, service *Service, leader *gameSession) *partyNativeConnectionProof {
	t.Helper()
	service.partyNativeAdmission.mu.Lock()
	p := service.partyNativeAdmission.bySession[leader]
	service.partyNativeAdmission.mu.Unlock()
	if p == nil {
		t.Fatal("retry did not request a native connection proof")
	}
	return p
}

func copyNativeRetryStats(stats map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(stats))
	for key, value := range stats {
		out[key] = value
	}
	return out
}

func requireNativeRetryCompletedOwners(t *testing.T, leader, follower *gameSession, oldLeader, oldFollower *runtimeDungeonState) {
	t.Helper()
	leader.dungeon.mu.Lock()
	leaderRetained := leader.dungeon.runtime == oldLeader
	leaderAck := oldLeader.settlementCardExitAckSent
	leader.dungeon.mu.Unlock()
	follower.dungeon.mu.Lock()
	followerRetained := follower.dungeon.runtime == oldFollower
	follower.dungeon.mu.Unlock()
	if !leaderRetained || !followerRetained {
		t.Fatalf("completed owners changed: leader=%v follower=%v", leaderRetained, followerRetained)
	}
	if oldLeader.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted || oldFollower.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
		t.Fatal("retry replaced the completed run state")
	}
	if !oldLeader.settlementClearRewardSent || !oldLeader.settlementCardRewardCommitted ||
		!oldFollower.settlementClearRewardSent || !oldFollower.settlementCardRewardCommitted {
		t.Fatal("retry discarded a committed completed-run reward")
	}
	if leaderAck {
		t.Fatal("failed native retry left the exit ACK gate consumed")
	}
}

func TestNativePartyRetryNegativeReplyPreservesCompletedRunAndRewards(t *testing.T) {
	f, oldLeader, oldFollower := newNativeRetryFailureFixture(t)
	service, leader, follower := f.service, f.leader, f.follower
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("repository fixture unavailable")
	}
	ctx := context.Background()
	statsBefore := make(map[string]map[string]int64, 2)
	for _, id := range []string{"99", "100"} {
		character, found, err := repositories.Character.Load(ctx, id)
		if err != nil || !found {
			t.Fatalf("load character %s before retry: found=%t err=%v", id, found, err)
		}
		statsBefore[id] = copyNativeRetryStats(character.Stats)
	}

	if err := service.handleDungeonEplpCommand(leader, []byte{1, 0}); err != nil {
		t.Fatal(err)
	}
	p := pendingNativeRetryProof(t, service, leader)
	leader.dungeon.mu.Lock()
	if leader.dungeon.runtime != oldLeader {
		leader.dungeon.mu.Unlock()
		t.Fatal("old completed runtime retired before the native reply")
	}
	leader.dungeon.mu.Unlock()
	follower.dungeon.mu.Lock()
	if follower.dungeon.runtime != oldFollower {
		follower.dungeon.mu.Unlock()
		t.Fatal("peer completed runtime retired before the native reply")
	}
	follower.dungeon.mu.Unlock()

	// A synthetic negative PCR1 reply exercises refusal handling. A real native
	// UDP failure still needs separate client-machine evidence.
	if err := service.handlePartyNativeConnectionReply(follower, nativeAdmissionReply(p, follower, 3)); err != nil {
		t.Fatal(err)
	}
	waitNativePartyAdmissionForTest(t, service, p)
	requireNativeRetryCompletedOwners(t, leader, follower, oldLeader, oldFollower)
	for _, id := range []string{"99", "100"} {
		character, found, err := repositories.Character.Load(ctx, id)
		if err != nil || !found {
			t.Fatalf("load character %s after retry: found=%t err=%v", id, found, err)
		}
		if !reflect.DeepEqual(statsBefore[id], character.Stats) {
			t.Fatalf("native refusal changed character %s stats: before=%v after=%v", id, statsBefore[id], character.Stats)
		}
	}
}

func TestNativePartyRetryTimeoutReopensExitAndAllowsAnotherAttempt(t *testing.T) {
	f, oldLeader, oldFollower := newNativeRetryFailureFixture(t)
	service, leader, follower := f.service, f.leader, f.follower
	if err := service.handleDungeonEplpCommand(leader, []byte{1, 0}); err != nil {
		t.Fatal(err)
	}
	p := pendingNativeRetryProof(t, service, leader)
	// Deliberately send no PCR1 reply. The production admission deadline drives
	// refusal, restoring the old owners and reopening the one-shot exit gate.
	waitNativePartyAdmissionForTest(t, service, p)
	requireNativeRetryCompletedOwners(t, leader, follower, oldLeader, oldFollower)

	if err := service.handleDungeonEplpCommand(leader, []byte{1, 0}); err != nil {
		t.Fatal(err)
	}
	retryProof := pendingNativeRetryProof(t, service, leader)
	if retryProof.nonce == p.nonce {
		t.Fatal("second retry reused the timed-out native proof")
	}
	leader.dungeon.mu.Lock()
	if leader.dungeon.runtime != oldLeader {
		leader.dungeon.mu.Unlock()
		t.Fatal("second retry retired the completed run before native readiness")
	}
	leader.dungeon.mu.Unlock()

	// End the second attempt through the same synthetic negative path so the
	// test does not claim a real native readiness result.
	if err := service.handlePartyNativeConnectionReply(follower, nativeAdmissionReply(retryProof, follower, 3)); err != nil {
		t.Fatal(err)
	}
	waitNativePartyAdmissionForTest(t, service, retryProof)
	requireNativeRetryCompletedOwners(t, leader, follower, oldLeader, oldFollower)
}

func TestNativePartyRetryPendingMembershipChangeCannotEnter(t *testing.T) {
	for _, mode := range []string{"leader_transfer", "member_leave"} {
		t.Run(mode, func(t *testing.T) {
			f, oldLeader, oldFollower := newNativeRetryFailureFixture(t)
			service, leader, follower := f.service, f.leader, f.follower
			if err := service.handleDungeonEplpCommand(leader, []byte{1, 0}); err != nil {
				t.Fatal(err)
			}
			p := pendingNativeRetryProof(t, service, leader)

			switch mode {
			case "leader_transfer":
				leaderBinding, leaderOK := service.boundGameSessionCharacterSnapshot(leader)
				followerBinding, followerOK := service.boundGameSessionCharacterSnapshot(follower)
				if !leaderOK || !followerOK {
					t.Fatal("fixture lost a bound party session")
				}
				result := service.runtimePartyManagerForService().TransferLeader(
					leaderBinding.character, leaderBinding.generation,
					followerBinding.character, followerBinding.generation,
				)
				if !result.OK {
					t.Fatalf("transfer leader: %s", result.Reason)
				}
			case "member_leave":
				identity, bound := service.boundGameSessionCharacterSnapshot(follower)
				if !bound {
					t.Fatal("fixture lost the leaving member")
				}
				_, result, removed := service.leaveManagedRuntimeParty(identity, "test_native_retry_pending_leave")
				if !removed {
					t.Fatalf("leave member: %s", result.Reason)
				}
			default:
				t.Fatalf("unknown mode %q", mode)
			}

			if service.partyNativeProofCurrent(p) {
				t.Fatal("native proof survived an authoritative membership change")
			}
			leader.dungeon.mu.Lock()
			if leader.dungeon.runtime != oldLeader {
				leader.dungeon.mu.Unlock()
				t.Fatal("membership change entered a fresh dungeon before native refusal")
			}
			leader.dungeon.mu.Unlock()
			follower.dungeon.mu.Lock()
			if follower.dungeon.runtime != oldFollower {
				follower.dungeon.mu.Unlock()
				t.Fatal("membership change replaced a pending completed owner")
			}
			follower.dungeon.mu.Unlock()

			if err := service.handlePartyNativeConnectionReply(leader, nativeAdmissionReply(p, leader, 0)); err != nil {
				t.Fatal(err)
			}
			waitNativePartyAdmissionForTest(t, service, p)
			requireNativeRetryCompletedOwners(t, leader, follower, oldLeader, oldFollower)
		})
	}
}

func TestNativePartyRetryUnavailableResultQueueReopensExit(t *testing.T) {
	f, oldLeader, oldFollower := newNativeRetryFailureFixture(t)
	s, leader, follower := f.service, f.leader, f.follower
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, peer := range []*gameSession{leader, follower} {
		if err := s.startGameSessionEvents(ctx, peer); err != nil {
			t.Fatal(err)
		}
		defer s.stopGameSessionEvents(context.Background(), peer)
	}
	if err := s.callGameSession(ctx, leader, "test-retry", func() error {
		return s.handleDungeonEplpCommand(leader, []byte{1, 0})
	}); err != nil {
		t.Fatal(err)
	}
	p := pendingNativeRetryProof(t, s, leader)
	// Closing only the event queue leaves the character binding and completed
	// owner intact. Whether the query was sent before shutdown or not, neither
	// its refusal nor its timeout can schedule a result callback on this queue.
	if err := s.stopGameSessionEvents(ctx, leader); err != nil {
		t.Fatal(err)
	}
	waitNativePartyAdmissionForTest(t, s, p)
	requireNativeRetryCompletedOwners(t, leader, follower, oldLeader, oldFollower)
}

func TestNativePartyRetryPeerStillSettlingReopensExitAfterPositiveReplies(t *testing.T) {
	f, oldLeader, oldFollower := newNativeRetryFailureFixture(t)
	s, leader, follower := f.service, f.leader, f.follower
	// A peer can have completed the fight while its card reward is still being
	// committed. Native network readiness does not authorize retiring that run.
	follower.dungeon.mu.Lock()
	oldFollower.settlementCardRewardCommitted = false
	follower.dungeon.mu.Unlock()
	if err := s.handleDungeonEplpCommand(leader, []byte{1, 0}); err != nil {
		t.Fatal(err)
	}
	completeNativePartyAdmissionForTest(t, s, leader)
	leader.dungeon.mu.Lock()
	retained := leader.dungeon.runtime == oldLeader
	ack := oldLeader.settlementCardExitAckSent
	leader.dungeon.mu.Unlock()
	follower.dungeon.mu.Lock()
	peerRetained := follower.dungeon.runtime == oldFollower
	oldFollower.settlementCardRewardCommitted = true
	follower.dungeon.mu.Unlock()
	if !retained || !peerRetained || ack {
		t.Fatalf("unfinished peer must preserve owners and reopen retry: leader=%v peer=%v ack=%v", retained, peerRetained, ack)
	}
	// Completing the outstanding reward must make the next ordinary click work,
	// without leaving/reforming the party or replacing the completed owners.
	if err := s.handleDungeonEplpCommand(leader, []byte{1, 0}); err != nil {
		t.Fatal(err)
	}
	completeNativePartyAdmissionForTest(t, s, leader)
	for _, peer := range []*gameSession{leader, follower} {
		peer.dungeon.mu.Lock()
		current := peer.dungeon.runtime
		peer.dungeon.mu.Unlock()
		if current == nil || current == oldLeader || current == oldFollower {
			t.Fatalf("member %d did not enter after its reward completed", peer.selectedCharacterID)
		}
	}
}
