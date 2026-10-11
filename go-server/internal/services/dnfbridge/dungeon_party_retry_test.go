package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func completePartyRetryFixture(t *testing.T, service *Service, session *gameSession) {
	t.Helper()
	runtime := session.dungeon.runtime
	if err := runtime.Session.CompleteCurrentRoom(); err != nil {
		t.Fatal(err)
	}
	runtime.settlementEntrySent = true
	runtime.settlementPlayResultReceived = true
	runtime.settlementStatisticReceived = true
	markCurrentDungeonCardRewardCommittedForTest(runtime)
}

func TestCompletedPartyRetryEntersBothMembersWithoutTownRequests(t *testing.T) {
	service, leader, follower, _, _, trace := partyDungeonSyncFixture(t)
	runPartyDungeonSyncEntry(t, service, leader)
	oldLeader, oldFollower := leader.dungeon.runtime, follower.dungeon.runtime
	completePartyRetryFixture(t, service, leader)
	completePartyRetryFixture(t, service, follower)
	trace.mu.Lock()
	trace.chunks = nil
	trace.mu.Unlock()
	if err := service.handleGameCommand(leader, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketEplpCommand), []byte{1, 0}); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		session *gameSession
		old     *runtimeDungeonState
	}{{leader, oldLeader}, {follower, oldFollower}} {
		runtime := check.session.dungeon.runtime
		if runtime == nil || runtime == check.old || runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
			t.Fatalf("member %d did not enter a fresh active run", check.session.selectedCharacterID)
		}
		steps := trace.orderedSteps(t)
		if firstPartyDungeonSyncStep(steps, check.session.selectedCharacterID, 29, nil) < 0 || firstPartyDungeonSyncStep(steps, check.session.selectedCharacterID, 28, nil) < 0 {
			t.Fatalf("member %d missing actual re-entry packets", check.session.selectedCharacterID)
		}
	}
	if leader.dungeon.runtime.partyInstance != follower.dungeon.runtime.partyInstance || leader.dungeon.runtime.partyInstance == nil {
		t.Fatal("retry did not share one fresh instance")
	}
}

func TestCompletedPartyRetryDoesNotDetachLeaderWhenPeerResultIsIncomplete(t *testing.T) {
	service, leader, follower, _, _, _ := partyDungeonSyncFixture(t)
	runPartyDungeonSyncEntry(t, service, leader)
	completePartyRetryFixture(t, service, leader)
	oldLeader, oldFollower := leader.dungeon.runtime, follower.dungeon.runtime
	if retired, err := service.retireCompletedPartyForRetry(leader, oldLeader); err == nil || retired {
		t.Fatal("incomplete peer permitted retry")
	}
	if leader.dungeon.runtime != oldLeader || follower.dungeon.runtime != oldFollower {
		t.Fatal("failed group validation detached a member")
	}
}

func TestCompletedPartySelectOtherRetiresBothAndOpensFollowerSelector(t *testing.T) {
	service, leader, follower, _, _, trace := partyDungeonSyncFixture(t)
	runPartyDungeonSyncEntry(t, service, leader)
	completePartyRetryFixture(t, service, leader)
	completePartyRetryFixture(t, service, follower)
	trace.mu.Lock()
	trace.chunks = nil
	trace.mu.Unlock()
	if err := service.handleGameCommand(leader, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketEplpCommand), []byte{1, 1}); err != nil {
		t.Fatal(err)
	}
	for _, session := range []*gameSession{leader, follower} {
		if session.dungeon.runtime != nil {
			t.Fatalf("member %d kept the old boss room", session.selectedCharacterID)
		}
		if !session.enterSelectDungeonContextSent || !session.enterSelectDungeonSent {
			t.Fatalf("member %d missing full selector", session.selectedCharacterID)
		}
		steps := trace.orderedSteps(t)
		for _, opcode := range []uint16{72, 15, 27} {
			if firstPartyDungeonSyncStep(steps, session.selectedCharacterID, opcode, nil) < 0 {
				t.Fatalf("member %d missing op%d", session.selectedCharacterID, opcode)
			}
		}
	}
	runPartyDungeonSyncEntry(t, service, leader)
	if leader.dungeon.runtime == nil || follower.dungeon.runtime == nil || leader.dungeon.runtime.partyInstance == nil || leader.dungeon.runtime.partyInstance != follower.dungeon.runtime.partyInstance {
		t.Fatal("selector re-entry did not create a shared fresh instance")
	}
}
