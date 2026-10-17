package dungeon

import (
	"context"
	"fmt"
	"math"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const griefRewardReceiptPrefix = "dungeon_settlement_grief_"

// GriefRewards keeps all ten PVF card positions, including empty cards.
// Only nonempty cards are granted; their display positions never collapse.
type GriefRewards struct {
	Items   [GriefRewardSlots]CardItemReward
	Project StackProjector
}

type GriefGrantedItem struct {
	ItemID, Quantity uint32
	Slot             int16
}

func commitGriefRewards(ctx context.Context, settings dnfrepo.SettingsRepository, tx dnfrepo.Group, character *dnfrepo.CharacterRecord, inventory *dnfrepo.InventoryRecord, floor int, rewards *GriefRewards, now time.Time) ([]GriefGrantedItem, error) {
	if rewards == nil {
		return nil, nil
	}
	if floor < 1 || floor > griefMaximumFloor {
		return nil, ErrRewardInvalid
	}
	if err := dnfrepo.BindInventoryContainerState(ctx, settings, inventory); err != nil {
		return nil, err
	}
	if inventory.Slots == nil {
		inventory.Slots = make(map[string]dnfrepo.ItemStack)
	}
	granted := make([]GriefGrantedItem, GriefRewardSlots)
	changed := false
	for i, reward := range rewards.Items {
		granted[i].Slot = -1
		if reward.Stack.ItemID == 0 && reward.Stack.Count == 0 {
			continue
		}
		if reward.Stack.ItemID <= 0 || reward.Stack.ItemID >= math.MaxUint32 || reward.Stack.Count <= 0 || reward.Stack.Count > math.MaxUint32 || !reward.Stackable || reward.Stack.Extra["item_kind"] == "avatar" {
			return nil, ErrRewardInvalid
		}
		if err := validateCardReward(CardRewardCommand{CharacterID: character.CharacterID, Bundle: CardRewardBundle{Items: []CardItemReward{reward}}, Project: rewards.Project}); err != nil {
			return nil, err
		}
		slot, err := AddResolvedRewardToInventory(inventory, reward, rewards.Project)
		if err != nil {
			return nil, err
		}
		granted[i] = GriefGrantedItem{ItemID: uint32(reward.Stack.ItemID), Quantity: uint32(reward.Stack.Count), Slot: slot}
		changed = true
	}
	if changed {
		inventory.UpdatedAt = now
		if err := dnfrepo.SaveInventoryFields(ctx, tx.Inventory, *inventory, dnfrepo.InventoryFieldSlots); err != nil {
			return nil, err
		}
	}
	character.Stats[griefRewardReceiptPrefix+"version"] = 1
	character.Stats[griefRewardReceiptPrefix+"floor"] = int64(floor)
	for i, item := range granted {
		character.Stats[fmt.Sprintf("%s%d_item", griefRewardReceiptPrefix, i)] = int64(item.ItemID)
		character.Stats[fmt.Sprintf("%s%d_count", griefRewardReceiptPrefix, i)] = int64(item.Quantity)
		character.Stats[fmt.Sprintf("%s%d_slot", griefRewardReceiptPrefix, i)] = int64(item.Slot)
	}
	return granted, nil
}

func readGriefRewardReceipt(stats map[string]int64, floor int, required bool) ([]GriefGrantedItem, error) {
	if !required {
		return nil, nil
	}
	if stats[griefRewardReceiptPrefix+"version"] != 1 || floor < 1 || floor > griefMaximumFloor || stats[griefRewardReceiptPrefix+"floor"] != int64(floor) {
		return nil, ErrSettlementStateInvalid
	}
	items := make([]GriefGrantedItem, GriefRewardSlots)
	for i := range items {
		id, hasID := stats[fmt.Sprintf("%s%d_item", griefRewardReceiptPrefix, i)]
		count, hasCount := stats[fmt.Sprintf("%s%d_count", griefRewardReceiptPrefix, i)]
		slot, hasSlot := stats[fmt.Sprintf("%s%d_slot", griefRewardReceiptPrefix, i)]
		if !hasID || !hasCount || !hasSlot {
			return nil, ErrSettlementStateInvalid
		}
		if id == 0 && count == 0 && slot == -1 {
			items[i].Slot = -1
			continue
		}
		if id <= 0 || id >= math.MaxUint32 || count <= 0 || count > math.MaxUint32 || slot < 3 || slot > math.MaxInt16 {
			return nil, ErrSettlementStateInvalid
		}
		items[i] = GriefGrantedItem{uint32(id), uint32(count), int16(slot)}
	}
	return items, nil
}
