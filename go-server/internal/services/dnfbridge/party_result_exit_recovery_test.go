package dnfbridge

import (
	"errors"
	"testing"
)

func TestCompletedPartySelectorLeadershipLostAfterAckReopensExit(t *testing.T) {
	f, oldLeader, oldFollower := newNativeRetryFailureFixture(t)
	s, leader, follower := f.service, f.leader, f.follower
	leaderBinding, leaderOK := s.boundGameSessionCharacterSnapshot(leader)
	followerBinding, followerOK := s.boundGameSessionCharacterSnapshot(follower)
	if !leaderOK || !followerOK {
		t.Fatal("fixture lost party bindings")
	}
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
	if err := s.selectOtherDungeonForCompletedParty(leader, oldLeader); err != nil {
		t.Fatal(err)
	}
	requireNativeRetryCompletedOwners(t, leader, follower, oldLeader, oldFollower)
}

func TestCompletedPartySelectorUnfinishedPeerReopensExit(t *testing.T) {
	f, oldLeader, oldFollower := newNativeRetryFailureFixture(t)
	s, leader, follower := f.service, f.leader, f.follower
	follower.dungeon.mu.Lock()
	oldFollower.settlementCardRewardCommitted = false
	follower.dungeon.mu.Unlock()
	// Classify this refusal only before mutation. The packet handler must not
	// pass it up as a fatal error that closes the leader's game connection.
	if retired, err := s.retireCompletedPartyForAction(leader, oldLeader, false); retired || !errors.Is(err, errCompletedPartyActionNotReady) {
		t.Fatalf("unfinished peer preflight: retired=%v err=%v", retired, err)
	}
	if err := s.handleDungeonEplpCommand(leader, []byte{1, 1}); err != nil {
		t.Fatalf("normal refusal escaped as fatal packet error: %v", err)
	}
	leader.dungeon.mu.Lock()
	retained, ack := leader.dungeon.runtime == oldLeader, oldLeader.settlementCardExitAckSent
	leader.dungeon.mu.Unlock()
	follower.dungeon.mu.Lock()
	peerRetained, reward := follower.dungeon.runtime == oldFollower, oldFollower.settlementCardRewardCommitted
	follower.dungeon.mu.Unlock()
	if !retained || !peerRetained || ack || reward {
		t.Fatalf("refused selector changed result ownership: leader=%v peer=%v ack=%v reward=%v", retained, peerRetained, ack, reward)
	}
	// Once the peer finishes its existing reward, a second ordinary action
	// must leave both completed runs and open the selector for the same party.
	follower.dungeon.mu.Lock()
	oldFollower.settlementCardRewardCommitted = true
	follower.dungeon.mu.Unlock()
	if err := s.handleDungeonEplpCommand(leader, []byte{1, 1}); err != nil {
		t.Fatal(err)
	}
	for _, peer := range []*gameSession{leader, follower} {
		peer.dungeon.mu.Lock()
		attached := peer.dungeon.runtime != nil
		peer.dungeon.mu.Unlock()
		if attached || !peer.enterSelectDungeonSent || !peer.enterSelectDungeonContextSent {
			t.Fatalf("member %d did not open selector after reward completion", peer.selectedCharacterID)
		}
	}
}

func TestNativePartyRetryLateRefusalPreservesNewExitOwner(t *testing.T) {
	for _, mode := range []string{"town_return_pending", "detached", "replacement"} {
		t.Run(mode, func(t *testing.T) {
			f, oldLeader, _ := newNativeRetryFailureFixture(t)
			s, leader, follower := f.service, f.leader, f.follower
			if err := s.handleDungeonEplpCommand(leader, []byte{1, 0}); err != nil {
				t.Fatal(err)
			}
			p := pendingNativeRetryProof(t, s, leader)
			// Deterministically represent the owner transition while PCR1 is
			// pending. No client reply has authorized a new entry in this test.
			leader.dungeon.mu.Lock()
			if !oldLeader.settlementCardExitAckSent {
				leader.dungeon.mu.Unlock()
				t.Fatal("pending retry did not consume its exit ACK")
			}
			current := oldLeader
			switch mode {
			case "town_return_pending":
				oldLeader.townReturnPending = true
			case "detached":
				current = nil
			case "replacement":
				current = &runtimeDungeonState{settlementCardExitAckSent: true}
			}
			leader.dungeon.runtime = current
			leader.dungeon.mu.Unlock()
			if err := s.handlePartyNativeConnectionReply(follower, nativeAdmissionReply(p, follower, 3)); err != nil {
				t.Fatal(err)
			}
			waitNativePartyAdmissionForTest(t, s, p)
			leader.dungeon.mu.Lock()
			defer leader.dungeon.mu.Unlock()
			if leader.dungeon.runtime != current || !oldLeader.settlementCardExitAckSent {
				t.Fatal("late refusal changed the new owner or reopened the retired action")
			}
			if current != nil && !current.settlementCardExitAckSent {
				t.Fatal("late refusal consumed the current owner's ACK")
			}
			if mode == "town_return_pending" && !oldLeader.townReturnPending {
				t.Fatal("late refusal cancelled the pending town return")
			}
		})
	}
}

func TestCompletedPartyRetryOwnerCaptureRefusalIsNonfatal(t *testing.T) {
	for _, mode := range []string{"unfinished_reward", "detached_peer"} {
		t.Run(mode, func(t *testing.T) {
			f, oldLeader, oldFollower := newNativeRetryFailureFixture(t)
			s, leader, follower := f.service, f.leader, f.follower
			// Exercise the synchronous packet-handler error boundary. Existing
			// native-positive coverage exercises the asynchronous continuation.
			t.Setenv("DNFBRIDGE_PARTY_NATIVE_ADMISSION", "off")
			follower.dungeon.mu.Lock()
			if mode == "unfinished_reward" {
				oldFollower.settlementCardRewardCommitted = false
			} else {
				follower.dungeon.runtime = nil
			}
			expectedPeer := follower.dungeon.runtime
			follower.dungeon.mu.Unlock()
			if _, err := s.captureCurrentRaidRetryReturnOwners(leader, oldLeader); !errors.Is(err, errCompletedPartyActionNotReady) {
				t.Fatalf("owner capture refusal not classified: %v", err)
			}
			if err := s.handleDungeonEplpCommand(leader, []byte{1, 0}); err != nil {
				t.Fatalf("normal retry refusal escaped as fatal packet error: %v", err)
			}
			leader.dungeon.mu.Lock()
			retained, ack := leader.dungeon.runtime == oldLeader, oldLeader.settlementCardExitAckSent
			leader.dungeon.mu.Unlock()
			follower.dungeon.mu.Lock()
			unchanged := follower.dungeon.runtime == expectedPeer
			reward := oldFollower.settlementCardRewardCommitted
			follower.dungeon.mu.Unlock()
			if !retained || ack || !unchanged || (mode == "unfinished_reward" && reward) {
				t.Fatalf("refused retry changed result ownership/rewards: retained=%v ack=%v peer=%v reward=%v", retained, ack, unchanged, reward)
			}
		})
	}
}
