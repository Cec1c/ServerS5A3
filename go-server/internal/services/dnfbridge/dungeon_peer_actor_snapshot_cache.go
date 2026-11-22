package dnfbridge

import (
	"sync"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const dungeonPeerActorSnapshotTTL = 30 * time.Second

type dungeonPeerActorSnapshotKey struct {
	actorCharID uint16
	generation  uint64
	actorOwner  byte
}

type dungeonPeerActorSnapshot struct {
	charName     string
	character    dnfrepo.CharacterRecord
	hasCharacter bool
	mode0Body    []byte
	mode1Body    []byte
	subtype0Body []byte
	expiresAt    time.Time
}

// dungeonPeerActorSnapshotCache 缓存副本内队友投影的成品线包（mode0/mode1/subtype0）。
// 4 人组队切图时 3 个队友 × 最多 6 轮恢复重试会对同一 actor 连续触发 18 次完整 DB 读与
// REPEATABLE READ buff 换装事务；短 TTL 缓存将同一房间内的重复构建收敛到每角色 1 次，
// 零改动发包顺序、包体内容与原生恢复状态机。
type dungeonPeerActorSnapshotCache struct {
	mu      sync.Mutex
	entries map[dungeonPeerActorSnapshotKey]dungeonPeerActorSnapshot
}

func (s *Service) lookupDungeonPeerActorSnapshot(key dungeonPeerActorSnapshotKey, now time.Time) (dungeonPeerActorSnapshot, bool) {
	if s == nil {
		return dungeonPeerActorSnapshot{}, false
	}
	s.dungeonPeerActorCache.mu.Lock()
	defer s.dungeonPeerActorCache.mu.Unlock()
	if s.dungeonPeerActorCache.entries == nil {
		return dungeonPeerActorSnapshot{}, false
	}
	entry, ok := s.dungeonPeerActorCache.entries[key]
	if !ok || !now.Before(entry.expiresAt) {
		if ok {
			delete(s.dungeonPeerActorCache.entries, key)
		}
		return dungeonPeerActorSnapshot{}, false
	}
	return entry, true
}

func (s *Service) storeDungeonPeerActorSnapshot(key dungeonPeerActorSnapshotKey, entry dungeonPeerActorSnapshot, now time.Time) {
	if s == nil {
		return
	}
	entry.expiresAt = now.Add(dungeonPeerActorSnapshotTTL)
	s.dungeonPeerActorCache.mu.Lock()
	defer s.dungeonPeerActorCache.mu.Unlock()
	if s.dungeonPeerActorCache.entries == nil {
		s.dungeonPeerActorCache.entries = make(map[dungeonPeerActorSnapshotKey]dungeonPeerActorSnapshot, 16)
	}
	if len(s.dungeonPeerActorCache.entries) > 256 {
		for k, v := range s.dungeonPeerActorCache.entries {
			if !now.Before(v.expiresAt) {
				delete(s.dungeonPeerActorCache.entries, k)
			}
		}
	}
	s.dungeonPeerActorCache.entries[key] = entry
}

func (s *Service) invalidateDungeonPeerActorSnapshot(actorCharID uint16) {
	if s == nil || actorCharID == 0 {
		return
	}
	s.dungeonPeerActorCache.mu.Lock()
	defer s.dungeonPeerActorCache.mu.Unlock()
	for k := range s.dungeonPeerActorCache.entries {
		if k.actorCharID == actorCharID {
			delete(s.dungeonPeerActorCache.entries, k)
		}
	}
}
