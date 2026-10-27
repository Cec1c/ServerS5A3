package inventory

import (
	"fmt"
	"math"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// PlaceStackableReward stages a PVF-resolved reward without committing it. A
// full inventory leaves the input untouched, including partially filled stacks.
// The caller owns the transaction and its durable reward receipt.
func PlaceStackableReward(record repo.InventoryRecord, item alignedcmd.MagicBoxRewardItem, count int64) (repo.InventoryRecord, error) {
	if count <= 0 || count > math.MaxInt32 || item.StackLimit < 0 || item.StackLimit > math.MaxInt32 || item.SlotEnd > 32766 {
		return repo.InventoryRecord{}, fmt.Errorf("invalid stackable reward contract")
	}
	// An omitted PVF stack limit means unlimited in the existing grant owner;
	// retain that behavior with an explicit bound for the native count field.
	if item.StackLimit == 0 {
		item.StackLimit = math.MaxInt32
	}
	staged := repo.CloneInventory(record)
	if staged.Slots == nil {
		staged.Slots = map[string]repo.ItemStack{}
	}
	if _, err := grantRandomRewardItem(staged.Slots, item, count, record.ContainerState); err != nil {
		return repo.InventoryRecord{}, err
	}
	return staged, nil
}
