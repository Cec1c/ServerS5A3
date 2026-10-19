package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/inventory"
	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) grantCurrentPVPTotalTargets(ctx context.Context, repos repo.Group, catalog *dnfpvp.Catalog, session *gameSession) (dnfpvp.TotalRewards, error) {
	place := func(record repo.InventoryRecord, id, count int64, now time.Time) (repo.InventoryRecord, error) {
		items, err := s.currentPVFItemCatalog()
		if err != nil {
			return repo.InventoryRecord{}, err
		}
		definition, err := items.ResolveItem(uint32(id))
		if err != nil {
			return repo.InventoryRecord{}, err
		}
		definition, err = currentPVFItemDefinitionForGrantAt(definition, now)
		if err != nil {
			return repo.InventoryRecord{}, err
		}
		if definition.Kind != dungeonDropItemStackable {
			return repo.InventoryRecord{}, fmt.Errorf("total target item %d is not stackable", id)
		}
		state := s.loadCurrentItemListContainerState(ctx, repos.Settings, record.CharacterID, 0)
		record.ContainerState = &state
		result, err := inventory.PlaceStackableReward(record, alignedcmd.MagicBoxRewardItem{
			ItemID: id, Kind: string(definition.Kind), StackLimit: definition.StackLimit,
			SlotStart: definition.SlotStart, SlotEnd: definition.SlotEnd, PVFPath: definition.PVFPath,
			ExpireAt: definition.ExpirationDate, UsablePeriodDays: definition.UsablePeriodDays,
		}, count)
		if errors.Is(err, inventory.ErrRandomRewardInventoryFull) {
			return repo.InventoryRecord{}, dnfpvp.ErrTotalRewardInventoryFull
		}
		if err != nil {
			return repo.InventoryRecord{}, fmt.Errorf("total target reward item=%d: %w", id, err)
		}
		return result, err
	}
	result, err := dnfpvp.GrantTotalTargets(ctx, repos, catalog, session.accountID, strconv.Itoa(int(session.selectedCharacterID)), s.gameplayNow(), place)
	if err == nil && (len(result.Granted) > 0 || len(result.Pending) > 0) {
		s.logGameEvent(session, "game-pvp-total-target-rewards", "granted", result.Granted, "pending_inventory_space", result.Pending)
	}
	return result, err
}

func (s *Service) sendCurrentPVPTotalRewards(session *gameSession, rewards dnfpvp.TotalRewards) error {
	entries := []currentItemListEntry{currentPVPEmblemWalletEntry(statU32(rewards.Character, dnfpvp.EmblemsKey, 0))}
	for slot, stack := range rewards.Items {
		entries = append(entries, currentItemListEntryFromStack(0, slot, stack))
	}
	sortCurrentItemListEntries(entries)
	return s.sendGameUpperRawClass(session, 14, buildCurrentItemUpdateBody(0, entries), 0)
}
