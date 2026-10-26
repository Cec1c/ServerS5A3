package buffswap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// migrateCurrentInstanceBindings upgrades legacy coordinate/template bindings
// only when the owned current repositories contain exactly one eligible item
// with that template. A second same-template instance makes the historical
// choice unknowable, so the binding is left byte-for-byte unchanged. The
// allocated identity is written to the item and the registration in this same
// owned-snapshot transaction; subsequent moves are then followed by instance.
func migrateCurrentInstanceBindings(ctx context.Context, tx repo.Group, characterID string, state *State, view *ownedSourceSnapshot) (bool, error) {
	if state == nil || state.WireProfile != CurrentWireProfile || len(state.Slots) == 0 {
		return false, nil
	}
	needsMigration := false
	for _, binding := range state.Slots {
		if binding.Instance == "" && binding.SourceKind != SourceLegacyUID {
			needsMigration = true
			break
		}
	}
	if !needsMigration {
		return false, nil
	}
	readOnly := view.readOnlyGroup(characterID)
	byLocation, err := items(ctx, readOnly, characterID)
	if err != nil {
		return false, err
	}
	identities, err := itemInstances(ctx, readOnly, characterID)
	if err != nil {
		return false, err
	}
	byTemplate := make(map[int64][]location)
	byInstance := make(map[string]int)
	inUse := make(map[string]bool)
	for loc, itemID := range byLocation {
		byTemplate[itemID] = append(byTemplate[itemID], loc)
		if instance := identities[loc]; instance != "" {
			byInstance[instance]++
			inUse[instance] = true
		}
	}

	changed, inventoryDirty, equipmentDirty := false, false, false
	for buffSlot, binding := range state.Slots {
		if binding.Instance != "" || binding.SourceKind == SourceLegacyUID {
			continue
		}
		candidates := byTemplate[binding.ItemID]
		if len(candidates) != 1 {
			continue
		}
		loc := candidates[0]
		instance := identities[loc]
		if instance != "" && byInstance[instance] > 1 {
			continue
		}
		if instance == "" {
			instance, err = allocateInstanceID(inUse)
			if err != nil {
				return false, err
			}
			if err := setOwnedInstance(view, loc, binding.ItemID, instance); err != nil {
				return false, err
			}
			if loc.list == 3 {
				equipmentDirty = true
			} else {
				inventoryDirty = true
			}
			inUse[instance] = true
			byInstance[instance] = 1
			identities[loc] = instance
		}
		binding.List = loc.list
		binding.Index = loc.index
		binding.Instance = instance
		binding.SourceKind = SourceCurrentInstance
		state.Slots[buffSlot] = binding
		changed = true
	}
	if !changed {
		return false, nil
	}
	if inventoryDirty {
		if err := repo.SaveInventoryFields(ctx, tx.Inventory, view.inventory, repo.InventoryFieldSlots); err != nil {
			return false, err
		}
	}
	if equipmentDirty {
		if err := repo.SaveEquipmentFields(ctx, tx.Equipment, view.equipment, repo.EquipmentFieldEntries); err != nil {
			return false, err
		}
	}
	raw, err := json.Marshal(*state)
	if err != nil {
		return false, err
	}
	record, found, err := tx.Settings.Load(ctx, Scope(characterID))
	if err != nil {
		return false, err
	}
	if !found {
		return false, fmt.Errorf("%w: registration vanished during identity migration", repo.ErrBuffOwnedSnapshotUnavailable)
	}
	values := make(map[string]string, len(record.Values)+1)
	for key, value := range record.Values {
		values[key] = value
	}
	values["state"] = string(raw)
	if strings.TrimSpace(values["source"]) == "" {
		values["source"] = "current_exe_994D20"
	}
	record.Scope = Scope(characterID)
	record.Values = values
	record.UpdatedAt = time.Now().UTC()
	if err := tx.Settings.Save(ctx, record); err != nil {
		return false, err
	}
	return true, nil
}

func allocateInstanceID(inUse map[string]bool) (string, error) {
	for {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return "", err
		}
		instance := hex.EncodeToString(raw[:])
		if !inUse[instance] {
			return instance, nil
		}
	}
}

// setOwnedInstance changes only the detached owned view. The caller persists
// both item containers after every binding has been migrated, keeping the
// registration and item metadata in one transaction.
func setOwnedInstance(view *ownedSourceSnapshot, loc location, itemID int64, instance string) error {
	if loc.list == 3 {
		matches := 0
		var key string
		var entry repo.EquipmentEntry
		for entryKey, candidate := range view.equipment.Entries {
			index, err := strconv.ParseUint(entryKey, 10, 16)
			if err != nil || index >= SlotCount || uint16(index) != loc.index {
				continue
			}
			matches++
			key, entry = entryKey, candidate
		}
		if matches != 1 || entry.ItemID != itemID {
			return fmt.Errorf("%w: equipment instance source is ambiguous", ErrInvalid)
		}
		if entry.Extra == nil {
			entry.Extra = make(map[string]string, 1)
		}
		entry.Extra[instanceKey] = instance
		view.equipment.Entries[key] = entry
		return nil
	}
	matches := 0
	var key string
	var stack repo.ItemStack
	for entryKey, candidate := range view.inventory.Slots {
		list, index, ok := repo.ParseCurrentInventoryLocation(entryKey)
		if !ok || list != loc.list || uint16(index) != loc.index {
			continue
		}
		matches++
		key, stack = entryKey, candidate
	}
	if matches != 1 || stack.ItemID != itemID {
		return fmt.Errorf("%w: inventory instance source is ambiguous", ErrInvalid)
	}
	if stack.Extra == nil {
		stack.Extra = make(map[string]string, 1)
	}
	stack.Extra[instanceKey] = instance
	view.inventory.Slots[key] = stack
	return nil
}
