package dnfbridge

import (
	"bytes"
	"testing"
)

func TestRaidParticipationOnlyCommittedSelectedActorAndMatchingDetach(t *testing.T) {
	s, session, r, b := raidLifeRuntimeFixture(t)
	s.recordCurrentRaidParticipation(session, r)
	if len(b.Participation) != 0 {
		t.Fatal("prepared actor occupied raid position")
	}
	session.postStartMapPlayerStateSent = true
	s.recordCurrentRaidParticipation(session, r)
	if len(b.Participation) != 1 || b.Participation[99].RunID != r.raidEntry.RunID {
		t.Fatal("committed actor not tracked")
	}
	clone := cloneRuntimeRaidBattle(b)
	delete(clone.Participation, 99)
	if len(b.Participation) != 1 {
		t.Fatal("snapshot mutated live position")
	}
	oldRun := r.raidEntry.RunID
	r.raidEntry.RunID = "foreign-detach"
	s.releaseCurrentRaidParticipation(r, 99)
	if len(b.Participation) != 1 {
		t.Fatal("old detach removed current position")
	}
	r.raidEntry.RunID = oldRun
	s.releaseCurrentRaidParticipation(r, 99)
	if len(b.Participation) != 0 {
		t.Fatal("legitimate detach retained position")
	}
	s.recordCurrentRaidParticipation(session, r)
	fresh := *r
	entry := *r.raidEntry
	fresh.raidEntry = &entry
	session.dungeon.runtime = &fresh
	s.recordCurrentRaidParticipation(session, &fresh)
	s.releaseCurrentRaidParticipation(r, 99)
	if len(b.Participation) != 1 || b.Participation[99].Runtime != &fresh {
		t.Fatal("old runtime of same run removed fresh committed actor")
	}
	s.releaseCurrentRaidParticipation(&fresh, 99)
	if len(b.Participation) != 0 {
		t.Fatal("fresh runtime detach retained position")
	}
}

func TestRaidParticipationNative584LayoutRefreshClearAndRemoval(t *testing.T) {
	previous := map[uint16]currentRaidParticipationPosition{99: {DungeonID: 210}}
	next := map[uint16]currentRaidParticipationPosition{99: {DungeonID: 218, Cleared: true, GroupIndex: 1}, 100: {DungeonID: 218, GroupIndex: 1}}
	packets, _ := currentRaidParticipationProjection(previous, next, false, nil)
	want := [][]byte{{1, 210, 0, 0, 0, 0, 1, 99, 0}, {1, 218, 0, 0, 0, 0, 2, 99, 0, 100, 0}, {1, 218, 0, 0, 0, 2, 2, 99, 0, 100, 0}, {1, 218, 0, 0, 0, 4, 1, 99, 0}}
	if len(packets) != len(want) {
		t.Fatal("native frame count", len(packets))
	}
	for i, packet := range packets {
		if packet.opcode != 584 || !bytes.Equal(packet.body, want[i]) {
			t.Fatalf("native 584 frame%d=%x want%x", i, packet.body, want[i])
		}
	}
	if p, _ := currentRaidParticipationProjection(next, next, false, nil); len(p) != 0 {
		t.Fatal("unchanged occupancy replayed clear effect")
	}
	if p, _ := currentRaidParticipationProjection(next, nil, false, nil); len(p) != 1 || p[0].body[5] != 0 {
		t.Fatal("departed occupancy not removed")
	}
}

func TestRaidParticipationSingleAliasAndPhaseAttemptFilter(t *testing.T) {
	b := &runtimeRaidBattle{Single: true, Mode: 1, MemberAccounts: map[uint16]string{99: "a"}, Participation: map[uint16]currentRaidParticipation{99: {RunID: "owned", DungeonID: 5314, Attempt: 1}}}
	positions, err := currentRaidParticipationPositions(b, []runtimeRaidMemberState{{CharID: 99}})
	if err != nil || positions[99].DungeonID != 210 {
		t.Fatal("r76 single alias lost", positions, err)
	}
	b.Phase = 1
	positions, err = currentRaidParticipationPositions(b, []runtimeRaidMemberState{{CharID: 99}})
	if err != nil || len(positions) != 0 {
		t.Fatal("previous phase occupancy retained")
	}
	b.Phase = 0
	b.TimedOut = true
	positions, err = currentRaidParticipationPositions(b, []runtimeRaidMemberState{{CharID: 99}})
	if err != nil || len(positions) != 0 {
		t.Fatal("timed-out occupancy retained")
	}
}

func TestRaidParticipationOldSocketCleanupCannotEraseFreshActor(t *testing.T) {
	s, old, r, b := raidLifeRuntimeFixture(t)
	old.postStartMapPlayerStateSent = true
	s.recordCurrentRaidParticipation(old, r)
	_, fresh, newRuntime := newBackToVillageRuntime(t)
	fresh.accountID, fresh.loginPacketSeen, fresh.accountFromToken = "account-1", true, true
	fresh.channel.ID, fresh.channel.Type = 23, 23
	s.bindGameSessionCharacter(fresh, 99)
	entry := *r.raidEntry
	newRuntime.raidEntry = &entry
	newRuntime.Dungeon = r.Dungeon
	newRuntime.AccountID = r.AccountID
	newRuntime.Character = r.Character
	fresh.postStartMapPlayerStateSent = true
	s.recordCurrentRaidParticipation(fresh, newRuntime)
	if b.Participation[99].Session != fresh {
		t.Fatal("fresh authenticated socket did not own position")
	}
	s.releaseCurrentRaidParticipationSession(old)
	if b.Participation[99].Session != fresh {
		t.Fatal("old socket cleanup erased fresh actor")
	}
	fresh.dungeon.runtime = nil
	s.releaseCurrentRaidParticipationSession(fresh)
	if len(b.Participation) != 0 {
		t.Fatal("current socket nil-runtime cleanup left ghost position")
	}
}
