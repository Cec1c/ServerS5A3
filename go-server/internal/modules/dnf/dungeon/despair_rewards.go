package dungeon

import (
	"context"
	"fmt"
	"math"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const despairRewardReceiptPrefix = "dungeon_settlement_despair_"
const despairMaximumRewardItems = 10

// DespairRewards contains resolved PVF items frozen for one completion. It is
// applied within CommitSettlement's transaction, never by a separate award.
type DespairRewards struct {
	Items   []DespairRewardItem
	Project StackProjector
}

type DespairRewardItem struct {
	CardItemReward
	ListType byte
}

type DespairGrantedItem struct {
	ItemID   uint32
	Quantity uint32
	Slot     int16
	ListType byte
}

func commitDespairRewards(ctx context.Context, settings dnfrepo.SettingsRepository, tx dnfrepo.Group, character *dnfrepo.CharacterRecord, inventory *dnfrepo.InventoryRecord, floor int, rewards *DespairRewards, now time.Time) ([]DespairGrantedItem, error) {
	if rewards == nil {
		return nil, nil
	}
	if floor < 1 || floor > despairMaximumFloor || len(rewards.Items) > despairMaximumRewardItems {
		return nil, ErrRewardInvalid
	}
	if err := dnfrepo.BindInventoryContainerState(ctx, settings, inventory); err != nil {
		return nil, err
	}
	if inventory.Slots == nil {
		inventory.Slots = make(map[string]dnfrepo.ItemStack)
	}
	granted := make([]DespairGrantedItem, 0, len(rewards.Items))
	for _, reward := range rewards.Items {
		if reward.Stack.ItemID <= 0 || reward.Stack.ItemID > math.MaxUint32 || reward.Stack.Count <= 0 || reward.Stack.Count > math.MaxUint32 {
			return nil, ErrRewardInvalid
		}
		var slot int16
		var err error
		switch reward.ListType {
		case 0:
			if reward.Stack.Extra["item_kind"] == "avatar" {
				return nil, ErrRewardInvalid
			}
			if err = validateCardReward(CardRewardCommand{CharacterID: character.CharacterID, Bundle: CardRewardBundle{Items: []CardItemReward{reward.CardItemReward}}, Project: rewards.Project}); err != nil {
				return nil, err
			}
			slot, err = AddResolvedRewardToInventory(inventory, reward.CardItemReward, rewards.Project)
		case 1:
			if reward.Stackable || reward.Stack.Count != 1 || reward.Stack.Extra["item_kind"] != "avatar" {
				return nil, ErrRewardInvalid
			}
			slot = -1
			// Same list1 domain as the existing current-client avatar grants.
			for candidate := int16(0); candidate <= 500; candidate++ {
				key := fmt.Sprintf("1:%d", candidate)
				if _, occupied := inventory.Slots[key]; !occupied {
					slot = candidate
					inventory.Slots[key] = cloneStack(reward.Stack)
					break
				}
			}
			if slot < 0 {
				err = ErrInventoryFull
			}
		default:
			return nil, ErrRewardInvalid
		}
		if err != nil {
			return nil, err
		}
		granted = append(granted, DespairGrantedItem{ItemID: uint32(reward.Stack.ItemID), Quantity: uint32(reward.Stack.Count), Slot: slot, ListType: reward.ListType})
	}
	if len(granted) > 0 {
		inventory.UpdatedAt = now
		if err := dnfrepo.SaveInventoryFields(ctx, tx.Inventory, *inventory, dnfrepo.InventoryFieldSlots); err != nil {
			return nil, err
		}
	}
	writeDespairRewardReceipt(character.Stats, floor, granted)
	return granted, nil
}

func writeDespairRewardReceipt(stats map[string]int64, floor int, granted []DespairGrantedItem) {
	// Replace the previous completion's receipt, including unused old rows.
	for i := 0; i < despairMaximumRewardItems; i++ {
		for _, field := range []string{"item", "quantity", "slot", "list"} {
			delete(stats, fmt.Sprintf("%s%d_%s", despairRewardReceiptPrefix, i, field))
		}
	}
	stats[despairRewardReceiptPrefix+"version"] = 1
	stats[despairRewardReceiptPrefix+"floor"] = int64(floor)
	stats[despairRewardReceiptPrefix+"count"] = int64(len(granted))
	for i, item := range granted {
		stats[fmt.Sprintf("%s%d_item", despairRewardReceiptPrefix, i)] = int64(item.ItemID)
		stats[fmt.Sprintf("%s%d_quantity", despairRewardReceiptPrefix, i)] = int64(item.Quantity)
		stats[fmt.Sprintf("%s%d_slot", despairRewardReceiptPrefix, i)] = int64(item.Slot)
		stats[fmt.Sprintf("%s%d_list", despairRewardReceiptPrefix, i)] = int64(item.ListType)
	}
}

func readDespairRewardReceipt(stats map[string]int64, floor int, required bool) ([]DespairGrantedItem, error) {
	if !required {
		return nil, nil
	}
	version := stats[despairRewardReceiptPrefix+"version"]
	count, countPresent := stats[despairRewardReceiptPrefix+"count"]
	if version != 1 || floor < 1 || floor > 100 || stats[despairRewardReceiptPrefix+"floor"] != int64(floor) || !countPresent || count < 0 || count > despairMaximumRewardItems {
		return nil, ErrSettlementStateInvalid
	}
	granted := make([]DespairGrantedItem, 0, int(count))
	for i := 0; i < int(count); i++ {
		item := stats[fmt.Sprintf("%s%d_item", despairRewardReceiptPrefix, i)]
		quantity := stats[fmt.Sprintf("%s%d_quantity", despairRewardReceiptPrefix, i)]
		slot, slotPresent := stats[fmt.Sprintf("%s%d_slot", despairRewardReceiptPrefix, i)]
		list, listPresent := stats[fmt.Sprintf("%s%d_list", despairRewardReceiptPrefix, i)]
		if item <= 0 || item > math.MaxUint32 || quantity <= 0 || quantity > math.MaxUint32 || !slotPresent || !listPresent || slot < 0 || slot > math.MaxInt16 || (list != 0 && list != 1) || (list == 0 && slot < 3) || (list == 1 && slot > 500) {
			return nil, ErrSettlementStateInvalid
		}
		granted = append(granted, DespairGrantedItem{ItemID: uint32(item), Quantity: uint32(quantity), Slot: int16(slot), ListType: byte(list)})
	}
	return granted, nil
}
