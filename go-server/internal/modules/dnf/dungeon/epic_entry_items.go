package dungeon

import (
	repo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"time"
)

// EpicEntryItems selects the first affordable PVF alternative. Only usable
// main-inventory stacks count; account storage and expired stacks cannot pay.
func EpicEntryItems(inventory repo.InventoryRecord, alternatives [][]EntryItemRequirement, now time.Time) ([]EntryItemRequirement, error) {
	if now.IsZero() || len(alternatives) == 0 {
		return nil, ErrEntryItemConsumeInvalid
	}
	counts := map[int64]int64{}
	for key, stack := range inventory.Slots {
		if _, ok := parseMainInventorySlotKey(key); !ok || stack.Count <= 0 || !stack.ExpireAt.IsZero() && !stack.ExpireAt.After(now) {
			continue
		}
		if stack.Count > math.MaxInt64-counts[stack.ItemID] {
			return nil, ErrEntryItemConsumeInvalid
		}
		counts[stack.ItemID] += stack.Count
	}
	for _, items := range alternatives {
		if len(items) == 0 {
			return nil, ErrEntryItemConsumeInvalid
		}
		need := map[int64]int64{}
		valid := true
		for _, item := range items {
			if item.ItemID <= 0 || item.Count <= 0 || !item.ConsumeOnEntry || item.Count > math.MaxInt64-need[item.ItemID] {
				return nil, ErrEntryItemConsumeInvalid
			}
			need[item.ItemID] += item.Count
		}
		for id, n := range need {
			if counts[id] < n {
				valid = false
			}
		}
		if valid {
			return append([]EntryItemRequirement(nil), items...), nil
		}
	}
	return nil, ErrEntryItemConsumeInsufficient
}

func consumeEpicEntryItems(inventory *repo.InventoryRecord, alternatives [][]EntryItemRequirement, now time.Time, project EntryItemProjector) ([]EntryItemConsumedStack, error) {
	items, err := EpicEntryItems(*inventory, alternatives, now)
	if err != nil {
		return nil, err
	}
	usable := repo.CloneInventory(*inventory)
	for key, stack := range usable.Slots {
		if !stack.ExpireAt.IsZero() && !stack.ExpireAt.After(now) {
			delete(usable.Slots, key)
		}
	}
	consumed := make([]EntryItemConsumedStack, 0, len(items))
	for _, item := range items {
		slots, err := consumeEntryItemRequirement(&usable, item, project)
		if err != nil {
			return nil, err
		}
		consumed = append(consumed, EntryItemConsumedStack{ItemID: item.ItemID, Count: item.Count, Slots: slots})
	}
	for _, item := range consumed {
		for _, slot := range item.Slots {
			if slot.Removed {
				delete(inventory.Slots, slot.SlotKey)
			} else {
				inventory.Slots[slot.SlotKey] = usable.Slots[slot.SlotKey]
			}
		}
	}
	return consumed, nil
}
