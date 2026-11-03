package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

func TestPartyRoomMoveAdvancesPeerProjectionGeneration(t *testing.T) {
	s, leader, follower := partyMovePreflightFixture(t)
	peers := []*gameSession{leader, follower}
	for i, receiver := range peers {
		runtime := receiver.dungeon.runtime
		scene, _ := runtime.Session.Scene()
		s.recordDungeonPartyRunKey(receiver, runtime, scene)
		key, ok := s.currentDungeonPartyRunKey(receiver)
		actor, bound := s.boundGameSessionCharacterSnapshot(peers[1-i])
		if !ok || !bound || !s.claimDungeonPeerActorResend(receiver, key, actor) {
			t.Fatal("initial room did not consume its peer resend")
		}
	}
	moveLoadingCycleFixture(t, s, leader, follower)
	for i, receiver := range peers {
		runtime := receiver.dungeon.runtime
		scene, _ := runtime.Session.Scene()
		want, _ := dungeonPartyRunKeyFrom(receiver, runtime, scene)
		key, ok := s.currentDungeonPartyRunKey(receiver)
		if !ok || key != want {
			t.Errorf("member %d retained old actor room: got=%+v want=%+v", receiver.selectedCharacterID, key, want)
		}
		actor, _ := s.boundGameSessionCharacterSnapshot(peers[1-i])
		if !s.claimDungeonPeerActorResend(receiver, key, actor) {
			t.Errorf("member %d lost the next room's final peer projection", receiver.selectedCharacterID)
		}
		if s.claimDungeonPeerActorResend(receiver, key, actor) {
			t.Fatal("same-room duplicate replayed peer projection")
		}
	}
	// Repeating the current destination is rejected by the move path. It must
	// not invalidate the already sent projection or re-arm another resend.
	body := make([]byte, dungeoncmd.MoveMapRequestSize)
	body[0] = 1
	if err := s.handleDungeonMoveMap(leader, body); err != nil {
		t.Fatal(err)
	}
	for i, receiver := range peers {
		key, _ := s.currentDungeonPartyRunKey(receiver)
		actor, _ := s.boundGameSessionCharacterSnapshot(peers[1-i])
		if s.claimDungeonPeerActorResend(receiver, key, actor) {
			t.Fatal("an uncommitted move re-armed a completed peer projection")
		}
	}
}
