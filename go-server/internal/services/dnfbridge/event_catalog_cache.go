package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"sync"

	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type archiveCatalogFlight[T any] struct {
	archive *platformpvf.Archive
	done    chan struct{}
	value   T
	err     error
}

// archiveCatalogCache caches a validated PVF-derived catalog scoped to a
// Service and the exact *platformpvf.Archive pointer. Concurrent cold requests
// on the same archive coalesce into one load while never holding
// Service.initialEquipmentMu during PVF parsing. Errors and canceled loads are
// never cached.
type archiveCatalogCache[T any] struct {
	mu      sync.Mutex
	archive *platformpvf.Archive
	hasVal  bool
	value   T
	flight  *archiveCatalogFlight[T]
}

func (c *archiveCatalogCache[T]) get(
	ctx context.Context,
	archive *platformpvf.Archive,
	load func(context.Context, *platformpvf.Archive) (T, error),
) (T, error) {
	var zero T
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if archive == nil {
		return zero, onlineevent.ErrOwnerUnavailable
	}
	for {
		c.mu.Lock()
		if c.hasVal && c.archive == archive {
			val := c.value
			c.mu.Unlock()
			if err := ctx.Err(); err != nil {
				return zero, err
			}
			return val, nil
		}
		if flight := c.flight; flight != nil {
			sameArchive := flight.archive == archive
			c.mu.Unlock()
			select {
			case <-flight.done:
			case <-ctx.Done():
				return zero, ctx.Err()
			}
			if err := ctx.Err(); err != nil {
				return zero, err
			}
			if !sameArchive {
				continue
			}
			if flight.err == nil {
				return flight.value, nil
			}
			if errors.Is(flight.err, context.Canceled) || errors.Is(flight.err, context.DeadlineExceeded) {
				continue
			}
			return zero, flight.err
		}
		flight := &archiveCatalogFlight[T]{
			archive: archive,
			done:    make(chan struct{}),
		}
		c.flight = flight
		c.mu.Unlock()

		val, err := func() (T, error) {
			defer func() {
				if r := recover(); r != nil {
					c.mu.Lock()
					if c.flight == flight {
						c.flight = nil
					}
					flight.err = fmt.Errorf("catalog load panic: %v", r)
					close(flight.done)
					c.mu.Unlock()
					panic(r)
				}
			}()
			return load(ctx, archive)
		}()

		if err == nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				err = ctxErr
			}
		}
		c.mu.Lock()
		if err == nil {
			c.archive = archive
			c.value = val
			c.hasVal = true
			flight.value = val
		} else {
			flight.err = err
		}
		if c.flight == flight {
			c.flight = nil
		}
		close(flight.done)
		c.mu.Unlock()
		if err != nil {
			return zero, err
		}
		return val, nil
	}
}

type returneeGiftRulesFlight struct {
	archive *platformpvf.Archive
	done    chan struct{}
	value   onlineevent.ReturneeGiftRules
	err     error
}

// returneeGiftRulesCache caches static PVF gift rules keyed by exact archive
// pointer and NPC ID. Archive replacement drops prior entries; errors and
// canceled loads are never cached.
type returneeGiftRulesCache struct {
	mu      sync.Mutex
	archive *platformpvf.Archive
	entries map[uint32]onlineevent.ReturneeGiftRules
	flights map[uint32]*returneeGiftRulesFlight
}

func (c *returneeGiftRulesCache) get(
	ctx context.Context,
	archive *platformpvf.Archive,
	npc uint32,
	load func(*platformpvf.Archive, uint32) (onlineevent.ReturneeGiftRules, error),
) (onlineevent.ReturneeGiftRules, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return onlineevent.ReturneeGiftRules{}, err
	}
	if archive == nil {
		return onlineevent.ReturneeGiftRules{}, onlineevent.ErrOwnerUnavailable
	}
	for {
		c.mu.Lock()
		if c.archive != archive {
			c.archive = archive
			c.entries = nil
			c.flights = nil
		}
		if rules, ok := c.entries[npc]; ok {
			c.mu.Unlock()
			if err := ctx.Err(); err != nil {
				return onlineevent.ReturneeGiftRules{}, err
			}
			return cloneReturneeGiftRules(rules), nil
		}
		if flight := c.flights[npc]; flight != nil {
			c.mu.Unlock()
			select {
			case <-flight.done:
			case <-ctx.Done():
				return onlineevent.ReturneeGiftRules{}, ctx.Err()
			}
			if err := ctx.Err(); err != nil {
				return onlineevent.ReturneeGiftRules{}, err
			}
			if flight.archive != archive {
				continue
			}
			if flight.err == nil {
				return cloneReturneeGiftRules(flight.value), nil
			}
			if errors.Is(flight.err, context.Canceled) || errors.Is(flight.err, context.DeadlineExceeded) {
				continue
			}
			return onlineevent.ReturneeGiftRules{}, flight.err
		}
		flight := &returneeGiftRulesFlight{
			archive: archive,
			done:    make(chan struct{}),
		}
		if c.flights == nil {
			c.flights = make(map[uint32]*returneeGiftRulesFlight, 4)
		}
		c.flights[npc] = flight
		c.mu.Unlock()

		rules, err := func() (onlineevent.ReturneeGiftRules, error) {
			defer func() {
				if r := recover(); r != nil {
					c.mu.Lock()
					if c.archive == archive && c.flights[npc] == flight {
						delete(c.flights, npc)
					}
					flight.err = fmt.Errorf("returnee gift rules load panic: %v", r)
					close(flight.done)
					c.mu.Unlock()
					panic(r)
				}
			}()
			return load(archive, npc)
		}()

		if err == nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				err = ctxErr
			}
		}
		c.mu.Lock()
		if err == nil {
			stored := cloneReturneeGiftRules(rules)
			flight.value = stored
			if c.archive == archive {
				if c.entries == nil {
					c.entries = make(map[uint32]onlineevent.ReturneeGiftRules, 4)
				}
				c.entries[npc] = stored
			}
		} else {
			flight.err = err
		}
		if c.archive == archive && c.flights[npc] == flight {
			delete(c.flights, npc)
		}
		close(flight.done)
		c.mu.Unlock()
		if err != nil {
			return onlineevent.ReturneeGiftRules{}, err
		}
		return cloneReturneeGiftRules(rules), nil
	}
}

func cloneSemesterCatalog(cat *onlineevent.SemesterCatalog) *onlineevent.SemesterCatalog {
	if cat == nil {
		return nil
	}
	copied := *cat
	return &copied
}

func cloneEpicCatalog(cat *onlineevent.EpicCatalog) *onlineevent.EpicCatalog {
	if cat == nil {
		return nil
	}
	copied := &onlineevent.EpicCatalog{
		Thresholds: cat.Thresholds,
		Rewards:    cat.Rewards,
	}
	if cat.EntryAlternatives != nil {
		copied.EntryAlternatives = make(map[int64][][]onlineevent.AttendancePVFItemReward, len(cat.EntryAlternatives))
		for dungeonID, groups := range cat.EntryAlternatives {
			if groups == nil {
				copied.EntryAlternatives[dungeonID] = nil
				continue
			}
			copiedGroups := make([][]onlineevent.AttendancePVFItemReward, len(groups))
			for i, group := range groups {
				if group == nil {
					continue
				}
				copiedGroup := make([]onlineevent.AttendancePVFItemReward, len(group))
				copy(copiedGroup, group)
				copiedGroups[i] = copiedGroup
			}
			copied.EntryAlternatives[dungeonID] = copiedGroups
		}
	}
	return copied
}

func cloneReturneeGiftRules(rules onlineevent.ReturneeGiftRules) onlineevent.ReturneeGiftRules {
	copied := rules
	if rules.Points != nil {
		copied.Points = make(map[int64][2]int64, len(rules.Points))
		for k, v := range rules.Points {
			copied.Points[k] = v
		}
	}
	return copied
}
