package guild

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"

	r "longheng.io/server/internal/modules/dnf/repository"
)

const guildReadSnapshotSources = 8
const guildReadSnapshotVersions = 3

type guildReadSnapshot struct {
	registry Registry
	encoded  map[uint32][]byte
	owners   map[string]uint32
}
type guildReadSnapshotEntry struct {
	revision string
	snapshot *guildReadSnapshot
}
type guildReadSnapshotCache struct {
	sync.RWMutex
	gate    chan struct{}
	entries []guildReadSnapshotEntry
}

var guildReadSnapshots = struct {
	sync.Mutex
	caches map[*r.GuildSnapshotNamespace]*guildReadSnapshotCache
	order  []*r.GuildSnapshotNamespace
}{caches: map[*r.GuildSnapshotNamespace]*guildReadSnapshotCache{}}

func guildSnapshotCacheFor(namespace *r.GuildSnapshotNamespace) *guildReadSnapshotCache {
	guildReadSnapshots.Lock()
	defer guildReadSnapshots.Unlock()
	if cache := guildReadSnapshots.caches[namespace]; cache != nil {
		return cache
	}
	for len(guildReadSnapshots.order) >= guildReadSnapshotSources {
		old := guildReadSnapshots.order[0]
		guildReadSnapshots.order = guildReadSnapshots.order[1:]
		delete(guildReadSnapshots.caches, old)
	}
	cache := &guildReadSnapshotCache{gate: make(chan struct{}, 1)}
	guildReadSnapshots.caches[namespace] = cache
	guildReadSnapshots.order = append(guildReadSnapshots.order, namespace)
	return cache
}

func newGuildReadSnapshot(reg Registry) (*guildReadSnapshot, error) {
	snapshot := &guildReadSnapshot{registry: Registry{Guilds: reg.Guilds}, encoded: map[uint32][]byte{}, owners: map[string]uint32{}}
	for id, g := range reg.Guilds {
		encoded, err := json.Marshal(g)
		if err != nil {
			return nil, err
		}
		snapshot.encoded[id] = encoded
		for characterID := range g.Members {
			if _, dup := snapshot.owners[characterID]; dup {
				return nil, ErrInvalidRegistry
			}
			snapshot.owners[characterID] = id
		}
	}
	return snapshot, nil
}

// The cached registry is immutable and never returned to callers. Every hit
// validates the source fingerprint, including inside repeatable-read views.
func loadGuildReadSnapshot(ctx context.Context, settings r.SettingsRepository) (*guildReadSnapshot, error) {
	reader, ok := settings.(r.GuildSnapshotRevisionReader)
	if !ok || reader.GuildSnapshotNamespace() == nil {
		reg, err := Load(ctx, settings)
		if err != nil {
			return nil, err
		}
		return newGuildReadSnapshot(reg)
	}
	cache := guildSnapshotCacheFor(reader.GuildSnapshotNamespace())
	revision, found, err := reader.LoadGuildSnapshotRevision(ctx)
	if err != nil {
		return nil, err
	}
	if !found {
		return newGuildReadSnapshot(NewRegistry())
	}
	if snapshot := cache.lookup(revision); snapshot != nil {
		return snapshot, nil
	}
	select {
	case cache.gate <- struct{}{}:
		defer func() { <-cache.gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	for attempt := 0; attempt < 3; attempt++ {
		revision, found, err := reader.LoadGuildSnapshotRevision(ctx)
		if err != nil {
			return nil, err
		}
		if !found {
			return newGuildReadSnapshot(NewRegistry())
		}
		if snapshot := cache.lookup(revision); snapshot != nil {
			return snapshot, nil
		}
		record, exists, err := settings.Load(ctx, RegistryScope)
		if err != nil {
			return nil, err
		}
		raw := record.Values[registryStateKey]
		digest := sha256.Sum256([]byte(raw))
		if !exists || revision != "" && hex.EncodeToString(digest[:]) != revision {
			continue
		}
		after, stillFound, err := reader.LoadGuildSnapshotRevision(ctx)
		if err != nil {
			return nil, err
		}
		if !stillFound || after != revision {
			continue
		}
		reg := NewRegistry()
		if strings.TrimSpace(raw) != "" {
			if err = json.Unmarshal([]byte(raw), &reg); err != nil {
				return nil, err
			}
			if err = validateRegistry(reg); err != nil {
				return nil, err
			}
			normalizeRegistry(&reg)
		}
		snapshot, err := newGuildReadSnapshot(reg)
		if err != nil {
			return nil, err
		}
		if len(raw) <= 4<<20 {
			cache.Lock()
			cache.entries = append([]guildReadSnapshotEntry{{revision: revision, snapshot: snapshot}}, cache.entries...)
			if len(cache.entries) > guildReadSnapshotVersions {
				cache.entries = cache.entries[:guildReadSnapshotVersions]
			}
			cache.Unlock()
		}
		return snapshot, nil
	}
	return nil, ErrRankingSnapshotChanged
}

func (cache *guildReadSnapshotCache) lookup(revision string) *guildReadSnapshot {
	cache.RLock()
	defer cache.RUnlock()
	for _, entry := range cache.entries {
		if entry.revision == revision {
			return entry.snapshot
		}
	}
	return nil
}

func (snapshot *guildReadSnapshot) owned(owner r.GuildOwner) *Guild {
	id, found := snapshot.owners[owner.CharacterID]
	if !found {
		return nil
	}
	g := snapshot.registry.Guilds[id]
	member, found := g.Members[owner.CharacterID]
	if !found || member.AccountID != owner.AccountID {
		return nil
	}
	return g
}

// GuildReadMembership binds one operation to one immutable registry view and
// one character. Acquire a new handle for each read operation, then validate
// the authoritative character before requesting a display or entitlement.
type GuildReadMembership struct {
	snapshot    *guildReadSnapshot
	characterID string
}

func ReadGuildMembership(ctx context.Context, settings r.SettingsRepository, characterID string) (*GuildReadMembership, bool, error) {
	snapshot, err := loadGuildReadSnapshot(ctx, settings)
	if err != nil {
		return nil, false, err
	}
	_, found := snapshot.owners[characterID]
	if !found {
		return nil, false, nil
	}
	return &GuildReadMembership{snapshot: snapshot, characterID: characterID}, true, nil
}

func (membership *GuildReadMembership) Display(owner r.GuildOwner) (*Guild, error) {
	if membership == nil || membership.characterID != owner.CharacterID {
		return nil, nil
	}
	return membership.snapshot.display(owner)
}

func (membership *GuildReadMembership) Entitlement(owner r.GuildOwner) (GuildEntitlement, bool) {
	if membership == nil || membership.characterID != owner.CharacterID {
		return GuildEntitlement{}, false
	}
	return membership.snapshot.entitlement(owner)
}

// ReadGuildDisplay returns a detached display object. Mutations by packet
// projection cannot poison another session's membership or cached facts.
func ReadGuildDisplay(ctx context.Context, settings r.SettingsRepository, owner r.GuildOwner) (*Guild, error) {
	snapshot, err := loadGuildReadSnapshot(ctx, settings)
	if err != nil {
		return nil, err
	}
	return snapshot.display(owner)
}

func (snapshot *guildReadSnapshot) display(owner r.GuildOwner) (*Guild, error) {
	g := snapshot.owned(owner)
	if g == nil {
		return nil, nil
	}
	var result Guild
	if err := json.Unmarshal(snapshot.encoded[g.ID], &result); err != nil {
		return nil, err
	}
	return &result, nil
}

type GuildEntitlement struct {
	ID       uint32
	Mark     byte
	Name     string
	Benefits map[byte]Benefit
}

// ReadGuildEntitlement copies only membership-checked metadata and benefits.
// It exposes neither mutable member maps nor historical reward receipts.
func ReadGuildEntitlement(ctx context.Context, settings r.SettingsRepository, owner r.GuildOwner) (GuildEntitlement, bool, error) {
	snapshot, err := loadGuildReadSnapshot(ctx, settings)
	if err != nil {
		return GuildEntitlement{}, false, err
	}
	result, found := snapshot.entitlement(owner)
	return result, found, nil
}

func (snapshot *guildReadSnapshot) entitlement(owner r.GuildOwner) (GuildEntitlement, bool) {
	g := snapshot.owned(owner)
	if g == nil {
		return GuildEntitlement{}, false
	}
	result := GuildEntitlement{ID: g.ID, Mark: g.Mark, Name: g.Name, Benefits: make(map[byte]Benefit, len(g.Benefits))}
	for kind, value := range g.Benefits {
		result.Benefits[kind] = value
	}
	return result, true
}

// GuildReadVersion identifies immutable registry contents in this process.
// It has no mutable fields and is meaningful only within a bounded read fanout.
type GuildReadVersion struct{ snapshot *guildReadSnapshot }

func (m *GuildReadMembership) Version() GuildReadVersion {
	if m == nil {
		return GuildReadVersion{}
	}
	return GuildReadVersion{snapshot: m.snapshot}
}
