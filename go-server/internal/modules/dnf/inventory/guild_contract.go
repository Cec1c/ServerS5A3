package inventory

import (
	"context"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/itemexpiration"
	r "longheng.io/server/internal/modules/dnf/repository"
)

type GuildContractClaim struct {
	Owner      r.GuildOwner
	RequestID  string
	Catalog    guild.ContentCatalog
	Reward     alignedcmd.MagicBoxRewardItem // Resolved from the matched runtime PVF.
	Revalidate func() bool
}

// ClaimGuildContract grants the daily PVF box and its permanent receipt in one
// guild/character/inventory transaction. A full bag or failed commit grants nothing.
func (o *Owner) ClaimGuildContract(ctx context.Context, req GuildContractClaim, now time.Time) (guild.ContractClaimReceipt, error) {
	var result guild.ContractClaimReceipt
	if o == nil || req.Revalidate == nil || req.Reward.ItemID != int64(req.Catalog.ContractRewardItemID) || req.Reward.Kind != "stackable" || req.Reward.StackLimit != 1 || req.Reward.TargetListType != r.MainInventoryListType || req.Catalog.ContractRewardCount != 1 {
		return result, guild.ErrInvalidContent
	}
	uow, ok := o.guild.(r.GuildInventoryUnitOfWork)
	if !ok {
		return result, r.ErrGuildTransaction
	}
	err := uow.WithinGuildInventory(ctx, req.Owner, func(tx r.Group) error {
		if !req.Revalidate() {
			return guild.ErrCharacterNotFound
		}
		actor, found, err := tx.Character.Load(ctx, req.Owner.CharacterID)
		if err != nil {
			return err
		}
		if !found || actor.AccountID != req.Owner.AccountID || actor.CharacterID != req.Owner.CharacterID {
			return guild.ErrCharacterNotFound
		}
		reg, err := guild.Load(ctx, tx.Settings)
		if err != nil {
			return err
		}
		claim, replayed, err := reg.RecordContractClaim(req.Owner, req.RequestID, req.Catalog, now)
		if err != nil {
			return err
		}
		if replayed {
			if !req.Revalidate() {
				return guild.ErrCharacterNotFound
			}
			result = claim
			return nil
		}
		bag, found, err := tx.Inventory.Load(ctx, req.Owner.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			return ErrInventoryNotFound
		}
		bag = r.CloneInventory(bag)
		if bag.Slots == nil {
			bag.Slots = make(map[string]r.ItemStack)
		}
		reward := req.Reward
		// [daily delete item] 1 is required by the bridge's PVF contract. The
		// deadline is calculated at commit, never extended by retry or relogin.
		reward.ExpireAt = claim.DayEnd
		slots, err := grantRandomRewardItem(bag.Slots, reward, 1, bag.ContainerState)
		if err != nil {
			return err
		}
		if len(slots) != 1 {
			return guild.ErrInvalidContent
		}
		key := slotKey(r.MainInventoryListType, slots[0])
		stack := bag.Slots[key]
		stack.Extra[itemexpiration.DailyDeleteDeadlineKey] = strconv.FormatInt(claim.DayEnd.Unix(), 10)
		bag.Slots[key] = stack
		bag.UpdatedAt = now
		claim.Slots = append([]int16(nil), slots...)
		reg.ContractClaims[req.RequestID] = claim
		if err := r.SaveInventoryFields(ctx, tx.Inventory, bag, r.InventoryFieldSlots); err != nil {
			return err
		}
		if err := guild.Save(ctx, tx.Settings, reg, now); err != nil {
			return err
		}
		if !req.Revalidate() {
			return guild.ErrCharacterNotFound
		}
		result = claim
		return nil
	})
	if err != nil {
		return guild.ContractClaimReceipt{}, err
	}
	return result, nil
}
