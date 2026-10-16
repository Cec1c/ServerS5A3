package characterdata

import (
	"context"
	"fmt"
	"math"
	"time"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const RenameCardSlotStat = "character_rename_pending_card_slot"

type RenameCardPolicy struct {
	MinimumLevel       int64
	CooldownSeconds    int64
	ReservationSeconds int64
	Slots              itemplacement.SlotPolicy
}

// Resolver validates the actual held item against authoritative PVF data and
// returns the existing expiration projection. It cannot authorize another slot.
type RenameCardResolver func(repo.CharacterRecord, repo.ItemStack) (RenameCardPolicy, repo.ItemStack, error)

type RenameCardUseResult struct {
	Changed bool
	Removed bool
	Stack   repo.ItemStack
}

func UseRenameCard(ctx context.Context, g repo.Group, accountID, characterID string, slot int16, now time.Time, resolve RenameCardResolver) (RenameCardUseResult, error) {
	if g.CeraShopAssets == nil || resolve == nil || accountID == "" || characterID == "" || now.IsZero() || slot < 0 || repo.IsAccountSharedInventorySlot(repo.MainInventoryListType, slot) {
		return RenameCardUseResult{}, ErrRenameUnavailable
	}
	var result RenameCardUseResult
	err := g.CeraShopAssets.WithinCeraShopAssets(ctx, accountID, characterID, repo.CharacterContainerStateScope(characterID), func(accounts repo.AccountRepository, characters repo.CharacterRepository, inventories repo.InventoryRepository, _ repo.EquipmentRepository, settings repo.SettingsRepository) error {
		account, found, err := accounts.Load(ctx, accountID)
		if err != nil {
			return err
		}
		if !found || account.AccountID != accountID {
			return ErrRenameUnavailable
		}
		c, found, err := characters.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || c.AccountID != accountID || c.Stats["delete_flag"] != 0 || c.Location.DungeonID != 0 {
			return ErrRenameUnavailable
		}
		if c.Stats[RenameEntitlementStat] == 1 {
			if c.Stats[RenameCardSlotStat] != int64(slot) {
				return ErrRenameUnavailable
			}
			return nil
		}
		if c.Stats[RenameEntitlementStat] != 0 || c.Stats[RenameNextAllowedUnixStat] > now.Unix() {
			return ErrRenameUnavailable
		}
		bag, found, err := inventories.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || bag.CharacterID != characterID {
			return ErrRenameUnavailable
		}
		if err := repo.BindInventoryContainerState(ctx, settings, &bag); err != nil {
			return err
		}
		key := fmt.Sprintf("0:%d", slot)
		held, found := bag.Slots[key]
		if !found || held.ItemID <= 0 || held.ItemID > math.MaxUint32 || held.Count < 1 || held.Count > math.MaxUint32 {
			return ErrRenameUnavailable
		}
		policy, projected, err := resolve(c, held)
		if err != nil {
			return err
		}
		if projected.ItemID != held.ItemID || projected.Count != held.Count || policy.MinimumLevel < 1 || int64(c.Level) < policy.MinimumLevel || !policy.Slots.ForInventory(&bag).Contains(repo.MainInventoryListType, slot) ||
			(policy.CooldownSeconds != 0 && policy.CooldownSeconds != int64(RenameCooldown/time.Second)) || (policy.ReservationSeconds != 0 && policy.ReservationSeconds != int64(PreviousNameReservation/time.Second)) {
			return ErrRenameUnavailable
		}
		projected.Count--
		if projected.Count == 0 {
			delete(bag.Slots, key)
		} else {
			bag.Slots[key] = projected
		}
		if c.Stats == nil {
			c.Stats = make(map[string]int64)
		}
		c.Stats[RenameEntitlementStat] = 1
		c.Stats[RenameCardSlotStat] = int64(slot)
		c.Stats[RenameMinimumLevelStat] = policy.MinimumLevel
		c.Stats[RenameCooldownSecondsStat] = policy.CooldownSeconds
		c.Stats[RenameReservationSecondsStat] = policy.ReservationSeconds
		c.UpdatedAt = now
		bag.UpdatedAt = now
		if err := repo.SaveCharacterFields(ctx, characters, c, repo.CharacterFieldStats); err != nil {
			return err
		}
		if err := repo.SaveInventoryFields(ctx, inventories, bag, repo.InventoryFieldSlots); err != nil {
			return err
		}
		result = RenameCardUseResult{Changed: true, Removed: projected.Count == 0, Stack: projected}
		return nil
	})
	if err != nil {
		return RenameCardUseResult{}, err
	}
	return result, nil
}
