package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"

	guild "longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

// 261 and NOTI237 consume capacity u32, count u16, then 119-byte item rows.
// Resolution uses the actual PVF container family, never a blanket list-0 row.
func buildCurrentGuildCargoSnapshotBody(cargo *guild.Cargo, limit uint32, catalog *pvfDungeonDropCatalog) ([]byte, error) {
	if cargo == nil {
		return nil, guild.ErrCargoUninitialized
	}
	if err := guild.ValidateCargo(cargo); err != nil {
		return nil, err
	}
	if limit == 0 || limit > math.MaxInt16+1 || cargo.Capacity > limit || len(cargo.Slots) > math.MaxUint16 || catalog == nil {
		return nil, fmt.Errorf("invalid guild cargo snapshot contract")
	}
	slots := make([]int, 0, len(cargo.Slots))
	for slot := range cargo.Slots {
		slots = append(slots, int(slot))
	}
	sort.Ints(slots)
	body := make([]byte, 6, 6+119*len(slots))
	binary.LittleEndian.PutUint32(body, cargo.Capacity)
	binary.LittleEndian.PutUint16(body[4:], uint16(len(slots)))
	for _, slot := range slots {
		item := cargo.Slots[uint16(slot)]
		if item.ItemID == math.MaxUint32 || item.Count > math.MaxInt32 {
			return nil, fmt.Errorf("invalid cargo instance")
		}
		def, err := catalog.ResolveItem(uint32(item.ItemID))
		if err != nil {
			return nil, err
		}
		entry, err := currentGuildCargoItemEntry(uint16(slot), item, def)
		if err != nil {
			return nil, err
		}
		body = append(body, entry.data[:]...)
	}
	return body, nil
}

func currentGuildCargoItemEntry(slot uint16, stack r.ItemStack, def dungeonDropItemDefinition) (currentItemListEntry, error) {
	if def.ItemID != uint32(stack.ItemID) || (def.Kind != dungeonDropItemEquipment && def.Kind != dungeonDropItemStackable) {
		return currentItemListEntry{}, fmt.Errorf("unresolved cargo item family")
	}
	container := currentPVFItemTargetContainer(def)
	// Native eligibility excludes these equipment types. Their side-channel
	// avatar/pet records cannot be represented by this warehouse snapshot.
	if container.Avatar || container.PetBody {
		return currentItemListEntry{}, fmt.Errorf("unsupported cargo instance family")
	}
	entry := currentItemListEntryFromStack(container.ListType, int16(slot), stack)
	if def.Kind == dungeonDropItemStackable {
		binary.LittleEndian.PutUint32(entry.data[6:10], uint32(stack.Count))
	} else {
		if stack.Count != 1 {
			return currentItemListEntry{}, fmt.Errorf("equipment cargo count must be one")
		}
		// Native 261 copies +0x3c dwords from the row into an 8-byte array.
		if entry.data[0x3c] > 2 {
			return currentItemListEntry{}, fmt.Errorf("cargo socket vector exceeds native array")
		}
		if seed := currentEquipmentQualitySeedFromStack(stack); seed != 0 {
			binary.LittleEndian.PutUint32(entry.data[6:10], seed)
		}
	}
	return entry, nil
}
