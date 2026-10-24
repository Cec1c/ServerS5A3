package dungeon

import (
	"context"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

// Item definitions are resolved before entering the transaction; quantities
// come exclusively from the frozen PVF catalog under the registry lock.
type GuildDungeonClear struct {
	Entry   guild.DungeonEntry
	Catalog *guild.DungeonCatalog
	Items   map[uint32]CardItemReward
	Project StackProjector
}

type GuildDungeonGrantedItem struct {
	ItemID, Quantity uint32
	Slot             int16
}

func commitGuildDungeonClear(ctx context.Context, settings r.SettingsRepository, tx r.Group, character *r.CharacterRecord, inventory *r.InventoryRecord, clear *GuildDungeonClear, now time.Time) (guild.DungeonClearResult, []GuildDungeonGrantedItem, error) {
	if clear == nil {
		return guild.DungeonClearResult{}, nil, nil
	}
	reward, result, err := guild.ApplyDungeonClear(ctx, tx.Settings, clear.Catalog, clear.Entry, now)
	if err != nil {
		return guild.DungeonClearResult{}, nil, err
	}
	if err = r.BindInventoryContainerState(ctx, settings, inventory); err != nil {
		return guild.DungeonClearResult{}, nil, err
	}
	if inventory.Slots == nil {
		inventory.Slots = map[string]r.ItemStack{}
	}
	var granted []GuildDungeonGrantedItem
	for _, item := range reward.Items {
		definition, ok := clear.Items[item.ItemID]
		if !ok || !definition.Stackable || definition.Stack.ItemID != int64(item.ItemID) || definition.Stack.Extra["item_kind"] == "avatar" {
			return guild.DungeonClearResult{}, nil, ErrRewardInvalid
		}
		definition.Stack.Count = int64(item.Count)
		if err = validateCardReward(CardRewardCommand{CharacterID: character.CharacterID, Bundle: CardRewardBundle{Items: []CardItemReward{definition}}, Project: clear.Project}); err != nil {
			return guild.DungeonClearResult{}, nil, err
		}
		slot, err := AddResolvedRewardToInventory(inventory, definition, clear.Project)
		if err != nil {
			return guild.DungeonClearResult{}, nil, err
		}
		granted = append(granted, GuildDungeonGrantedItem{ItemID: item.ItemID, Quantity: item.Count, Slot: slot})
	}
	if len(granted) > 0 {
		inventory.UpdatedAt = now
		if err = r.SaveInventoryFields(ctx, tx.Inventory, *inventory, r.InventoryFieldSlots); err != nil {
			return guild.DungeonClearResult{}, nil, err
		}
	}
	return result, granted, nil
}
