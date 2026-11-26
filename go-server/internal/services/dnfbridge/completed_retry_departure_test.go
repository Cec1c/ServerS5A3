package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/worldmap"
	"testing"
)

func TestCompletedRetryAfterMemberLeaves(t *testing.T) {
	t.Setenv("DNFBRIDGE_PARTY_NATIVE_ADMISSION", "off")
	for _, leaderLeaves := range []bool{false, true} {
		name := "follower_leaves"
		if leaderLeaves {
			name = "leader_leaves"
		}
		t.Run(name, func(t *testing.T) {
			s, leader, follower, _, _, trace := partyDungeonSyncFixture(t)
			logger, logPath := newPeerEndpointTestLogger(t)
			s.packetLog = logger
			t.Cleanup(func() {
				if t.Failed() {
					t.Log(readPeerEndpointTestLog(t, logger, logPath))
				}
				_ = logger.Close()
			})
			if _, ok := s.bootstrapManagedRuntimePartyForSession(leader); !ok {
				t.Fatal("party fixture was not registered")
			}
			for _, se := range []*gameSession{leader, follower} {
				se.loginPacketSeen = true
				se.accountFromToken = true
				se.accountID = "account-1"
			}
			runPartyDungeonSyncEntry(t, s, leader)
			s.options.currentClientVersion = "official-24.9"
			completePartyRetryFixture(t, s, leader)
			completePartyRetryFixture(t, s, follower)
			leaving, staying := follower, leader
			if leaderLeaves {
				leaving, staying = leader, follower
			}
			old := staying.dungeon.runtime
			departedRun := leaving.dungeon.runtime
			if err := s.handleOnlineLeaveParty(leaving, 13); err != nil {
				t.Fatal(err)
			}
			trace.mu.Lock()
			trace.chunks = nil
			trace.mu.Unlock()
			if err := s.handleGameCommand(staying, 1, 72, []byte{1, 0}); err != nil {
				t.Fatal(err)
			}
			r := staying.dungeon.runtime
			if r == nil || r == old || r.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
				t.Fatal("survivor did not enter a fresh active dungeon after member departure")
			}
			steps := trace.orderedSteps(t)
			for _, op := range []uint16{28, 29} {
				if firstPartyDungeonSyncStep(steps, staying.selectedCharacterID, op, nil) < 0 {
					t.Fatalf("survivor missing entry packet %d", op)
				}
			}
			if leaving.dungeon.runtime != nil && leaving.dungeon.runtime != departedRun {
				t.Fatal("departed member was pulled into retry")
			}
		})
	}
}

func TestCompletedRetryAfterFollowerReturnsToTown(t *testing.T) {
	t.Setenv("DNFBRIDGE_PARTY_NATIVE_ADMISSION", "off")
	s, leader, follower, _, _, trace := partyDungeonSyncFixture(t)
	if _, ok := s.bootstrapManagedRuntimePartyForSession(leader); !ok {
		t.Fatal("party fixture was not registered")
	}
	for _, se := range []*gameSession{leader, follower} {
		se.loginPacketSeen = true
		se.accountFromToken = true
		se.accountID = "account-1"
	}
	runPartyDungeonSyncEntry(t, s, leader)
	s.options.currentClientVersion = "official-24.9"
	completePartyRetryFixture(t, s, leader)
	completePartyRetryFixture(t, s, follower)
	old := leader.dungeon.runtime
	if err := s.handleGameCommand(follower, 1, 72, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if follower.dungeon.runtime != nil {
		t.Fatal("follower did not return to town")
	}
	if err := s.handleTownSetUserPosition(follower, buildTownPositionRequest(450, 234, 5, 100)); err != nil {
		t.Fatal(err)
	}
	if state := s.runtimePartyStateForSession(leader); len(runtimePartyMembers(state)) != 2 {
		t.Fatal("completed return unexpectedly left the party")
	}
	trace.mu.Lock()
	trace.chunks = nil
	trace.mu.Unlock()
	if err := s.handleGameCommand(leader, 1, 72, []byte{1, 0}); err != nil {
		t.Fatal(err)
	}
	if leader.dungeon.runtime == old || leader.dungeon.runtime == nil || follower.dungeon.runtime == nil {
		t.Fatal("leader retry cannot reenter with a member already back in town")
	}
	for _, se := range []*gameSession{leader, follower} {
		if se.dungeon.runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
			t.Fatal("retry did not commit an active run")
		}
	}
}
