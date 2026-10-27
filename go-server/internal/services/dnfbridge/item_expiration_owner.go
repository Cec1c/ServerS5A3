package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type currentPVFExpirationReconcileSummary = itemexpiration.Summary

func (s *Service) currentPVFDefinitionForItem(catalog *pvfDungeonDropCatalog, itemID uint32) (dungeonDropItemDefinition, error) {
	if catalog == nil || itemID == 0 {
		return dungeonDropItemDefinition{}, nil
	}
	definition, err := catalog.ResolveItem(itemID)
	if errors.Is(err, errDungeonDropItemUnresolved) {
		return dungeonDropItemDefinition{}, nil
	}
	return definition, err
}

func (s *Service) reconcileCurrentPVFItemExpirations(
	ctx context.Context,
	repositories dnfrepo.Group,
	characterID string,
	accountID string,
) (currentPVFExpirationReconcileSummary, error) {
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return currentPVFExpirationReconcileSummary{}, err
	}
	return s.reconcileCurrentPVFItemExpirationsWithCatalog(ctx, repositories, characterID, accountID, catalog)
}

func (s *Service) reconcileCurrentPVFItemExpirationsWithCatalog(
	ctx context.Context,
	repositories dnfrepo.Group,
	characterID string,
	accountID string,
	catalog *pvfDungeonDropCatalog,
) (currentPVFExpirationReconcileSummary, error) {
	if catalog == nil {
		return currentPVFExpirationReconcileSummary{}, errDungeonDropSourceRequired
	}
	owner, err := itemexpiration.NewOwner(repositories)
	if err != nil {
		return currentPVFExpirationReconcileSummary{}, err
	}
	now := s.gameplayNow().UTC()
	return owner.Reconcile(ctx, itemexpiration.Command{
		AccountID:   accountID,
		CharacterID: characterID,
		UpdatedAt:   now,
		Project: func(assets *itemexpiration.Assets) (itemexpiration.Summary, error) {
			return s.projectCurrentPVFItemExpirations(catalog, assets, now)
		},
	})
}

func (s *Service) projectCurrentPVFItemExpirations(
	catalog *pvfDungeonDropCatalog,
	assets *itemexpiration.Assets,
	now time.Time,
) (itemexpiration.Summary, error) {
	var summary itemexpiration.Summary
	if assets == nil {
		return summary, itemexpiration.ErrProjectorRequired
	}
	var petErr error
	summary, petErr = s.removeExpiredPetState(catalog, assets, now)
	if petErr != nil {
		return summary, petErr
	}
	var errs []error
	normalizeStack := func(scope string, stack dnfrepo.ItemStack) (dnfrepo.ItemStack, bool, bool) {
		definition, resolveErr := s.currentPVFDefinitionForItem(catalog, sceneInventoryUint32FromInt64(stack.ItemID))
		if resolveErr != nil {
			errs = append(errs, fmt.Errorf("resolve %s item=%d: %w", scope, stack.ItemID, resolveErr))
			patched, changed := cleanupCurrentPVFWrongExpirationProjection(stack)
			return patched, changed, false
		}
		if definition.DailyDeleteItem && (definition.Kind != dungeonDropItemEquipment || currentPVFDailyEquipmentGrantRecorded(stack, definition)) {
			_, _, expired, err := itemexpiration.ReconcileDailyDelete(stack, now)
			if err != nil {
				errs = append(errs, fmt.Errorf("resolve daily delete %s: %w", scope, err))
				return stack, false, false
			}
			if expired {
				return stack, true, true
			}
		}
		if currentPVFRelativeGrantExpired(stack, definition, now) {
			return stack, true, true
		}
		patched, changed := normalizeCurrentPVFItemStack(stack, definition, now)
		return patched, changed, false
	}

	if assets.Inventory != nil {
		for key, stack := range assets.Inventory.Slots {
			if patched, changed, expired := normalizeStack("inventory key="+key, stack); changed {
				if expired {
					delete(assets.Inventory.Slots, key)
					summary.DeletedInventory++
				} else {
					assets.Inventory.Slots[key] = patched
				}
				summary.Inventory++
			}
		}
		for key, stack := range assets.Inventory.Warehouse {
			if patched, changed, expired := normalizeStack("warehouse key="+key, stack); changed {
				if expired {
					delete(assets.Inventory.Warehouse, key)
					summary.DeletedWarehouse++
				} else {
					assets.Inventory.Warehouse[key] = patched
				}
				summary.Warehouse++
			}
		}
	}
	if assets.AccountInventory != nil {
		for key, stack := range assets.AccountInventory.Slots {
			if patched, changed, expired := normalizeStack("account inventory key="+key, stack); changed {
				if expired {
					delete(assets.AccountInventory.Slots, key)
					summary.DeletedAccount++
				} else {
					assets.AccountInventory.Slots[key] = patched
				}
				summary.Account++
			}
		}
	}
	if assets.Equipment != nil {
		for key, entry := range assets.Equipment.Entries {
			definition, resolveErr := s.currentPVFDefinitionForItem(catalog, sceneInventoryUint32FromInt64(entry.ItemID))
			if resolveErr != nil {
				errs = append(errs, fmt.Errorf("resolve equipment key=%s item=%d: %w", key, entry.ItemID, resolveErr))
				patched, changed := normalizeCurrentPVFEquipmentEntry(entry, dungeonDropItemDefinition{}, now)
				if changed {
					assets.Equipment.Entries[key] = patched
					summary.Equipment++
				}
				continue
			}
			if avatarSlot, matched := currentPVFRelativeAvatarEquipmentExpiry(definition, entry, now); matched {
				if key != strconv.Itoa(int(entry.SlotIndex)) || entry.SlotIndex != avatarSlot {
					return summary, fmt.Errorf("item expiry invalid avatar key %q slot %d", key, entry.SlotIndex)
				}
				delete(assets.Equipment.Entries, key)
				summary.Equipment++
				continue
			}
			stack := dnfrepo.ItemStack{ItemID: entry.ItemID, ExpireAt: entry.ExpireAt, Extra: entry.Extra}
			if currentPVFRelativeGrantExpired(stack, definition, now) ||
				(currentPVFDailyEquipmentGrantRecorded(stack, definition) && !stack.ExpireAt.After(now)) {
				actorSlot, valid := currentEXEActorEquipmentSlot(entry)
				if key != strconv.Itoa(int(entry.SlotIndex)) || !valid || actorSlot < 12 || actorSlot > 25 {
					return summary, fmt.Errorf("item expiry invalid worn key %q slot %d", key, entry.SlotIndex)
				}
				delete(assets.Equipment.Entries, key)
				summary.Equipment++
				continue
			}
			if patched, changed := normalizeCurrentPVFEquipmentEntry(entry, definition, now); changed {
				assets.Equipment.Entries[key] = patched
				summary.Equipment++
			}
		}
	}
	return summary, errors.Join(errs...)
}
