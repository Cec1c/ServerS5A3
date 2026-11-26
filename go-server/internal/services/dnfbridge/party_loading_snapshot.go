package dnfbridge

import (
	"context"
	"strconv"
	"sync"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type partyLoadingSnapshotKey struct{}
type partyLoadingRead[T any] struct {
	done   chan struct{}
	record T
	found  bool
	err    error
}
type partyLoadingSnapshots struct {
	mu         sync.Mutex
	owners     map[*gameSession]uint64 // immutable placement epochs, published with the cache
	characters map[string]*partyLoadingRead[dnfrepo.CharacterRecord]
	equipment  map[string]*partyLoadingRead[dnfrepo.EquipmentRecord]
}

func newPartyLoadingSnapshots() *partyLoadingSnapshots {
	return &partyLoadingSnapshots{owners: make(map[*gameSession]uint64), characters: make(map[string]*partyLoadingRead[dnfrepo.CharacterRecord]), equipment: make(map[string]*partyLoadingRead[dnfrepo.EquipmentRecord])}
}

// One loader owns each key. Waiters never hold the cache mutex during IO or
// waiting, and each caller receives its own detached mutable record.
func readPartyLoadingSnapshot[T any](ctx context.Context, mu *sync.Mutex, entries map[string]*partyLoadingRead[T], id string, load func(context.Context, string) (T, bool, error), clone func(T) T) (T, bool, error) {
	mu.Lock()
	e, exists := entries[id]
	if !exists {
		e = &partyLoadingRead[T]{done: make(chan struct{})}
		entries[id] = e
	}
	mu.Unlock()
	if exists {
		select {
		case <-e.done:
		case <-ctx.Done():
			var zero T
			return zero, false, ctx.Err()
		}
	} else {
		e.record, e.found, e.err = load(ctx, id)
		mu.Lock()
		if e.err != nil && entries[id] == e {
			delete(entries, id)
		}
		close(e.done)
		mu.Unlock()
	}
	return clone(e.record), e.found, e.err
}

func (c *partyLoadingSnapshots) invalidate() {
	c.mu.Lock()
	clear(c.characters)
	clear(c.equipment)
	c.mu.Unlock()
}

type partyLoadingCharacterRepository struct {
	dnfrepo.CharacterRepository
	cache *partyLoadingSnapshots
}

func (r partyLoadingCharacterRepository) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	return readPartyLoadingSnapshot(ctx, &r.cache.mu, r.cache.characters, id, r.CharacterRepository.Load, dnfrepo.CloneCharacter)
}
func (r partyLoadingCharacterRepository) Save(ctx context.Context, row dnfrepo.CharacterRecord) error {
	r.cache.invalidate()
	err := r.CharacterRepository.Save(ctx, row)
	r.cache.invalidate()
	return err
}

type partyLoadingEquipmentRepository struct {
	dnfrepo.EquipmentRepository
	cache *partyLoadingSnapshots
}

func (r partyLoadingEquipmentRepository) Load(ctx context.Context, id string) (dnfrepo.EquipmentRecord, bool, error) {
	return readPartyLoadingSnapshot(ctx, &r.cache.mu, r.cache.equipment, id, r.EquipmentRepository.Load, dnfrepo.CloneEquipment)
}
func (r partyLoadingEquipmentRepository) Save(ctx context.Context, row dnfrepo.EquipmentRecord) error {
	r.cache.invalidate()
	err := r.EquipmentRepository.Save(ctx, row)
	r.cache.invalidate()
	return err
}
func (c *partyLoadingSnapshots) wrap(repos dnfrepo.Group) dnfrepo.Group {
	if repos.Character != nil {
		repos.Character = partyLoadingCharacterRepository{repos.Character, c}
	}
	if repos.Equipment != nil {
		repos.Equipment = partyLoadingEquipmentRepository{repos.Equipment, c}
	}
	return repos
}

func (s *Service) withPartyLoadingSnapshot(ctx context.Context, session *gameSession) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Value(partyLoadingSnapshotKey{}) != nil || session == nil {
		return ctx
	}
	cache := session.partyLoadingSnapshots.Load()
	if cache != nil && cache.owners[session] != session.dungeonPlacementEpoch.Load() {
		return ctx
	}
	if cache == nil {
		return ctx
	}
	return context.WithValue(ctx, partyLoadingSnapshotKey{}, cache)
}

func (s *Service) preloadPartyLoadingSnapshots(cache *partyLoadingSnapshots, owners map[uint16]partyDungeonLoadingOwner) {
	repos, ok := s.repositoryGroup()
	if !ok {
		return
	}
	repos = cache.wrap(repos)
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for id := range owners {
		wg.Add(1)
		go func(id uint16) {
			defer wg.Done()
			key := strconv.Itoa(int(id))
			if repos.Character != nil {
				_, _, _ = repos.Character.Load(ctx, key)
			}
			if repos.Equipment != nil {
				_, _, _ = repos.Equipment.Load(ctx, key)
			}
		}(id)
	}
	wg.Wait()
}

func (s *Service) finishPartyLoadingSnapshot(partyID uint16, cycle *partyDungeonLoadingCycle, id uint16) {
	s.partyDungeonLoadingMu.Lock()
	defer s.partyDungeonLoadingMu.Unlock()
	if s.partyDungeonLoadingCycles[partyID] != cycle {
		return
	}
	if cycle.snapshotFinished == nil {
		cycle.snapshotFinished = make(map[uint16]bool)
	}
	cycle.snapshotFinished[id] = true
	for member := range cycle.preparing {
		if !cycle.snapshotFinished[member] {
			return
		}
	}
	if cache := cycle.snapshots; cache != nil {
		for owner := range cache.owners {
			owner.partyLoadingSnapshots.CompareAndSwap(cache, nil)
		}
	}
	cycle.snapshots = nil
}

func (r partyLoadingCharacterRepository) LoadCharacterMirrorStats(ctx context.Context, id string, keys ...string) (dnfrepo.CharacterMirrorStats, bool, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return dnfrepo.CharacterMirrorStats{}, false, err
		}
	}
	r.cache.mu.Lock()
	entry := r.cache.characters[id]
	r.cache.mu.Unlock()
	if entry != nil {
		select {
		case <-entry.done:
			return dnfrepo.SelectCharacterMirrorStats(entry.record, keys...), entry.found, entry.err
		default:
		}
	}
	return dnfrepo.LoadCharacterMirrorStats(ctx, r.CharacterRepository, id, keys...)
}

func (r partyLoadingCharacterRepository) LoadCharacterExtraStats(ctx context.Context, id string, keys ...string) (dnfrepo.CharacterMirrorStats, bool, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return dnfrepo.CharacterMirrorStats{}, false, err
		}
	}
	r.cache.mu.Lock()
	entry := r.cache.characters[id]
	r.cache.mu.Unlock()
	if entry != nil {
		select {
		case <-entry.done:
			return dnfrepo.SelectCharacterMirrorStats(entry.record, keys...), entry.found, entry.err
		default:
		}
	}
	return dnfrepo.LoadCharacterExtraStats(ctx, r.CharacterRepository, id, keys...)
}
