package dnfbridge

import "testing"

// The full result-action handler must refuse follower team actions before it
// acknowledges an exit. These cases deliberately do not synthesize a retry.
func TestNativePartyRetryFollowerActionsPreserveCompletedExit(t *testing.T) {
	for _, option := range []byte{0, 1} {
		t.Run(map[byte]string{0: "retry", 1: "other_selector"}[option], func(t *testing.T) {
			f, oldLeader, oldFollower := newNativeRetryFailureFixture(t)
			phase := oldFollower.settlementPhase
			if err := f.service.handleDungeonEplpCommand(f.follower, []byte{1, option}); err != nil {
				t.Fatal(err)
			}
			requireNativeRetryCompletedOwners(t, f.leader, f.follower, oldLeader, oldFollower)
			f.follower.dungeon.mu.Lock()
			ack, after := oldFollower.settlementCardExitAckSent, oldFollower.settlementPhase
			f.follower.dungeon.mu.Unlock()
			if ack || after != phase {
				t.Fatalf("refused follower action consumed exit: ack=%v phase=%v want=%v", ack, after, phase)
			}
		})
	}
}

func TestNativePartyRetryLeadershipLostAfterAckReopensExit(t *testing.T) {
	f, oldLeader, oldFollower := newNativeRetryFailureFixture(t)
	s, leader, follower := f.service, f.leader, f.follower
	leaderBinding, leaderOK := s.boundGameSessionCharacterSnapshot(leader)
	followerBinding, followerOK := s.boundGameSessionCharacterSnapshot(follower)
	if !leaderOK || !followerOK {
		t.Fatal("fixture lost party bindings")
	}
	// Model the gap between accepting a result action and its retry helper:
	// another session has authoritatively transferred leadership in that gap.
	leader.dungeon.mu.Lock()
	oldLeader.settlementCardExitAckSent = true
	leader.dungeon.mu.Unlock()
	result := s.runtimePartyManagerForService().TransferLeader(
		leaderBinding.character, leaderBinding.generation,
		followerBinding.character, followerBinding.generation,
	)
	if !result.OK {
		t.Fatalf("transfer leader: %s", result.Reason)
	}
	s.publishRuntimePartySnapshot(result.Party)
	if err := s.retryCurrentCompletedDungeon(leader, oldLeader, "test_leadership_lost_after_ack"); err != nil {
		t.Fatal(err)
	}
	requireNativeRetryCompletedOwners(t, leader, follower, oldLeader, oldFollower)
}
