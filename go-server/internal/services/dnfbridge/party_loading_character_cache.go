package dnfbridge

import (
	"context"
	"sync"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type partyLoadingCharacterSnapshot struct {
	charID       uint16
	charName     string
	character    dnfrepo.CharacterRecord
	hasCharacter bool
}

type partyLoadingCharacterFlight struct {
	done  chan struct{}
	entry partyLoadingCharacterSnapshot
}

// partyLoadingCharacterSnapshotCache 是单次组队进图/换房读条屏障周期内共享的
// 角色+装备聚合快照缓存（含按角色 singleflight 去重）。
//
// 生命周期严格绑定于单次 partyDungeonLoadingCycle：
//   - 屏障触发放行前，在后台 goroutine 并发预热全队成员快照（将 DB IO 移出事件循环）；
//   - 4 个会话事件循环执行 sendCurrentPostStartMapPlayerPlacement /
//     prepareCurrentFinishLoadingCharacterStateSnapshot / sendCurrentPartyFrameProjection 时
//     直接复用同一份克隆快照，把单次进图 ~28 次重复 mysqlCharStore.Load 收敛到每角色 1 次；
//   - 放行结束随 cycle 自动释放，跨房间/换装/升级零陈旧风险，不改任何发包顺序与字节。
type partyLoadingCharacterSnapshotCache struct {
	mu      sync.Mutex
	entries map[uint16]partyLoadingCharacterSnapshot
	flights map[uint16]*partyLoadingCharacterFlight
}

func newPartyLoadingCharacterSnapshotCache(capacity int) *partyLoadingCharacterSnapshotCache {
	if capacity < 4 {
		capacity = 4
	}
	return &partyLoadingCharacterSnapshotCache{
		entries: make(map[uint16]partyLoadingCharacterSnapshot, capacity),
		flights: make(map[uint16]*partyLoadingCharacterFlight, capacity),
	}
}

func (c *partyLoadingCharacterSnapshotCache) getOrLoad(
	charID uint16,
	loader func() (uint16, string, dnfrepo.CharacterRecord, bool),
) (uint16, string, dnfrepo.CharacterRecord, bool) {
	if c == nil || charID == 0 {
		return loader()
	}
	c.mu.Lock()
	if entry, ok := c.entries[charID]; ok {
		c.mu.Unlock()
		return entry.charID, entry.charName, dnfrepo.CloneCharacter(entry.character), entry.hasCharacter
	}
	if flight, inflight := c.flights[charID]; inflight {
		c.mu.Unlock()
		<-flight.done
		return flight.entry.charID, flight.entry.charName, dnfrepo.CloneCharacter(flight.entry.character), flight.entry.hasCharacter
	}
	flight := &partyLoadingCharacterFlight{done: make(chan struct{})}
	c.flights[charID] = flight
	c.mu.Unlock()

	loadedID, charName, record, found := loader()
	entry := partyLoadingCharacterSnapshot{
		charID:       loadedID,
		charName:     charName,
		character:    record,
		hasCharacter: found,
	}
	flight.entry = entry

	c.mu.Lock()
	if found && loadedID == charID {
		c.entries[charID] = entry
	}
	delete(c.flights, charID)
	close(flight.done)
	c.mu.Unlock()

	return loadedID, charName, dnfrepo.CloneCharacter(record), found
}

// prewarmPartyLoadingCharacterSnapshots 在向各成员事件循环投递放行事件之前，
// 于工作线程并发预热全队角色快照，避免 4 条事件循环在同一毫秒阻塞于 MySQL 查库。
func (s *Service) prewarmPartyLoadingCharacterSnapshots(
	cache *partyLoadingCharacterSnapshotCache,
	members []uint16,
	owners map[uint16]partyDungeonLoadingOwner,
) {
	if s == nil || cache == nil || len(members) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, charID := range members {
		owner, ok := owners[charID]
		if !ok || owner.session == nil || charID == 0 {
			continue
		}
		wg.Add(1)
		go func(id uint16, sess *gameSession) {
			defer wg.Done()
			cache.getOrLoad(id, func() (uint16, string, dnfrepo.CharacterRecord, bool) {
				return s.loadCharacterForEnterUncached(ctx, sess, id)
			})
		}(charID, owner.session)
	}
	wg.Wait()
}
