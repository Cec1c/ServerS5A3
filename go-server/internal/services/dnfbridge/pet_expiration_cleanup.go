package dnfbridge

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Read the current slot on retries; never clear a newly equipped replacement.
func (s *Service) sendCurrentExpiredPetWornSlot(session *gameSession) error {
	repos, ok := s.repositoryGroup()
	if !ok || repos.Equipment == nil {
		return fmt.Errorf("pet expiry equipment unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	record, found, err := repos.Equipment.Load(ctx, strconv.Itoa(int(session.selectedCharacterID)))
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("pet expiry equipment missing")
	}
	if _, worn := currentEquippedCreatureEquipmentEntry(record); worn {
		return s.sendSelectedEquippedCreatureItemRefresh(session, "relative_expiration_retry")
	}
	var entry currentItemListEntry
	entry.patchCore(26, math.MaxUint32, 0)
	return s.sendGameUpperRawClass(session, 14, buildCurrentItemUpdateBody(3, []currentItemListEntry{entry}), 0)
}

// A hatched instance retains its egg's deadline. Verify the real hatch mapping
// before using that egg's expiry rule; a marker alone is not a deletion grant.
func (s *Service) currentPetExpirationDefinition(catalog *pvfDungeonDropCatalog, stack dnfrepo.ItemStack) (dungeonDropItemDefinition, error) {
	def, err := s.currentPVFDefinitionForItem(catalog, sceneInventoryUint32FromInt64(stack.ItemID))
	if err != nil || stack.Extra["hatched_from_item_id"] == "" {
		return def, err
	}
	eggID, err := strconv.ParseInt(stack.Extra["hatched_from_item_id"], 10, 64)
	if err != nil || eggID <= 0 {
		return def, fmt.Errorf("invalid pet hatch expiry source")
	}
	pets, err := s.currentPetPVFCatalog()
	if err != nil {
		return def, err
	}
	hatch, err := pets.ResolveHatch(eggID)
	if err != nil {
		return def, err
	}
	if hatch.HatchedItemID != stack.ItemID {
		return def, fmt.Errorf("pet hatch expiry template mismatch")
	}
	return s.currentPVFDefinitionForItem(catalog, sceneInventoryUint32FromInt64(eggID))
}

func (s *Service) removeExpiredPetState(catalog *pvfDungeonDropCatalog, assets *itemexpiration.Assets, now time.Time) (itemexpiration.Summary, error) {
	var summary itemexpiration.Summary
	remove := func(stack dnfrepo.ItemStack) error {
		if !assets.PetsAvailable {
			return fmt.Errorf("pet expiry requires atomic pet assets")
		}
		if assets.Pets == nil {
			return nil
		}
		serial, err := strconv.ParseUint(stack.Extra["creature_serial_or_handle"], 10, 32)
		if err != nil || serial == 0 {
			return fmt.Errorf("pet expiry missing instance serial")
		}
		key := strconv.FormatUint(serial, 10)
		entry, found := assets.Pets.Entries[key]
		if !found {
			return nil
		}
		if entry.PetKey != key || uint64(entry.CreatureKey) != serial || entry.ItemID != stack.ItemID {
			return fmt.Errorf("pet expiry durable identity mismatch")
		}
		delete(assets.Pets.Entries, key)
		if assets.Pets.EquippedKey == key {
			assets.Pets.EquippedKey = ""
		}
		summary.Pets++
		return nil
	}
	expired := func(stack dnfrepo.ItemStack) (bool, error) {
		if stack.Extra["expiration_source"] != "runtime_pvf_usable_period_grant" || stack.ExpireAt.IsZero() || stack.ExpireAt.After(now) {
			return false, nil
		}
		def, err := s.currentPetExpirationDefinition(catalog, stack)
		if err != nil {
			return false, err
		}
		return currentPVFRelativeGrantExpired(stack, def, now), nil
	}
	if assets.Inventory != nil {
		for key, stack := range assets.Inventory.Slots {
			list, slot, ok := parseSceneInventorySlotKey(key)
			if !ok || list != 7 || slot < 0 {
				continue
			}
			due, err := expired(stack)
			if err != nil {
				return summary, err
			}
			if !due {
				continue
			}
			// Unhatched eggs have no durable creature row and need no serial.
			if stack.Extra["hatched_from_item_id"] != "" || stack.Extra["creature_serial_or_handle"] != "" {
				if err := remove(stack); err != nil {
					return summary, err
				}
			}
			delete(assets.Inventory.Slots, key)
			summary.Inventory++
			summary.DeletedInventory++
		}
	}
	if assets.Equipment != nil {
		for key, entry := range assets.Equipment.Entries {
			slot, valid := currentEXEActorEquipmentSlot(entry)
			if !valid || slot != 26 {
				continue
			}
			stack := dnfrepo.ItemStack{ItemID: entry.ItemID, ExpireAt: entry.ExpireAt, Extra: entry.Extra}
			due, err := expired(stack)
			if err != nil {
				return summary, err
			}
			if !due {
				continue
			}
			if key != strconv.Itoa(int(entry.SlotIndex)) {
				return summary, fmt.Errorf("pet expiry worn slot mismatch")
			}
			if err := remove(stack); err != nil {
				return summary, err
			}
			delete(assets.Equipment.Entries, key)
			summary.Equipment++
		}
	}
	return summary, nil
}
