package vending

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"time"
)

type Command struct {
	AccountID, CharacterID string
	CoinID, CoinCount      int64
	CoinSlot               int16
	Reward                 dungeon.CardItemReward
	Project                dungeon.StackProjector
	// ValidateCoin checks PVF and instance deadlines on the locked source,
	// rather than trusting the bridge's earlier inventory snapshot.
	ValidateCoin func(repo.ItemStack, time.Time) error
	Now          time.Time
}
type Result struct {
	CoinRemaining int64
	RewardSlot    int16
}

// A failed grant rolls back the coin debit. Entitlement, character ownership,
// source quantity and inventory capacity are checked against real records.
func Use(ctx context.Context, repos repo.Group, cmd Command) (Result, error) {
	if repos.CharacterAssets == nil || repos.Account == nil || cmd.CoinID <= 0 || cmd.CoinCount <= 0 || cmd.CoinSlot < 0 || cmd.Now.IsZero() {
		return Result{}, fmt.Errorf("vending dependencies or command invalid")
	}
	var result Result
	err := repos.CharacterAssets.WithinCharacterAssets(ctx, cmd.CharacterID, func(characters repo.CharacterRepository, items repo.InventoryRepository, _ repo.EquipmentRepository) error {
		account, found, e := repos.Account.Load(ctx, cmd.AccountID)
		if e != nil {
			return e
		}
		if !found || !premium.Active(account, premium.TypeBlackDiamond, cmd.Now) {
			return fmt.Errorf("black diamond required")
		}
		character, found, e := characters.Load(ctx, cmd.CharacterID)
		if e != nil {
			return e
		}
		if !found || character.AccountID != cmd.AccountID {
			return fmt.Errorf("vending character ownership invalid")
		}
		inventory, found, e := items.Load(ctx, cmd.CharacterID)
		if e != nil {
			return e
		}
		if !found {
			return fmt.Errorf("vending inventory missing")
		}
		inventory = repo.CloneInventory(inventory)
		key := "0:" + strconv.Itoa(int(cmd.CoinSlot))
		coin, found := inventory.Slots[key]
		if !found || coin.ItemID != cmd.CoinID || coin.Count < cmd.CoinCount {
			return fmt.Errorf("vending coin unavailable")
		}
		if cmd.ValidateCoin != nil {
			if e := cmd.ValidateCoin(coin, cmd.Now); e != nil {
				return e
			}
		} else if !coin.ExpireAt.IsZero() && !cmd.Now.Before(coin.ExpireAt) {
			return fmt.Errorf("vending coin expired")
		}
		coin.Count -= cmd.CoinCount
		result.CoinRemaining = coin.Count
		if coin.Count == 0 {
			delete(inventory.Slots, key)
		} else {
			inventory.Slots[key] = coin
		}
		result.RewardSlot, e = dungeon.AddResolvedRewardToInventory(&inventory, cmd.Reward, cmd.Project)
		if e != nil {
			return e
		}
		inventory.UpdatedAt = cmd.Now
		return repo.SaveInventoryFields(ctx, items, inventory, repo.InventoryFieldSlots)
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}
