package buffswap

import (
	"context"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"strings"
)

// These private repositories own detached values. They perform no warehouse
// I/O and cannot save. Resolve/items may retain existing current identity
// semantics without re-entering a scoped repository or altering lock order.
func (view ownedSourceSnapshot) readOnlyGroup(characterID string) repo.Group {
	return repo.Group{
		Inventory: ownedInventoryView{characterID, view.inventory, view.inventoryFound},
		Equipment: ownedEquipmentView{characterID, view.equipment, view.equipmentFound},
	}
}

type ownedInventoryView struct {
	characterID string
	record      repo.InventoryRecord
	found       bool
}

func (v ownedInventoryView) Load(ctx context.Context, id string) (repo.InventoryRecord, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return repo.InventoryRecord{}, false, err
	}
	if id != v.characterID {
		return repo.InventoryRecord{}, false, repo.ErrBuffLegacyOwnerMismatch
	}
	return repo.CloneInventory(v.record), v.found, nil
}
func (ownedInventoryView) Save(context.Context, repo.InventoryRecord) error {
	return fmt.Errorf("%w: detached buff inventory view is read-only", ErrInvalid)
}

type ownedEquipmentView struct {
	characterID string
	record      repo.EquipmentRecord
	found       bool
}

func (v ownedEquipmentView) Load(ctx context.Context, id string) (repo.EquipmentRecord, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return repo.EquipmentRecord{}, false, err
	}
	if id != v.characterID {
		return repo.EquipmentRecord{}, false, repo.ErrBuffLegacyOwnerMismatch
	}
	return repo.CloneEquipment(v.record), v.found, nil
}
func (ownedEquipmentView) Save(context.Context, repo.EquipmentRecord) error {
	return fmt.Errorf("%w: detached buff equipment view is read-only", ErrInvalid)
}

// Match the existing items/itemInstances parser, including leading-zero keys.
// Multiple physical keys at one parsed location are ambiguous and omitted;
// do not pick a map iteration winner or substitute another instance.
func (view ownedSourceSnapshot) stackForBinding(b Binding) (repo.ItemStack, bool) {
	var result repo.ItemStack
	matches := 0
	for key, item := range view.inventory.Slots {
		parts := strings.Split(key, ":")
		if len(parts) != 2 {
			continue
		}
		list, e1 := strconv.ParseUint(parts[0], 10, 8)
		index, e2 := strconv.ParseUint(parts[1], 10, 16)
		if e1 != nil || e2 != nil || !(list <= 2 || list == 7) || index >= 0x8000 || byte(list) != b.List || uint16(index) != b.Index {
			continue
		}
		matches++
		result = item
	}
	if matches != 1 || result.ItemID != b.ItemID || (b.Instance != "" && result.Extra[instanceKey] != b.Instance) {
		return repo.ItemStack{}, false
	}
	return repo.CloneInventory(repo.InventoryRecord{Slots: map[string]repo.ItemStack{"row": result}}).Slots["row"], true
}
func (view ownedSourceSnapshot) equipmentForBinding(b Binding) (repo.EquipmentEntry, bool) {
	var result repo.EquipmentEntry
	matches := 0
	for key, item := range view.equipment.Entries {
		index, err := strconv.ParseUint(key, 10, 16)
		if err != nil || index >= SlotCount || uint16(index) != b.Index {
			continue
		}
		matches++
		result = item
	}
	if matches != 1 || result.ItemID != b.ItemID || (b.Instance != "" && result.Extra[instanceKey] != b.Instance) {
		return repo.EquipmentEntry{}, false
	}
	return repo.CloneEquipment(repo.EquipmentRecord{Entries: map[string]repo.EquipmentEntry{"row": result}}).Entries["row"], true
}
