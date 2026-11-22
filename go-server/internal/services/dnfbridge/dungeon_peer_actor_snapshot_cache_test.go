package dnfbridge

import (
	"testing"
	"time"
)

func TestDungeonPeerActorSnapshotCacheHitExpireInvalidate(t *testing.T) {
	s := &Service{}
	key := dungeonPeerActorSnapshotKey{actorCharID: 1001, generation: 7, actorOwner: 1}
	now := time.Unix(1700000000, 0)

	if _, ok := s.lookupDungeonPeerActorSnapshot(key, now); ok {
		t.Fatalf("expected initial cache miss")
	}
	s.storeDungeonPeerActorSnapshot(key, dungeonPeerActorSnapshot{
		charName:     "Tester",
		hasCharacter: true,
		mode0Body:    []byte{1, 2, 3},
		mode1Body:    []byte{4, 5, 6},
	}, now)

	got, ok := s.lookupDungeonPeerActorSnapshot(key, now.Add(2*time.Second))
	if !ok || got.charName != "Tester" || len(got.mode1Body) != 3 {
		t.Fatalf("expected cache hit within TTL, got ok=%v entry=%+v", ok, got)
	}
	if _, ok := s.lookupDungeonPeerActorSnapshot(key, now.Add(dungeonPeerActorSnapshotTTL+time.Millisecond)); ok {
		t.Fatalf("expected cache miss after TTL expiry")
	}

	s.storeDungeonPeerActorSnapshot(key, dungeonPeerActorSnapshot{charName: "Tester"}, now)
	s.invalidateDungeonPeerActorSnapshot(1001)
	if _, ok := s.lookupDungeonPeerActorSnapshot(key, now.Add(time.Second)); ok {
		t.Fatalf("expected cache miss after explicit invalidation")
	}
}
