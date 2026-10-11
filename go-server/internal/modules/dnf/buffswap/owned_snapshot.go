package buffswap

import (
	"context"
	"fmt"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

const SourceCurrentInstance = "current_instance"
const SourceLegacyUID = "legacy_uid"

// This result contains detached values only. No scoped repository can escape
// its transaction; callers encode/send after successful completion.
type OwnedProjection struct {
	Owner                repo.CharacterRecord
	State                State
	Found                bool
	Omitted              int
	Items                map[byte]OwnedProjectedItem
	PetMetadata          repo.PetRecord
	PetMetadataAvailable bool
}

type OwnedProjectedItem struct {
	Binding             Binding
	CurrentStack        repo.ItemStack
	HasCurrentStack     bool
	CurrentEquipment    repo.EquipmentEntry
	HasCurrentEquipment bool
	LegacyItem          repo.OwnedLegacyItem
	HasLegacyItem       bool
}

type OwnedProjectionOptions struct {
	// Must be a pure metadata predicate, never I/O or a packet sender.
	NeedsEquippedPetMetadata func(repo.EquipmentEntry) bool
	legacyLists              []byte
}

func validateBindingIdentity(b Binding) error {
	switch b.SourceKind {
	case "":
		if b.LegacyItemUID != 0 {
			return ErrInvalid
		}
	case SourceCurrentInstance:
		if b.Instance == "" || b.LegacyItemUID != 0 {
			return ErrInvalid
		}
	case SourceLegacyUID:
		if b.Instance != "" || b.LegacyItemUID <= 0 || (b.List != 0 && b.List != 1 && b.List != 7) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

type ownedSourceSnapshot struct {
	inventoryFound    bool
	equipmentFound    bool
	inventory         repo.InventoryRecord
	equipment         repo.EquipmentRecord
	currentContainers map[byte]bool
	legacy            map[location]repo.OwnedLegacyItem
	pet               repo.PetRecord
	petAvailable      bool
}

func readOwnedSources(ctx context.Context, tx repo.Group, characterID string, options OwnedProjectionOptions) (ownedSourceSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if tx.Inventory == nil || tx.Equipment == nil {
		return ownedSourceSnapshot{}, repo.ErrBuffOwnedSnapshotUnavailable
	}
	inventory, inventoryFound, err := tx.Inventory.Load(ctx, characterID)
	if err != nil {
		return ownedSourceSnapshot{}, err
	}
	if inventoryFound && inventory.CharacterID != characterID {
		return ownedSourceSnapshot{}, fmt.Errorf("%w: inventory returned another character", repo.ErrBuffLegacyOwnerMismatch)
	}
	if !inventoryFound {
		// A missing repository record cannot lend any payload to this owner.
		inventory = repo.InventoryRecord{CharacterID: characterID}
	}
	equipment, equipmentFound, err := tx.Equipment.Load(ctx, characterID)
	if err != nil {
		return ownedSourceSnapshot{}, err
	}
	if equipmentFound && equipment.CharacterID != characterID {
		return ownedSourceSnapshot{}, fmt.Errorf("%w: equipment returned another character", repo.ErrBuffLegacyOwnerMismatch)
	}
	if !equipmentFound {
		equipment = repo.EquipmentRecord{CharacterID: characterID}
	}
	view := ownedSourceSnapshot{inventoryFound: inventoryFound, equipmentFound: equipmentFound, inventory: repo.CloneInventory(inventory), equipment: repo.CloneEquipment(equipment), currentContainers: map[byte]bool{}, legacy: map[location]repo.OwnedLegacyItem{}}
	for key, item := range inventory.Slots {
		list, _, valid := repo.ParseCurrentInventoryLocation(key)
		if valid && (list == 0 || list == 1 || list == 7) && repo.CurrentInventoryItemProjects(key, item, list) {
			view.currentContainers[list] = true
		}
	}
	if options.NeedsEquippedPetMetadata != nil && tx.BuffPetMetadata != nil {
		needed := false
		for _, item := range equipment.Entries {
			if options.NeedsEquippedPetMetadata(item) {
				needed = true
				break
			}
		}
		if needed {
			value, found, err := tx.BuffPetMetadata.LoadBuffPetEntries(ctx, characterID)
			if err != nil {
				return ownedSourceSnapshot{}, err
			}
			if found && value.CharacterID == characterID {
				view.pet = repo.ClonePet(value)
				view.petAvailable = true
			}
		}
	}
	if tx.OwnedLegacyInventory != nil {
		lists := []byte{}
		for _, list := range options.legacyLists {
			if (list == 0 || list == 1 || list == 7) && !view.currentContainers[list] {
				lists = append(lists, list)
			}
		}
		if len(lists) > 0 {
			rows, err := tx.OwnedLegacyInventory.SelectOwnedBuffItems(ctx, characterID, lists)
			if err != nil {
				return ownedSourceSnapshot{}, err
			}
			seen := map[int64]bool{}
			for _, row := range rows {
				if row.OwnerScope != "character" || row.OwnerID != characterID || row.CharacterID != characterID || row.ItemUID <= 0 || row.Item.SlotIndex < 0 || view.currentContainers[row.Item.ListType] || (row.Item.ListType != 0 && row.Item.ListType != 1 && row.Item.ListType != 7) {
					return ownedSourceSnapshot{}, repo.ErrBuffLegacyOwnerMismatch
				}
				loc := location{row.Item.ListType, uint16(row.Item.SlotIndex)}
				if _, exists := view.legacy[loc]; exists || seen[row.ItemUID] {
					return ownedSourceSnapshot{}, repo.ErrBuffLegacyAmbiguous
				}
				seen[row.ItemUID] = true
				view.legacy[loc] = repo.CloneOwnedLegacyItem(row)
			}
		}
	}
	return view, nil
}

func legacyBindingEligible(row repo.OwnedLegacyItem) bool {
	switch row.Item.ListType {
	case 0:
		return row.ItemKind == "equipment" && row.Item.StackCount == 1 && row.Item.ItemTemplateID > 0
	case 1:
		return row.ItemKind == "avatar" && (row.Item.StackCount == 0 || row.Item.StackCount == 1) && row.Item.ItemTemplateID > 0
	case 7:
		return row.ItemKind == "pet" && row.Item.StackCount == 1 && row.Item.ItemTemplateID > 0
	}
	return false
}

func registerOwnedBinding(ctx context.Context, tx repo.Group, characterID string, list byte, index uint16) (Binding, error) {
	view, err := readOwnedSources(ctx, tx, characterID, OwnedProjectionOptions{legacyLists: []byte{list}})
	if err != nil {
		return Binding{}, err
	}
	if (list == 0 || list == 1 || list == 7) && !view.currentContainers[list] {
		if tx.OwnedLegacyInventory == nil {
			return Binding{}, fmt.Errorf("%w: legacy capability unavailable", repo.ErrBuffOwnedSnapshotUnavailable)
		}
		row, found := view.legacy[location{list, index}]
		if !found || !legacyBindingEligible(row) {
			return Binding{}, fmt.Errorf("%w: owned legacy source missing or unsupported", ErrInvalid)
		}
		return Binding{List: list, Index: index, ItemID: row.Item.ItemTemplateID, SourceKind: SourceLegacyUID, LegacyItemUID: row.ItemUID}, nil
	}
	all, err := items(ctx, view.readOnlyGroup(characterID), characterID)
	if err != nil {
		return Binding{}, err
	}
	id := all[location{list, index}]
	if id <= 0 {
		return Binding{}, fmt.Errorf("%w: source item missing", ErrInvalid)
	}
	// Existing instance registration deliberately re-reads the already locked
	// current repositories and may save identity metadata. It is not a pure
	// projection; cold resolution below never performs these transaction reads.
	instance, err := registerInstance(ctx, tx, characterID, list, index, id)
	if err != nil {
		return Binding{}, err
	}
	return Binding{List: list, Index: index, ItemID: id, Instance: instance, SourceKind: SourceCurrentInstance}, nil
}

func LoadResolvedOwned(ctx context.Context, base repo.Group, characterID, accountID string) (OwnedProjection, error) {
	return LoadResolvedOwnedWithOptions(ctx, base, characterID, accountID, OwnedProjectionOptions{})
}

func LoadResolvedOwnedWithOptions(ctx context.Context, base repo.Group, characterID, accountID string, options OwnedProjectionOptions) (OwnedProjection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var result OwnedProjection
	err := repo.WithinBuffOwnedSnapshot(ctx, base, characterID, accountID, func(tx repo.Group) error {
		owner, ownerFound, err := tx.Character.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !ownerFound || owner.CharacterID != characterID || owner.AccountID != accountID {
			return repo.ErrBuffLegacyOwnerMismatch
		}
		result.Owner = repo.CloneCharacter(owner)
		state, found, err := Load(ctx, tx, characterID, accountID)
		if err != nil {
			return err
		}
		result.Found = found
		if !found {
			result.State = State{WireProfile: CurrentWireProfile}
			return nil
		}
		// Skill-only/cleared states need no item container/Legacy/Pet dependency.
		if len(state.Slots) == 0 {
			result.State = state
			return nil
		}
		// Only an explicit Legacy binding needs the optional old mirror. Current
		// bindings never start unrelated Legacy reads during cold recovery.
		options.legacyLists = nil
		for _, binding := range state.Slots {
			if binding.SourceKind == SourceLegacyUID {
				options.legacyLists = []byte{0, 1, 7}
				break
			}
		}
		view, err := readOwnedSources(ctx, tx, characterID, options)
		if err != nil {
			return err
		}
		projection, err := resolveOwnedProjection(ctx, tx, characterID, state, view)
		projection.Owner = result.Owner
		result = projection
		return err
	})
	if err != nil {
		return OwnedProjection{}, err
	}
	return result, nil
}

func resolveOwnedProjection(ctx context.Context, tx repo.Group, characterID string, state State, view ownedSourceSnapshot) (OwnedProjection, error) {
	current := State{WireProfile: state.WireProfile, Skill: state.Skill, Slots: map[byte]Binding{}}
	for slot, b := range state.Slots {
		if b.SourceKind != SourceLegacyUID {
			current.Slots[slot] = b
		}
	}
	resolved, _, err := Resolve(ctx, view.readOnlyGroup(characterID), characterID, current)
	if err != nil {
		return OwnedProjection{}, err
	}
	result := OwnedProjection{State: resolved, Found: true, Items: map[byte]OwnedProjectedItem{}, PetMetadata: repo.ClonePet(view.pet), PetMetadataAvailable: view.petAvailable}
	for slot, b := range resolved.Slots {
		projected := OwnedProjectedItem{Binding: b}
		if b.List == 3 {
			value, ok := view.equipmentForBinding(b)
			if !ok || value.ItemID != b.ItemID {
				delete(result.State.Slots, slot)
				continue
			}
			projected.CurrentEquipment = value
			projected.HasCurrentEquipment = true
		} else {
			value, ok := view.stackForBinding(b)
			if !ok || value.ItemID != b.ItemID {
				delete(result.State.Slots, slot)
				continue
			}
			projected.CurrentStack = value
			projected.HasCurrentStack = true
		}
		result.Items[slot] = projected
	}
	for slot, b := range state.Slots {
		if b.SourceKind != SourceLegacyUID {
			continue
		}
		if tx.OwnedLegacyInventory == nil {
			return OwnedProjection{}, repo.ErrBuffOwnedSnapshotUnavailable
		}
		matches := 0
		var row repo.OwnedLegacyItem
		for _, item := range view.legacy {
			if item.ItemUID == b.LegacyItemUID && item.Item.ItemTemplateID == b.ItemID && legacyBindingEligible(item) {
				row = item
				matches++
			}
		}
		if matches != 1 {
			continue
		}
		b.List, b.Index = row.Item.ListType, uint16(row.Item.SlotIndex)
		if state.WireProfile != CurrentWireProfile && b.List == 7 {
			continue
		}
		result.State.Slots[slot] = b
		result.Items[slot] = OwnedProjectedItem{Binding: b, LegacyItem: repo.CloneOwnedLegacyItem(row), HasLegacyItem: true}
	}
	result.Omitted = len(state.Slots) - len(result.State.Slots)
	return result, nil
}
