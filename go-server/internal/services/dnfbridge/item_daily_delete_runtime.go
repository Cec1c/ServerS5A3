package dnfbridge

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type currentDailyDeleteSlot struct {
	list byte
	slot int16
}

// Capture actual removed keys inside the owner transaction. No clear packet is
// emitted if projection or persistence fails; live containers use op14 instead
// of replacing their complete object managers with a login snapshot.
func (s *Service) reconcileCurrentDailyDeleteWithRemoved(ctx context.Context, repositories dnfrepo.Group, characterID, accountID string, catalog *pvfDungeonDropCatalog) (itemexpiration.Summary, []currentDailyDeleteSlot, error) {
	if catalog == nil {
		return itemexpiration.Summary{}, nil, errDungeonDropSourceRequired
	}
	owner, err := itemexpiration.NewOwner(repositories)
	if err != nil {
		return itemexpiration.Summary{}, nil, err
	}
	now := s.gameplayNow().UTC()
	var removed []currentDailyDeleteSlot
	summary, err := owner.Reconcile(ctx, itemexpiration.Command{
		AccountID: accountID, CharacterID: characterID, UpdatedAt: now,
		Project: func(assets *itemexpiration.Assets) (itemexpiration.Summary, error) {
			worn := make(map[string]int16)
			if assets.Equipment != nil {
				for key, entry := range assets.Equipment.Entries {
					if actorSlot, valid := currentEXEActorEquipmentSlot(entry); valid && actorSlot >= 12 && actorSlot <= 26 {
						worn[key] = int16(actorSlot)
					}
				}
			}
			containers := make([]map[string]dnfrepo.ItemStack, 0, 3)
			if assets.Inventory != nil {
				containers = append(containers, assets.Inventory.Slots, assets.Inventory.Warehouse)
			}
			if assets.AccountInventory != nil {
				containers = append(containers, assets.AccountInventory.Slots)
			}
			keys := make([][]string, len(containers))
			for i, container := range containers {
				for key := range container {
					keys[i] = append(keys[i], key)
				}
			}
			result, err := s.projectCurrentPVFItemExpirations(catalog, assets, now)
			if err != nil {
				return result, err
			}
			for key, slot := range worn {
				if _, exists := assets.Equipment.Entries[key]; exists {
					continue
				}
				removed = append(removed, currentDailyDeleteSlot{list: 3, slot: slot})
			}
			for i, container := range containers {
				for _, key := range keys[i] {
					if _, exists := container[key]; exists {
						continue
					}
					list, slot, ok := parseSceneInventorySlotKey(key)
					if !ok {
						return result, fmt.Errorf("daily delete invalid container key %q", key)
					}
					removed = append(removed, currentDailyDeleteSlot{list: list, slot: slot})
				}
			}
			return result, nil
		},
	})
	if err != nil {
		return summary, nil, err
	}
	sort.Slice(removed, func(i, j int) bool {
		if removed[i].list != removed[j].list {
			return removed[i].list < removed[j].list
		}
		return removed[i].slot < removed[j].slot
	})
	return summary, removed, nil
}

func (s *Service) sendCurrentDailyDeleteClearedSlots(session *gameSession, removed []currentDailyDeleteSlot) error {
	for start := 0; start < len(removed); {
		list := removed[start].list
		end := start
		var entries []currentItemListEntry
		for end < len(removed) && removed[end].list == list && len(entries) < math.MaxUint16 {
			var entry currentItemListEntry
			entry.patchCore(removed[end].slot, math.MaxUint32, 0)
			entries = append(entries, entry)
			end++
		}
		if err := s.sendGameUpperRawClass(session, 14, buildCurrentItemUpdateBody(list, entries), 0); err != nil {
			return err
		}
		start = end
	}
	return nil
}

func (s *Service) stopCurrentDailyDeleteClock(session *gameSession) {
	if s == nil || session == nil {
		return
	}
	s.ensureGameplayTimeQueue().Cancel("daily-delete:" + session.connID)
	session.pendingItemExpiryProjection = nil
}

func (s *Service) armCurrentDailyDeleteClock(session *gameSession) {
	if s == nil || session == nil {
		return
	}
	now := s.gameplayNow()
	deadline := itemexpiration.NextDailyDeleteAt(now)
	if next, err := s.nextCurrentRelativeItemExpiration(session, now); err != nil {
		if retry := now.Add(time.Minute); retry.Before(deadline) {
			deadline = retry
		}
	} else if !next.IsZero() && next.Before(deadline) {
		deadline = next
	}
	s.scheduleCurrentDailyDeleteClock(session, deadline.Sub(now))
}

func (s *Service) scheduleCurrentDailyDeleteClock(session *gameSession, delay time.Duration) {
	characterID, generation, err := gameSessionCharacterEventIdentity(session, session.selectedCharacterID)
	if err != nil {
		return
	}
	err = s.ensureGameplayTimeQueue().ScheduleAfter("daily-delete:"+session.connID, delay, func(time.Time) {
		err := s.postGameSessionCharacterEvent(session, "daily-delete", characterID, generation, func() error {
			if session.selectedCharacterID != characterID || session.characterGeneration != generation {
				return nil
			}
			if session.ctx != nil && session.ctx.Err() != nil {
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			defer cancel()
			repositories, ok := s.repositoryGroup()
			if !ok {
				s.scheduleCurrentDailyDeleteClock(session, time.Minute)
				return itemexpiration.ErrOwnerUnavailable
			}
			catalog, err := s.currentPVFItemCatalog()
			if err != nil {
				s.scheduleCurrentDailyDeleteClock(session, time.Minute)
				return err
			}
			summary, removed, err := s.reconcileCurrentDailyDeleteWithRemoved(ctx, repositories, strconv.Itoa(int(characterID)), s.accountIDForSession(session), catalog)
			if err != nil {
				s.scheduleCurrentDailyDeleteClock(session, time.Minute)
				return err
			}
			s.mergeCurrentExpiryProjection(session, characterID, generation, removed)
			if len(removed) > 0 {
				s.logGameEvent(session, "game-daily-delete-committed", "inventory", summary.DeletedInventory, "warehouse", summary.DeletedWarehouse, "account", summary.DeletedAccount)
			}
			if err := s.sendCurrentExpiryProjection(session); err != nil {
				s.scheduleCurrentDailyDeleteClock(session, time.Minute)
				return err
			}
			s.armCurrentDailyDeleteClock(session)
			return nil
		})
		if err != nil {
			s.logGameEvent(session, "game-daily-delete-deferred", "error", err)
		}
	})
	if err != nil {
		s.logGameEvent(session, "game-daily-delete-timer-deferred", "error", err)
	}
}
