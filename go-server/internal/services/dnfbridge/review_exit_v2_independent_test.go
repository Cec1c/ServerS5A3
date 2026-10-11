package dnfbridge

import (
	"context"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/raid"
)

func reviewExitV2Party(t *testing.T) (*Service, *gameSession, *gameSession) {
	t.Helper()
	s, a, b, _, _, _ := partyDungeonSyncFixture(t)
	runPartyDungeonSyncEntry(t, s, a)
	s.options.currentClientVersion = "official-24.9"
	a.channel = channelcatalog.Channel{ID: 23, Type: 23}
	b.channel = a.channel
	ids := []uint16{a.selectedCharacterID, b.selectedCharacterID}
	battle := &runtimeRaidBattle{InstanceID: "independent-exit-v2", Mode: 1, ChannelID: 23, ChannelType: 23, Deadline: time.Now().Add(time.Hour), MemberAccounts: map[uint16]string{}, ClearedRuns: map[string]currentRaidClearedRun{}}
	state := &runtimeRaidState{RaidKey: 777, LeaderID: a.selectedCharacterID, Started: true, Battle: battle}
	for _, p := range []*gameSession{a, b} {
		completePartyRetryFixture(t, s, p)
		r := p.dungeon.runtime
		p.accountID = r.AccountID
		p.loginPacketSeen = true
		p.accountFromToken = true
		s.bindGameSessionCharacter(p, p.selectedCharacterID)
		r.settlementCardLayoutSent = false
		r.settlementCardSelectionSent = false
		r.settlementCardRewardCommitted = false
		r.settlementPhase = currentDungeonSettlementPhaseResultShown
		r.raidEntry = &currentRaidDungeonEntry{RaidKey: 777, InstanceID: battle.InstanceID, RunID: "independent-completed-run", Attempt: 1, ChannelID: 23, ChannelType: 23, Participants: ids}
		battle.MemberAccounts[p.selectedCharacterID] = r.AccountID
		state.Members = append(state.Members, runtimeRaidMemberState{CharID: p.selectedCharacterID})
	}
	battle.Dungeons = []raid.DungeonState{{ID: uint32(a.dungeon.runtime.Dungeon.ID)}}
	battle.ClearedRuns["independent-completed-run"] = currentRaidClearedRun{DungeonID: uint32(a.dungeon.runtime.Dungeon.ID), Attempt: 1, Participants: ids}
	s.raids = map[uint32]*runtimeRaidState{777: state}
	return s, a, b
}

func TestReviewExitV2NonleaderRetryDoesNotWaitForLeaderEvent(t *testing.T) {
	s, leader, peer := reviewExitV2Party(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.startGameSessionEvents(ctx, leader); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, leader)
	blocked := make(chan struct{})
	release := make(chan struct{})
	leaderDone := make(chan error, 1)
	go func() {
		leaderDone <- s.callGameSession(ctx, leader, "independent-existing-leader-event", func() error { close(blocked); <-release; return nil })
	}()
	waitTestSignal(t, blocked, "leader event not running")
	peerDone := make(chan error, 1)
	go func() { peerDone <- s.handleDungeonEplpCommand(peer, []byte{1, 0}) }()
	delayed := false
	select {
	case err := <-peerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(300 * time.Millisecond):
		delayed = true
	}
	close(release)
	if err := <-leaderDone; err != nil {
		t.Fatal(err)
	}
	if delayed {
		<-peerDone
		t.Fatal("nonleader retry waited on leader event before leader ownership rejection; simultaneous leader-to-peer action can form reciprocal waits")
	}
}

func TestReviewExitV2RestoreDoesNotResurrectChangedGenerationOrReplaceNewRuntime(t *testing.T) {
	for _, fault := range []string{"new_runtime", "changed_generation", "lost_auth", "changed_channel"} {
		t.Run(fault, func(t *testing.T) {
			s, leader, peer := reviewExitV2Party(t)
			old := peer.dungeon.runtime
			owners, err := s.captureCurrentRaidRetryReturnOwners(leader, leader.dungeon.runtime)
			if err != nil {
				t.Fatal(err)
			}
			for i := range owners {
				owners[i].retired = true
			}
			peer.dungeon.runtime = nil
			switch fault {
			case "new_runtime":
				peer.dungeon.runtime = &runtimeDungeonState{}
			case "changed_generation":
				peer.characterGeneration++
			case "lost_auth":
				peer.accountFromToken = false
			case "changed_channel":
				peer.channel.ID = 11
			}
			expected := peer.dungeon.runtime
			s.restoreCurrentRaidRetryReturnOwners(leader, owners)
			if peer.dungeon.runtime != expected || peer.dungeon.runtime == old {
				t.Fatal("stale return owner resurrected or replaced new runtime")
			}
		})
	}
}

func TestReviewExitV2RestoreDoesNotUndoCompletedPeerTownReturn(t *testing.T) {
	s, leader, peer := reviewExitV2Party(t)
	old := peer.dungeon.runtime
	owners, err := s.captureCurrentRaidRetryReturnOwners(leader, leader.dungeon.runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleDungeonEplpCommand(peer, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if peer.dungeon.runtime != nil || !old.townReturnOp24Sent {
		t.Fatal("peer normal town return did not commit; regression boundary not exercised")
	}
	s.restoreCurrentRaidRetryReturnOwners(leader, owners)
	if peer.dungeon.runtime != nil {
		t.Fatal("retry restore resurrected peer old run after peer's independent legitimate op72 town route committed")
	}
}
