package dnfbridge

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	guildMode8CacheTTL        = 10 * time.Second
	guildMode8CacheMaxEntries = 2048
)

var guildMode8CacheMaxInflight = 256

type guildMode8CacheKey struct {
	characterID   uint16
	generation    uint64
	actorSession  *gameSession
	ownerChannel  byte
	directory     byte
	appearanceRev uint64
	skillPageAll  bool
}

type guildMode8CacheEntry struct {
	accountID        string
	body             []byte
	expiresAt        time.Time
	dynamicExpiresAt time.Time
}

type guildMode8Inflight struct {
	done            chan struct{}
	invalidationGen uint64
	retired         bool
	entry           guildMode8CacheEntry
	valid           bool
	notFound        bool
	err             error
}

type guildMode8LookupOutcome int

const (
	guildMode8LookupHit guildMode8LookupOutcome = iota
	guildMode8LookupWait
	guildMode8LookupBuild
	guildMode8LookupBypass
)

// guildMode8Cache caches immutable silent cache-only mode8 frames per online
// actor and coalesces concurrent guild roster priming bursts for the same peer.
// Leaf lock only: never acquire any other lock while holding mu.
type guildMode8Cache struct {
	mu              sync.Mutex
	entries         map[guildMode8CacheKey]guildMode8CacheEntry
	inflight        map[guildMode8CacheKey]*guildMode8Inflight
	invalidationGen map[uint16]uint64
	nowFn           func() time.Time
}

func (c *guildMode8Cache) now() time.Time {
	if c != nil && c.nowFn != nil {
		return c.nowFn()
	}
	return time.Now()
}

func (s *Service) guildMode8Now() time.Time {
	if s != nil && s.onlinePlayers != nil {
		return s.onlinePlayers.guildMode8.now()
	}
	return time.Now()
}

func (c *guildMode8Cache) generation(characterID uint16) uint64 {
	if c == nil || characterID == 0 {
		return 0
	}
	c.mu.Lock()
	gen := c.invalidationGen[characterID]
	c.mu.Unlock()
	return gen
}

func (s *Service) guildMode8CacheSendGuard(
	characterID uint16,
	generation uint64,
	dynamicExpiresAt time.Time,
) func() bool {
	return func() bool {
		if s != nil && s.onlinePlayers != nil && s.onlinePlayers.guildMode8.generation(characterID) != generation {
			return false
		}
		return dynamicExpiresAt.IsZero() || s.guildMode8Now().Before(dynamicExpiresAt)
	}
}

func (c *guildMode8Cache) invalidate(characterID uint16) {
	if c == nil || characterID == 0 {
		return
	}
	c.mu.Lock()
	if c.invalidationGen == nil {
		c.invalidationGen = make(map[uint16]uint64)
	}
	c.invalidationGen[characterID]++
	for k := range c.entries {
		if k.characterID == characterID {
			delete(c.entries, k)
		}
	}
	for k, f := range c.inflight {
		if k.characterID == characterID {
			f.retired = true
			delete(c.inflight, k)
		}
	}
	c.mu.Unlock()
}

func (c *guildMode8Cache) lookupOrStart(key guildMode8CacheKey, accountID string) (guildMode8LookupOutcome, guildMode8CacheEntry, *guildMode8Inflight, uint64) {
	if c == nil {
		return guildMode8LookupBypass, guildMode8CacheEntry{}, nil, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	gen := c.invalidationGen[key.characterID]
	if entry, ok := c.entries[key]; ok {
		if now.Before(entry.expiresAt) && entry.accountID == accountID {
			return guildMode8LookupHit, entry, nil, gen
		}
		delete(c.entries, key)
	}
	if existing, waiting := c.inflight[key]; waiting && !existing.retired {
		return guildMode8LookupWait, guildMode8CacheEntry{}, existing, existing.invalidationGen
	}
	if guildMode8CacheMaxInflight > 0 && len(c.inflight) >= guildMode8CacheMaxInflight {
		return guildMode8LookupBypass, guildMode8CacheEntry{}, nil, gen
	}
	if c.inflight == nil {
		c.inflight = make(map[guildMode8CacheKey]*guildMode8Inflight)
	}
	flight := &guildMode8Inflight{
		done:            make(chan struct{}),
		invalidationGen: gen,
	}
	c.inflight[key] = flight
	return guildMode8LookupBuild, guildMode8CacheEntry{}, flight, gen
}

func guildMode8EffectiveExpiry(now time.Time, dynamicExpiresAt time.Time) time.Time {
	expiresAt := now.Add(guildMode8CacheTTL)
	if !dynamicExpiresAt.IsZero() && dynamicExpiresAt.Before(expiresAt) {
		return dynamicExpiresAt
	}
	return expiresAt
}

func (c *guildMode8Cache) storeLocked(key guildMode8CacheKey, entry guildMode8CacheEntry, now time.Time) {
	if c.entries == nil {
		c.entries = make(map[guildMode8CacheKey]guildMode8CacheEntry)
	}
	if len(c.entries) >= guildMode8CacheMaxEntries {
		for k, v := range c.entries {
			if !now.Before(v.expiresAt) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= guildMode8CacheMaxEntries {
			var oldestKey guildMode8CacheKey
			var oldestTime time.Time
			first := true
			for k, v := range c.entries {
				if first || v.expiresAt.Before(oldestTime) {
					oldestKey, oldestTime, first = k, v.expiresAt, false
				}
			}
			if !first {
				delete(c.entries, oldestKey)
			}
		}
	}
	c.entries[key] = entry
}

func (c *guildMode8Cache) storeIfCurrent(key guildMode8CacheKey, entry guildMode8CacheEntry, expectedGen uint64) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.invalidationGen[key.characterID] != expectedGen {
		return false
	}
	now := c.now()
	expiresAt := guildMode8EffectiveExpiry(now, entry.dynamicExpiresAt)
	if !expiresAt.After(now) {
		return false
	}
	entry.expiresAt = expiresAt
	c.storeLocked(key, entry, now)
	return true
}

// buildCurrentLightweightUserInfoMode3Body builds a minimal wire-exact mode3
// record for silent cache-only guild roster priming. It omits worn equipment
// create rows, creature, name-tag, and skill-page payloads so DNF.exe registers
// the peer in its auxiliary cache (category 4) without synchronously loading
// dozens of players' NPK/IMG/ANI assets on the UI main thread. Explicit View
// Info (cacheOnly=false) still sends the full mode8 + mode3 pair on demand.
func buildCurrentLightweightUserInfoMode3Body(
	serverRoute byte,
	ownerChannel byte,
	objectKey uint16,
	character dnfrepo.CharacterRecord,
) []byte {
	var w packetWriter
	w.writeByte(3)
	w.writeUint16(1)
	w.writeByte(serverRoute)
	w.writeByte(ownerChannel)
	w.writeUint32(0) // adventure-group manage level (omitted in silent prime)
	w.writeUint16(0) // mode3 raw8 auxiliary u16
	w.writeByte(0)   // mode3 raw8 flag A
	w.writeByte(0)   // mode3 raw8 flag B
	w.writeUint16(objectKey)
	w.writeUint32(statU32(character, "exp", 0))
	w.writeUint32(currentMode1StatBlobWireSize)
	w.writeZero(currentMode1StatBlobWireSize)
	w.writeByte(0)    // extraEquipmentSlotState
	w.writeByte(0)    // createRows count = 0 (no CItem allocations on client)
	w.writeUint32(0)  // sub_1D77560 final state dword
	w.writeUint32(0)  // nameTagItemID
	w.writeUint32(0)  // nameTagExpireTime
	w.writeByte(0xff) // sub_2003420 selected resource group (empty skill page)
	w.writeByte(0)    // group0 count
	w.writeByte(0)    // group1 count
	w.writeByte(0)    // creature.level
	w.writeUint32(0)
	w.writeUint32(0)
	w.writeUint32(0)
	w.writeByte(0)
	w.writeByte(0)
	w.writeByte(0)
	w.writeByte(0)
	w.writeByte(0) // sub_2006940 list count
	w.writeUint32(0)
	w.writeUint16(clampUint16ForUserInfo(statU32(character, "pvp_grade", 0)))
	w.writeUint32(statU32(character, "pvp_rank_point", 0))
	w.writeRawDstr(nil)
	w.writeByte(byte(numericCharacterStatValue(character, "expert_job_type")))
	w.writeByte(0)
	w.writeUint32(0) // sub_2002DC0 count
	w.writeByte(0)   // sub_2006BB0 count
	w.writeByte(0)   // sub_20070C0 count
	return w.bytes()
}

func currentSceneGuildProjectionFromGuild(g *dnfguild.Guild) currentSceneGuildProjection {
	if g == nil || g.ID == 0 {
		return currentSceneGuildProjection{}
	}
	return currentSceneGuildProjection{
		id:   g.ID,
		name: g.Name,
		mark: uint32(g.Mark),
	}
}

// loadOrBuildGuildCacheOnlyMode8 returns an immutable lightweight silent mode8
// frame for cache-only guild roster priming. It keeps the cache's invalidation
// generation, retired-flight handling and coalescing while omitting worn
// equipment/avatar/creature/name-tag/skill payloads so client UI threads do
// not synchronously load NPK sprites for every online guildmate. Explicit View
// Info (cacheOnly=false) builds and sends the full mode8 + mode3 pair.
func (s *Service) loadOrBuildGuildCacheOnlyMode8(
	ctx context.Context,
	a currentGuildMemberProfileAccess,
	owner byte,
	guildSnapshot ...*dnfguild.Guild,
) ([]byte, uint64, uint64, time.Time, bool, error) {
	actor := a.actor.session
	var knownGuild *dnfguild.Guild
	if len(guildSnapshot) > 0 {
		knownGuild = guildSnapshot[0]
	}
	buildEntry := func(_ time.Time) (guildMode8CacheEntry, bool, error) {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return guildMode8CacheEntry{}, false, err
			}
		}
		repos, ok := s.repositoryGroupFor(ctx)
		if !ok || repos.Character == nil {
			return guildMode8CacheEntry{}, false, dnfguild.ErrRepositoryUnavailable
		}
		charIDStr := strconv.Itoa(int(a.actor.character))
		c, found, err := repos.Character.Load(ctx, charIDStr)
		if err != nil {
			return guildMode8CacheEntry{}, false, err
		}
		if !found || c.CharacterID != charIDStr || c.AccountID != a.actorAccount || c.Stats["delete_flag"] != 0 {
			return guildMode8CacheEntry{}, false, nil
		}
		lightCharacter := dnfrepo.CloneCharacter(c)
		lightCharacter.Roster.Entry.EquipSummary = nil
		if lightCharacter.Stats != nil {
			delete(lightCharacter.Stats, "name_tag_item_id")
			delete(lightCharacter.Stats, "name_tag_expire_time")
		}
		var guildProj currentSceneGuildProjection
		if knownGuild != nil && knownGuild.ID == a.guildID {
			guildProj = currentSceneGuildProjectionFromGuild(knownGuild)
		} else {
			var guildErr error
			guildProj, guildErr = s.currentSceneGuild(ctx, a.actor.character)
			if guildErr != nil {
				return guildMode8CacheEntry{}, false, guildErr
			}
		}
		objectKey := currentSceneActorObjectKey(a.actor.character)
		route := s.currentSceneActorRoute()
		mode0 := buildCurrentSceneObjectListBodyWithCreatureAndAdventureNameInContext(
			objectKey,
			lightCharacter,
			true,
			c.Name,
			currentEquippedCreatureSnapshot{},
			"",
			owner,
			route,
			guildProj,
		)
		mode3 := buildCurrentLightweightUserInfoMode3Body(route, owner, objectKey, lightCharacter)
		priming, err := buildCurrentCrossSceneUserInfoBody(mode0, mode3)
		if err != nil {
			return guildMode8CacheEntry{}, false, err
		}
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return guildMode8CacheEntry{}, false, err
			}
		}
		return guildMode8CacheEntry{
			accountID: a.actorAccount,
			body:      append([]byte(nil), priming...),
			// Lightweight mode3 omits the name-tag payload. Keep the compatibility
			// field zero so the entry lives only for the normal cache TTL.
			dynamicExpiresAt: time.Time{},
		}, true, nil
	}

	if s.onlinePlayers == nil {
		revision := currentCrossSceneAppearanceRevision(actor)
		entry, ok, err := buildEntry(time.Now())
		return entry.body, revision, 0, entry.dynamicExpiresAt, ok, err
	}
	cache := &s.onlinePlayers.guildMode8

	for attempt := 0; attempt < 2; attempt++ {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, 0, 0, time.Time{}, false, err
			}
		}
		revision := currentCrossSceneAppearanceRevision(actor)
		key := guildMode8CacheKey{
			characterID:   a.actor.character,
			generation:    a.actor.generation,
			actorSession:  actor,
			ownerChannel:  owner,
			directory:     a.request.directory,
			appearanceRev: revision,
		}
		outcome, hitEntry, flight, gen := cache.lookupOrStart(key, a.actorAccount)
		switch outcome {
		case guildMode8LookupHit:
			return hitEntry.body, revision, gen, hitEntry.dynamicExpiresAt, true, nil
		case guildMode8LookupWait:
			select {
			case <-flight.done:
				if flight.err == nil && flight.valid && flight.entry.accountID == a.actorAccount {
					if cache.generation(a.actor.character) == flight.invalidationGen && cache.now().Before(flight.entry.expiresAt) {
						return flight.entry.body, revision, flight.invalidationGen, flight.entry.dynamicExpiresAt, true, nil
					}
					continue
				}
				if flight.err == nil && flight.notFound {
					return nil, 0, 0, time.Time{}, false, nil
				}
				if flight.err != nil {
					if (errors.Is(flight.err, context.Canceled) || errors.Is(flight.err, context.DeadlineExceeded)) && ctx.Err() == nil && attempt == 0 {
						continue
					}
					return nil, 0, 0, time.Time{}, false, flight.err
				}
				continue
			case <-ctx.Done():
				return nil, 0, 0, time.Time{}, false, ctx.Err()
			}
		case guildMode8LookupBypass:
			buildStart := cache.now()
			built, ok, buildErr := buildEntry(buildStart)
			if buildErr != nil {
				return nil, 0, 0, time.Time{}, false, buildErr
			}
			if !ok {
				return nil, 0, 0, time.Time{}, false, nil
			}
			stillCurrent := ctx.Err() == nil &&
				currentCrossSceneAppearanceRevision(actor) == key.appearanceRev &&
				s.boundGameSessionCharacterCurrent(a.actor)
			if stillCurrent && cache.storeIfCurrent(key, built, gen) {
				return built.body, revision, gen, built.dynamicExpiresAt, true, nil
			}
			if !s.boundGameSessionCharacterCurrent(a.actor) {
				return nil, 0, 0, time.Time{}, false, nil
			}
			if ctx.Err() != nil {
				return nil, 0, 0, time.Time{}, false, ctx.Err()
			}
			if attempt == 0 {
				continue
			}
			return nil, 0, 0, time.Time{}, false, nil
		case guildMode8LookupBuild:
			buildStart := cache.now()
			var built guildMode8CacheEntry
			var ok bool
			var buildErr error
			var published bool
			func() {
				defer func() {
					// Evaluate non-cache predicates before taking leaf cache.mu.
					stillCurrent := buildErr == nil && ok && ctx.Err() == nil &&
						currentCrossSceneAppearanceRevision(actor) == key.appearanceRev &&
						s.boundGameSessionCharacterCurrent(a.actor)

					cache.mu.Lock()
					flightActive := !flight.retired &&
						cache.inflight[key] == flight &&
						cache.invalidationGen[key.characterID] == flight.invalidationGen
					finishNow := cache.now()
					expiresAt := guildMode8EffectiveExpiry(finishNow, built.dynamicExpiresAt)
					if stillCurrent && flightActive && expiresAt.After(finishNow) {
						built.expiresAt = expiresAt
						cache.storeLocked(key, built, finishNow)
						flight.entry = built
						flight.valid = true
						published = true
					} else if buildErr != nil {
						flight.err = buildErr
					} else if !ok {
						flight.notFound = true
					}
					if cache.inflight[key] == flight {
						delete(cache.inflight, key)
					}
					cache.mu.Unlock()
					close(flight.done)
				}()
				built, ok, buildErr = buildEntry(buildStart)
			}()
			if buildErr != nil {
				return nil, 0, 0, time.Time{}, false, buildErr
			}
			if !ok {
				return nil, 0, 0, time.Time{}, false, nil
			}
			if published {
				return built.body, revision, flight.invalidationGen, built.dynamicExpiresAt, true, nil
			}
			if !s.boundGameSessionCharacterCurrent(a.actor) {
				return nil, 0, 0, time.Time{}, false, nil
			}
			if ctx.Err() != nil {
				return nil, 0, 0, time.Time{}, false, ctx.Err()
			}
			if attempt == 0 {
				continue
			}
			return nil, 0, 0, time.Time{}, false, nil
		}
	}

	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, 0, 0, time.Time{}, false, err
		}
	}
	return nil, 0, 0, time.Time{}, false, nil
}
