package buffswap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"strings"
)

const instanceKey = "native_buff_instance"

func itemInstances(ctx context.Context, repos repo.Group, characterID string) (map[location]string, error) {
	out := map[location]string{}
	inv, _, err := repos.Inventory.Load(ctx, characterID)
	if err != nil {
		return nil, err
	}
	for key, stack := range inv.Slots {
		parts := strings.Split(key, ":")
		if len(parts) != 2 {
			continue
		}
		list, e1 := strconv.ParseUint(parts[0], 10, 8)
		index, e2 := strconv.ParseUint(parts[1], 10, 16)
		if e1 == nil && e2 == nil && (list < 3 || list == 7) && index < 0x8000 {
			out[location{byte(list), uint16(index)}] = stack.Extra[instanceKey]
		}
	}
	worn, _, err := repos.Equipment.Load(ctx, characterID)
	if err != nil {
		return nil, err
	}
	for key, entry := range worn.Entries {
		index, e := strconv.ParseUint(key, 10, 16)
		if e == nil && index < SlotCount {
			out[location{3, uint16(index)}] = entry.Extra[instanceKey]
		}
	}
	return out, nil
}

// The identity is allocated only for an explicitly selected real item in the
// same transaction as its registration. Existing ambiguous bindings are not
// assigned an identity, and item quality/raw bytes are never identity input.
func registerInstance(ctx context.Context, repos repo.Group, characterID string, list byte, index uint16, itemID int64) (string, error) {
	identities, err := itemInstances(ctx, repos, characterID)
	if err != nil {
		return "", err
	}
	loc := location{list, index}
	instance := identities[loc]
	matches := 0
	if instance != "" {
		for _, v := range identities {
			if v == instance {
				matches++
			}
		}
	}
	if matches > 1 {
		return "", fmt.Errorf("buff instance ambiguous")
	}
	if instance == "" {
		for {
			var raw [16]byte
			if _, err := rand.Read(raw[:]); err != nil {
				return "", err
			}
			instance = hex.EncodeToString(raw[:])
			collision := false
			for _, v := range identities {
				collision = collision || v == instance
			}
			if !collision {
				break
			}
		}
	}
	if list == 3 {
		worn, found, err := repos.Equipment.Load(ctx, characterID)
		if err != nil {
			return "", err
		}
		key := strconv.Itoa(int(index))
		entry, exists := worn.Entries[key]
		if !found || !exists || entry.ItemID != itemID {
			return "", ErrInvalid
		}
		entry = repo.CloneEquipment(repo.EquipmentRecord{Entries: map[string]repo.EquipmentEntry{"x": entry}}).Entries["x"]
		if entry.Extra == nil {
			entry.Extra = map[string]string{}
		}
		entry.Extra[instanceKey] = instance
		worn.Entries[key] = entry
		if err := repos.Equipment.Save(ctx, worn); err != nil {
			return "", err
		}
	} else {
		inv, found, err := repos.Inventory.Load(ctx, characterID)
		if err != nil {
			return "", err
		}
		key := fmt.Sprintf("%d:%d", list, index)
		stack, exists := inv.Slots[key]
		if !found || !exists || stack.ItemID != itemID {
			return "", ErrInvalid
		}
		_, _, stackable := repo.StackContract(stack)
		if stackable || strings.EqualFold(strings.TrimSpace(stack.Extra["item_kind"]), "stackable") ||
			(stack.Count != 1 && !(list == 1 && stack.Count == 0)) {
			return "", fmt.Errorf("buff registration requires one non-stackable item")
		}
		stack = repo.CloneInventory(repo.InventoryRecord{Slots: map[string]repo.ItemStack{"x": stack}}).Slots["x"]
		if stack.Extra == nil {
			stack.Extra = map[string]string{}
		}
		stack.Extra[instanceKey] = instance
		inv.Slots[key] = stack
		if err := repos.Inventory.Save(ctx, inv); err != nil {
			return "", err
		}
	}
	return instance, nil
}
