package dnfbridge

import (
	"sync"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// characterEnterShortTTL 控制角色进场/投影只读快照的短 TTL（1s）。
// 覆盖“清房 -> 换房 -> 下一间”连续窗口内的高频重复 characterForEnter 查库，
// 同时保持极短过期窗口以兜底换装/升级/技能变动；写入口亦可调用
// invalidateCharacterEnterShortCache 立即失效。
const characterEnterShortTTL = time.Second

type characterEnterShortCacheKey struct {
	charID     uint16
	generation uint64
}

type characterEnterShortCacheEntry struct {
	charID       uint16
	charName     string
	character    dnfrepo.CharacterRecord
	hasCharacter bool
	expiresAt    time.Time
}

type characterEnterShortCacheFlight struct {
	done  chan struct{}
	entry characterEnterShortCacheEntry
}

type characterEnterShortCache struct {
	mu      sync.Mutex
	entries map[characterEnterShortCacheKey]characterEnterShortCacheEntry
	flights map[characterEnterShortCacheKey]*characterEnterShortCacheFlight
}

func (c *characterEnterShortCache) getOrLoad(
	key characterEnterShortCacheKey,
	now time.Time,
	loader func() (uint16, string, dnfrepo.CharacterRecord, bool),
) (uint16, string, dnfrepo.CharacterRecord, bool) {
	if c == nil || key.charID == 0 || key.generation == 0 {
		return loader()
	}
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok && now.Before(entry.expiresAt) {
		c.mu.Unlock()
		return entry.charID, entry.charName, dnfrepo.CloneCharacter(entry.character), entry.hasCharacter
	}
	if flight, inflight := c.flights[key]; inflight {
		c.mu.Unlock()
		<-flight.done
		return flight.entry.charID, flight.entry.charName, dnfrepo.CloneCharacter(flight.entry.character), flight.entry.hasCharacter
	}
	if c.flights == nil {
		c.flights = make(map[characterEnterShortCacheKey]*characterEnterShortCacheFlight, 16)
	}
	flight := &characterEnterShortCacheFlight{done: make(chan struct{})}
	c.flights[key] = flight
	c.mu.Unlock()

	loadedID, charName, record, found := loader()
	entry := characterEnterShortCacheEntry{
		charID:       loadedID,
		charName:     charName,
		character:    record,
		hasCharacter: found,
		expiresAt:    time.Now().Add(characterEnterShortTTL),
	}
	flight.entry = entry

	c.mu.Lock()
	if found && loadedID == key.charID {
		if c.entries == nil {
			c.entries = make(map[characterEnterShortCacheKey]characterEnterShortCacheEntry, 32)
		}
		if len(c.entries) > 256 {
			pruneNow := time.Now()
			for k, v := range c.entries {
				if !pruneNow.Before(v.expiresAt) {
					delete(c.entries, k)
				}
			}
		}
		c.entries[key] = entry
	}
	delete(c.flights, key)
	close(flight.done)
	c.mu.Unlock()

	return loadedID, charName, dnfrepo.CloneCharacter(record), found
}

func (s *Service) invalidateCharacterEnterShortCache(charID uint16) {
	if s == nil || charID == 0 {
		return
	}
	s.characterEnterCache.mu.Lock()
	defer s.characterEnterCache.mu.Unlock()
	for k := range s.characterEnterCache.entries {
		if k.charID == charID {
			delete(s.characterEnterCache.entries, k)
		}
	}
}
