package dnfbridge

import "testing"

// Keep the receiver in the same run while replacing only the peer connection.
// The old character-id projection must not suppress the replacement actor.
func TestDungeonPeerReplacementProjectsToRetainedLeader(t *testing.T) {
	s, leader, old := peerProjectionEntryFixture(t)
	enterDungeonRun(t, s, []*gameSession{leader, old}, 7)
	markPeerSceneStableForTest(leader)
	if err := s.projectDungeonPartyPeerActors(old, "first"); err != nil {
		t.Fatal(err)
	}
	leader.conn.(*bufferConn).write.Reset()
	replacement := &gameSession{conn: &bufferConn{}, channel: old.channel, accountID: old.accountID,
		selectedCharacterID: old.selectedCharacterID, characterGeneration: old.characterGeneration + 1}
	s.bindGameSessionCharacter(replacement, old.selectedCharacterID)
	s.promoteResidentGameSession(replacement, old.selectedCharacterID)
	storeRuntimePartyState(replacement, runtimePartyStateSnapshot(old))
	enterDungeonRun(t, s, []*gameSession{replacement}, 7)
	if err := s.projectDungeonPartyPeerActors(replacement, "replacement"); err != nil {
		t.Fatal(err)
	}
	frames := splitUpperFrames(t, leader.conn.(*bufferConn).write.Bytes())
	mode0, mode1, op9 := r160UpperPeerActorCounts(frames, replacement.selectedCharacterID)
	if mode0 == 0 || mode1 != 2 || op9 == 0 {
		t.Fatalf("replacement must receive creation and stable-scene resend: mode0=%d mode1=%d op9=%d", mode0, mode1, op9)
	}
	leader.conn.(*bufferConn).write.Reset()
	if err := s.projectDungeonPartyPeerActors(replacement, "repeat"); err != nil {
		t.Fatal(err)
	}
	if leader.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("replacement projection was not idempotent")
	}
	if err := s.projectDungeonPartyPeerActors(old, "retired"); err != nil {
		t.Fatal(err)
	}
	if leader.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("retired peer published after replacement")
	}
}

func TestDungeonPeerReplacementRetriesFailedProjection(t *testing.T) {
	s, leader, peer := peerProjectionEntryFixture(t)
	enterDungeonRun(t, s, []*gameSession{leader, peer}, 7)
	good := leader.conn
	broken := &r160FailWritesConn{fail: true}
	leader.conn = broken
	if err := s.projectDungeonPartyPeerActors(peer, "failed"); err == nil {
		t.Fatal("expected transport failure")
	}
	leader.conn = good
	leader.conn.(*bufferConn).write.Reset()
	if err := s.projectDungeonPartyPeerActors(peer, "retry"); err != nil {
		t.Fatal(err)
	}
	a, b, c := r160UpperPeerActorCounts(splitUpperFrames(t, leader.conn.(*bufferConn).write.Bytes()), peer.selectedCharacterID)
	if a == 0 || b == 0 || c == 0 {
		t.Fatal("failed projection could not retry", a, b, c)
	}
}
